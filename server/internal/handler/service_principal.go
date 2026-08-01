package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var servicePrincipalScopes = map[string]struct{}{
	"projections:create": {},
	"projections:read":   {},
	"projections:update": {},
	"approvals:read":     {},
	"approvals:decide":   {},
}

type ServicePrincipalResponse struct {
	ID                string   `json:"id"`
	WorkspaceID       string   `json:"workspace_id"`
	OwnerUserID       string   `json:"owner_user_id"`
	Name              string   `json:"name"`
	Scopes            []string `json:"scopes"`
	TokenPrefix       string   `json:"token_prefix"`
	CredentialVersion int32    `json:"credential_version"`
	Status            string   `json:"status"`
	LastUsedAt        *string  `json:"last_used_at"`
	CreatedAt         string   `json:"created_at"`
	RevokedAt         *string  `json:"revoked_at"`
}

type ServicePrincipalCredentialResponse struct {
	ServicePrincipalResponse
	Credential string `json:"credential"`
}

type createServicePrincipalRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

func servicePrincipalResponse(p db.ServicePrincipal) ServicePrincipalResponse {
	return ServicePrincipalResponse{
		ID:                uuidToString(p.ID),
		WorkspaceID:       uuidToString(p.WorkspaceID),
		OwnerUserID:       uuidToString(p.OwnerUserID),
		Name:              p.Name,
		Scopes:            p.Scopes,
		TokenPrefix:       p.TokenPrefix,
		CredentialVersion: p.CredentialVersion,
		Status:            p.Status,
		LastUsedAt:        timestampToPtr(p.LastUsedAt),
		CreatedAt:         timestampToString(p.CreatedAt),
		RevokedAt:         timestampToPtr(p.RevokedAt),
	}
}

func validateServicePrincipalScopes(scopes []string) ([]string, bool) {
	if len(scopes) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if _, ok := servicePrincipalScopes[scope]; !ok {
			return nil, false
		}
		seen[scope] = struct{}{}
	}
	clean := make([]string, 0, len(seen))
	for scope := range seen {
		clean = append(clean, scope)
	}
	sort.Strings(clean)
	return clean, true
}

func servicePrincipalTokenPrefix(token string) string {
	if len(token) > 12 {
		return token[:12]
	}
	return token
}

func (h *Handler) CreateServicePrincipal(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := chi.URLParam(r, "id")
	var req createServicePrincipalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	scopes, valid := validateServicePrincipalScopes(req.Scopes)
	if req.Name == "" || !valid {
		writeError(w, http.StatusBadRequest, "name and valid scopes are required")
		return
	}
	raw, err := auth.GenerateServicePrincipalToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate credential")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create service principal")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	principal, err := q.CreateServicePrincipal(r.Context(), db.CreateServicePrincipalParams{
		WorkspaceID:     parseUUID(workspaceID),
		OwnerUserID:     parseUUID(userID),
		CreatedByUserID: parseUUID(userID),
		Name:            req.Name,
		Scopes:          scopes,
		TokenHash:       auth.HashToken(raw),
		TokenPrefix:     servicePrincipalTokenPrefix(raw),
	})
	if err == nil {
		details, _ := json.Marshal(map[string]any{"scopes": scopes, "credential_version": 1})
		err = q.CreateServicePrincipalAudit(r.Context(), db.CreateServicePrincipalAuditParams{
			WorkspaceID: principal.WorkspaceID, ServicePrincipalID: principal.ID,
			ActorType: "member", ActorID: parseUUID(userID), OwnerUserID: principal.OwnerUserID,
			Action: "created", Details: details,
		})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "failed to create service principal")
		return
	}
	writeJSON(w, http.StatusCreated, ServicePrincipalCredentialResponse{
		ServicePrincipalResponse: servicePrincipalResponse(principal), Credential: raw,
	})
}

func (h *Handler) ListServicePrincipals(w http.ResponseWriter, r *http.Request) {
	principals, err := h.Queries.ListServicePrincipalsByWorkspace(r.Context(), parseUUID(chi.URLParam(r, "id")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list service principals")
		return
	}
	response := make([]ServicePrincipalResponse, len(principals))
	for i, principal := range principals {
		response[i] = servicePrincipalResponse(principal)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) RotateServicePrincipalCredential(w http.ResponseWriter, r *http.Request) {
	h.mutateServicePrincipalCredential(w, r, "rotated")
}

func (h *Handler) RevokeServicePrincipal(w http.ResponseWriter, r *http.Request) {
	h.mutateServicePrincipalCredential(w, r, "revoked")
}

func (h *Handler) mutateServicePrincipalCredential(w http.ResponseWriter, r *http.Request, action string) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	principalID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "principalId"), "service principal id")
	if !ok {
		return
	}
	var raw string
	var err error
	if action == "rotated" {
		raw, err = auth.GenerateServicePrincipalToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate credential")
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update service principal")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	var principal db.ServicePrincipal
	if action == "rotated" {
		principal, err = q.RotateServicePrincipalCredential(r.Context(), db.RotateServicePrincipalCredentialParams{
			ID: principalID, WorkspaceID: workspaceID,
			TokenHash: auth.HashToken(raw), TokenPrefix: servicePrincipalTokenPrefix(raw),
		})
	} else {
		principal, err = q.RevokeServicePrincipal(r.Context(), db.RevokeServicePrincipalParams{ID: principalID, WorkspaceID: workspaceID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "active service principal not found")
		return
	}
	if err == nil {
		details, _ := json.Marshal(map[string]any{"credential_version": principal.CredentialVersion})
		err = q.CreateServicePrincipalAudit(r.Context(), db.CreateServicePrincipalAuditParams{
			WorkspaceID: workspaceID, ServicePrincipalID: principal.ID,
			ActorType: "member", ActorID: parseUUID(userID), OwnerUserID: principal.OwnerUserID,
			Action: action, Details: details,
		})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "failed to update service principal")
		return
	}
	if action == "revoked" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, ServicePrincipalCredentialResponse{
		ServicePrincipalResponse: servicePrincipalResponse(principal), Credential: raw,
	})
}

func (h *Handler) GetServicePrincipalIdentity(w http.ResponseWriter, r *http.Request) {
	scopes := strings.Split(r.Header.Get("X-Service-Principal-Scopes"), ",")
	writeJSON(w, http.StatusOK, map[string]any{
		"actor_type":    "service_principal",
		"id":            r.Header.Get("X-Service-Principal-ID"),
		"workspace_id":  r.Header.Get("X-Workspace-ID"),
		"owner_user_id": r.Header.Get("X-Credential-Owner-ID"),
		"scopes":        scopes,
	})
}
