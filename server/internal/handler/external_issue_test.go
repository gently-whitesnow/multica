package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
)

func externalIssueRequest(provider, instanceID, externalID, title string) *http.Request {
	req := newRequest(http.MethodPost, "/api/integration/issues", map[string]any{
		"external_issue_ref": map[string]any{
			"provider": provider, "instance_id": instanceID, "external_id": externalID,
			"external_url": "https://overtime.example/tasks/" + externalID,
		},
		"issue": map[string]any{"title": title, "status": "backlog"},
	})
	req.Header.Del("X-User-ID")
	req.Header.Set("X-Actor-Source", "service_principal")
	req.Header.Set("X-Service-Principal-ID", uuid.NewString())
	return req
}

func cleanupExternalIssue(t *testing.T, provider, instanceID, externalID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id IN (
			SELECT issue_id FROM external_issue_ref
			WHERE workspace_id = $1 AND provider = $2 AND instance_id = $3 AND external_id = $4
		)`, testWorkspaceID, provider, instanceID, externalID)
		_, _ = testPool.Exec(ctx, `DELETE FROM external_issue_ref
			WHERE workspace_id = $1 AND provider = $2 AND instance_id = $3 AND external_id = $4`,
			testWorkspaceID, provider, instanceID, externalID)
	})
}

func decodeExternalIssueResponse(t *testing.T, w *httptest.ResponseRecorder) externalIssueResponse {
	t.Helper()
	var response externalIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode external issue response: %v", err)
	}
	return response
}

func TestCreateExternalIssueConcurrentCreateOrGetAndLostResponseRetry(t *testing.T) {
	provider, instanceID, externalID := "overtime", "primary", uuid.NewString()
	cleanupExternalIssue(t, provider, instanceID, externalID)

	var wg sync.WaitGroup
	recorders := make([]*httptest.ResponseRecorder, 2)
	for i := range recorders {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			testHandler.CreateExternalIssue(w, externalIssueRequest(provider, instanceID, externalID, "Concurrent projection"))
			recorders[index] = w
		}(i)
	}
	wg.Wait()

	statuses := map[int]int{}
	issueIDs := map[string]struct{}{}
	for _, w := range recorders {
		statuses[w.Code]++
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("concurrent status = %d: %s", w.Code, w.Body.String())
		}
		issueIDs[decodeExternalIssueResponse(t, w).Issue.ID] = struct{}{}
	}
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusOK] != 1 || len(issueIDs) != 1 {
		t.Fatalf("create/get results = statuses %#v, issue ids %#v", statuses, issueIDs)
	}

	// Simulate a client losing the first response, then retrying after the issue
	// has become terminal. Identity remains bound and terminal state is preserved.
	var issueID string
	for id := range issueIDs {
		issueID = id
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET status = 'done' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("mark issue terminal: %v", err)
	}
	retry := httptest.NewRecorder()
	testHandler.CreateExternalIssue(retry, externalIssueRequest(provider, instanceID, externalID, "Concurrent projection"))
	if retry.Code != http.StatusOK {
		t.Fatalf("retry status = %d: %s", retry.Code, retry.Body.String())
	}
	retried := decodeExternalIssueResponse(t, retry)
	if retried.Issue.ID != issueID || retried.Issue.Status != "done" || retried.Created {
		t.Fatalf("retry changed identity or terminal state: %+v", retried)
	}
}

func TestCreateExternalIssuePayloadConflictDoesNotMutate(t *testing.T) {
	provider, instanceID, externalID := "overtime", "conflict", uuid.NewString()
	cleanupExternalIssue(t, provider, instanceID, externalID)
	first := httptest.NewRecorder()
	testHandler.CreateExternalIssue(first, externalIssueRequest(provider, instanceID, externalID, "Original payload"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}
	created := decodeExternalIssueResponse(t, first)

	conflict := httptest.NewRecorder()
	testHandler.CreateExternalIssue(conflict, externalIssueRequest(provider, instanceID, externalID, "Changed payload"))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d: %s", conflict.Code, conflict.Body.String())
	}
	var title string
	if err := testPool.QueryRow(context.Background(), `SELECT title FROM issue WHERE id = $1`, created.Issue.ID).Scan(&title); err != nil {
		t.Fatalf("read original issue: %v", err)
	}
	if title != "Original payload" {
		t.Fatalf("conflicting retry mutated title to %q", title)
	}
}

func TestGetExternalIssueIsWorkspaceIsolated(t *testing.T) {
	provider, instanceID, externalID := "overtime", "tenant", uuid.NewString()
	cleanupExternalIssue(t, provider, instanceID, externalID)
	createdW := httptest.NewRecorder()
	testHandler.CreateExternalIssue(createdW, externalIssueRequest(provider, instanceID, externalID, "Tenant projection"))
	if createdW.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", createdW.Code, createdW.Body.String())
	}

	foreignWorkspaceID := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO workspace (id, name, slug, issue_prefix) VALUES ($1, $2, $3, 'TEN')`,
		foreignWorkspaceID, "External ref foreign tenant", "external-ref-"+externalID[:8]); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})

	path := fmt.Sprintf("/api/integration/issues?provider=%s&instance_id=%s&external_id=%s", provider, instanceID, externalID)
	req := newRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.GetExternalIssue(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign workspace status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestDeleteExternallyReferencedIssueIsRejected(t *testing.T) {
	provider, instanceID, externalID := "overtime", "delete", uuid.NewString()
	cleanupExternalIssue(t, provider, instanceID, externalID)
	createdW := httptest.NewRecorder()
	testHandler.CreateExternalIssue(createdW, externalIssueRequest(provider, instanceID, externalID, "Protected projection"))
	created := decodeExternalIssueResponse(t, createdW)

	deleteW := httptest.NewRecorder()
	testHandler.DeleteIssue(deleteW, withURLParam(newRequest(http.MethodDelete, "/api/issues/"+created.Issue.ID, nil), "id", created.Issue.ID))
	if deleteW.Code != http.StatusConflict {
		t.Fatalf("delete status = %d, want 409: %s", deleteW.Code, deleteW.Body.String())
	}
}

func TestCreateExternalIssueRequiresExactServicePrincipalScope(t *testing.T) {
	provider, instanceID, externalID := "overtime", "auth", uuid.NewString()
	cleanupExternalIssue(t, provider, instanceID, externalID)
	handler := middleware.RequireServicePrincipalScope("projections:create")(http.HandlerFunc(testHandler.CreateExternalIssue))

	for _, tc := range []struct {
		name, source, scopes string
		want                 int
	}{
		{name: "human", scopes: "projections:create", want: http.StatusForbidden},
		{name: "read only", source: "service_principal", scopes: "projections:read", want: http.StatusForbidden},
		{name: "prefix", source: "service_principal", scopes: "projections:create:any", want: http.StatusForbidden},
		{name: "create", source: "service_principal", scopes: "projections:create", want: http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := externalIssueRequest(provider, instanceID, externalID, "Authorized projection")
			req.Header.Set("X-Actor-Source", tc.source)
			req.Header.Set("X-Service-Principal-Scopes", tc.scopes)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestCreateExternalIssueDoesNotGrantAgentExecution(t *testing.T) {
	provider, instanceID, externalID := "overtime", "no-exec", uuid.NewString()
	cleanupExternalIssue(t, provider, instanceID, externalID)
	agentID := createHandlerTestAgent(t, "External projection no execution "+externalID[:8], nil)
	req := externalIssueRequest(provider, instanceID, externalID, "Assigned projection")
	req = newRequest(http.MethodPost, "/api/integration/issues", map[string]any{
		"external_issue_ref": map[string]any{"provider": provider, "instance_id": instanceID, "external_id": externalID},
		"issue": map[string]any{
			"title": "Assigned projection", "status": "todo", "assignee_type": "agent", "assignee_id": agentID,
		},
	})
	req.Header.Del("X-User-ID")
	req.Header.Set("X-Actor-Source", "service_principal")
	req.Header.Set("X-Service-Principal-ID", uuid.NewString())
	w := httptest.NewRecorder()
	testHandler.CreateExternalIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", w.Code, w.Body.String())
	}
	created := decodeExternalIssueResponse(t, w)
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, created.Issue.ID).Scan(&count); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("service-principal projection started %d agent task(s)", count)
	}
}

func TestCreateExternalIssueRetryReturnsOriginalAfterAssigneeArchived(t *testing.T) {
	provider, instanceID, externalID := "overtime", "archived-assignee", uuid.NewString()
	cleanupExternalIssue(t, provider, instanceID, externalID)
	agentID := createHandlerTestAgent(t, "External projection archived retry "+externalID[:8], nil)
	request := func() *http.Request {
		req := newRequest(http.MethodPost, "/api/integration/issues", map[string]any{
			"external_issue_ref": map[string]any{
				"provider": provider, "instance_id": instanceID, "external_id": externalID,
			},
			"issue": map[string]any{
				"title": "Assigned projection", "status": "backlog",
				"assignee_type": "agent", "assignee_id": agentID,
			},
		})
		req.Header.Del("X-User-ID")
		req.Header.Set("X-Actor-Source", "service_principal")
		req.Header.Set("X-Service-Principal-ID", uuid.NewString())
		return req
	}

	createdW := httptest.NewRecorder()
	testHandler.CreateExternalIssue(createdW, request())
	if createdW.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", createdW.Code, createdW.Body.String())
	}
	created := decodeExternalIssueResponse(t, createdW)

	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("archive assignee: %v", err)
	}

	// Simulate a lost 201 response. The immutable binding already exists, so
	// an identical retry must resolve it even if mutable assignee state changed.
	retryW := httptest.NewRecorder()
	testHandler.CreateExternalIssue(retryW, request())
	if retryW.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200: %s", retryW.Code, retryW.Body.String())
	}
	retried := decodeExternalIssueResponse(t, retryW)
	if retried.Created || retried.Issue.ID != created.Issue.ID {
		t.Fatalf("retry did not return original issue: %+v", retried)
	}
}
