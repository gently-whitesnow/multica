package handler

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var externalIssueProviderPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type externalIssueRefInput struct {
	Provider    string  `json:"provider"`
	InstanceID  string  `json:"instance_id"`
	ExternalID  string  `json:"external_id"`
	ExternalURL *string `json:"external_url,omitempty"`
}

type createExternalIssueRequest struct {
	ExternalIssueRef externalIssueRefInput `json:"external_issue_ref"`
	Issue            struct {
		Title        string  `json:"title"`
		Description  *string `json:"description,omitempty"`
		Status       string  `json:"status,omitempty"`
		Priority     string  `json:"priority,omitempty"`
		AssigneeType *string `json:"assignee_type,omitempty"`
		AssigneeID   *string `json:"assignee_id,omitempty"`
		ProjectID    *string `json:"project_id,omitempty"`
	} `json:"issue"`
}

type externalIssueRefResponse struct {
	Provider    string  `json:"provider"`
	InstanceID  string  `json:"instance_id"`
	ExternalID  string  `json:"external_id"`
	ExternalURL *string `json:"external_url"`
	IssueID     string  `json:"issue_id"`
	CreatedAt   string  `json:"created_at"`
}

type externalIssueResponse struct {
	ExternalIssueRef externalIssueRefResponse `json:"external_issue_ref"`
	Issue            IssueResponse            `json:"issue"`
	IssuePath        string                   `json:"issue_path"`
	IssueURL         string                   `json:"issue_url,omitempty"`
	Created          bool                     `json:"created"`
}

func normalizeExternalIssueRef(in externalIssueRefInput) (externalIssueRefInput, string, bool) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.InstanceID = strings.TrimSpace(in.InstanceID)
	in.ExternalID = strings.TrimSpace(in.ExternalID)
	if !externalIssueProviderPattern.MatchString(in.Provider) || !validExternalIDPart(in.InstanceID) || !validExternalIDPart(in.ExternalID) {
		return in, "provider, instance_id and external_id must be valid non-empty identifiers", false
	}
	if in.ExternalURL != nil {
		clean := strings.TrimSpace(*in.ExternalURL)
		u, err := url.Parse(clean)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" {
			return in, "external_url must be an http(s) URL without credentials or query parameters", false
		}
		in.ExternalURL = &clean
	}
	return in, "", true
}

func validExternalIDPart(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func externalIssueRefToResponse(ref db.ExternalIssueRef) externalIssueRefResponse {
	return externalIssueRefResponse{
		Provider:    ref.Provider,
		InstanceID:  ref.InstanceID,
		ExternalID:  ref.ExternalID,
		ExternalURL: textToPtr(ref.ExternalUrl),
		IssueID:     uuidToString(ref.IssueID),
		CreatedAt:   timestampToString(ref.CreatedAt),
	}
}

func (h *Handler) externalIssueResponse(ctxReq *http.Request, ref db.ExternalIssueRef, issue db.Issue, created bool) externalIssueResponse {
	prefix := h.getIssuePrefix(ctxReq.Context(), issue.WorkspaceID)
	issueResp := issueToResponse(issue, prefix)
	path := "/issues/" + url.PathEscape(issueResp.Identifier)
	if ws, err := h.Queries.GetWorkspace(ctxReq.Context(), issue.WorkspaceID); err == nil {
		path = "/" + url.PathEscape(ws.Slug) + path
	}
	resp := externalIssueResponse{
		ExternalIssueRef: externalIssueRefToResponse(ref),
		Issue:            issueResp,
		IssuePath:        path,
		Created:          created,
	}
	if appURL := resolveFrontendAppURL(); appURL != "" {
		resp.IssueURL = appURL + path
	}
	return resp
}

func (h *Handler) CreateExternalIssue(w http.ResponseWriter, r *http.Request) {
	var req createExternalIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	refInput, msg, ok := normalizeExternalIssueRef(req.ExternalIssueRef)
	if !ok {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	req.ExternalIssueRef = refInput
	req.Issue.Title = strings.TrimSpace(req.Issue.Title)
	if req.Issue.Title == "" {
		writeError(w, http.StatusBadRequest, "issue.title is required")
		return
	}
	if req.Issue.Status == "" {
		req.Issue.Status = "todo"
	}
	if req.Issue.Priority == "" {
		req.Issue.Priority = "none"
	}
	if req.Issue.AssigneeType != nil {
		clean := strings.ToLower(strings.TrimSpace(*req.Issue.AssigneeType))
		req.Issue.AssigneeType = &clean
	}
	if !validateIssueEnum(w, "status", req.Issue.Status, validIssueStatuses) || !validateIssueEnum(w, "priority", req.Issue.Priority, validIssuePriorities) {
		return
	}

	workspaceID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Workspace-ID"), "workspace_id")
	if !ok {
		return
	}
	principalID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Service-Principal-ID"), "service_principal_id")
	if !ok {
		return
	}
	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if req.Issue.AssigneeType != nil {
		assigneeType = pgtype.Text{String: *req.Issue.AssigneeType, Valid: true}
	}
	if req.Issue.AssigneeID != nil {
		id, parsed := parseUUIDOrBadRequest(w, *req.Issue.AssigneeID, "issue.assignee_id")
		if !parsed {
			return
		}
		assigneeID = id
	}
	if status, errMsg := h.validateAssigneePair(r.Context(), r, uuidToString(workspaceID), assigneeType, assigneeID); status != 0 {
		writeError(w, status, errMsg)
		return
	}
	var projectID pgtype.UUID
	if req.Issue.ProjectID != nil {
		id, parsed := parseUUIDOrBadRequest(w, *req.Issue.ProjectID, "issue.project_id")
		if !parsed {
			return
		}
		projectID = id
	}

	canonicalPayload := struct {
		ExternalIssueRef externalIssueRefInput `json:"external_issue_ref"`
		Title            string                `json:"title"`
		Description      *string               `json:"description"`
		Status           string                `json:"status"`
		Priority         string                `json:"priority"`
		AssigneeType     *string               `json:"assignee_type"`
		AssigneeID       *string               `json:"assignee_id"`
		ProjectID        *string               `json:"project_id"`
	}{
		ExternalIssueRef: refInput,
		Title:            req.Issue.Title, Description: req.Issue.Description,
		Status: req.Issue.Status, Priority: req.Issue.Priority,
		AssigneeType: req.Issue.AssigneeType,
	}
	if assigneeID.Valid {
		id := uuidToString(assigneeID)
		canonicalPayload.AssigneeID = &id
	}
	if projectID.Valid {
		id := uuidToString(projectID)
		canonicalPayload.ProjectID = &id
	}
	canonical, _ := json.Marshal(canonicalPayload)
	digest := sha256.Sum256(canonical)
	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:    workspaceID,
		Title:          req.Issue.Title,
		Description:    ptrToText(req.Issue.Description),
		Status:         req.Issue.Status,
		Priority:       req.Issue.Priority,
		AssigneeType:   assigneeType,
		AssigneeID:     assigneeID,
		CreatorType:    "service_principal",
		CreatorID:      principalID,
		ProjectID:      projectID,
		AllowDuplicate: true,
		ExternalRef: &service.ExternalIssueRefCreateParams{
			Provider:                    refInput.Provider,
			InstanceID:                  refInput.InstanceID,
			ExternalID:                  refInput.ExternalID,
			PayloadHash:                 digest[:],
			ExternalURL:                 ptrToText(refInput.ExternalURL),
			CreatedByServicePrincipalID: principalID,
		},
	}, service.IssueCreateOpts{
		ActorID:         uuidToString(principalID),
		Platform:        "integration",
		SuppressEnqueue: true,
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
			return map[string]any{"issue": issueToResponse(issue, h.getIssuePrefix(r.Context(), issue.WorkspaceID))}
		},
	})
	if errors.Is(err, service.ErrExternalIssuePayloadConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":               "external_issue_payload_conflict",
			"error":              "external issue key is already bound to a different payload",
			"external_issue_ref": externalIssueRefToResponse(*result.ExternalRef),
		})
		return
	}
	if errors.Is(err, service.ErrProjectNotFound) {
		writeError(w, http.StatusBadRequest, "project not found in this workspace")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create external issue")
		return
	}
	status := http.StatusCreated
	if result.Existing {
		status = http.StatusOK
	}
	writeJSON(w, status, h.externalIssueResponse(r, *result.ExternalRef, result.Issue, !result.Existing))
}

func (h *Handler) GetExternalIssue(w http.ResponseWriter, r *http.Request) {
	input, msg, ok := normalizeExternalIssueRef(externalIssueRefInput{
		Provider: r.URL.Query().Get("provider"), InstanceID: r.URL.Query().Get("instance_id"), ExternalID: r.URL.Query().Get("external_id"),
	})
	if !ok {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Workspace-ID"), "workspace_id")
	if !ok {
		return
	}
	ref, err := h.Queries.GetExternalIssueRef(r.Context(), db.GetExternalIssueRefParams{
		WorkspaceID: workspaceID, Provider: input.Provider, InstanceID: input.InstanceID, ExternalID: input.ExternalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "external issue reference not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get external issue reference")
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: ref.IssueID, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "referenced issue not found")
		return
	}
	writeJSON(w, http.StatusOK, h.externalIssueResponse(r, ref, issue, false))
}
