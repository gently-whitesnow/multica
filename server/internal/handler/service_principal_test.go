package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
)

func TestServicePrincipalCredentialLifecycle(t *testing.T) {
	createReq := withURLParam(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/service-principals", map[string]any{
		"name":   "approval bridge",
		"scopes": []string{"approvals:read", "projections:read", "approvals:read"},
	}), "id", testWorkspaceID)
	createW := httptest.NewRecorder()
	testHandler.CreateServicePrincipal(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", createW.Code, createW.Body.String())
	}
	var created ServicePrincipalCredentialResponse
	if err := json.NewDecoder(createW.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if !strings.HasPrefix(created.Credential, "msp_") || created.ID == "" {
		t.Fatalf("create did not return one-time credential and id: %+v", created)
	}
	if len(created.Scopes) != 2 {
		t.Fatalf("scopes were not normalized: %v", created.Scopes)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM service_principal_audit WHERE service_principal_id = $1`, parseUUID(created.ID))
		testPool.Exec(context.Background(), `DELETE FROM service_principal WHERE id = $1`, parseUUID(created.ID))
	})

	listReq := withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/service-principals", nil), "id", testWorkspaceID)
	listW := httptest.NewRecorder()
	testHandler.ListServicePrincipals(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listW.Code, listW.Body.String())
	}
	if strings.Contains(listW.Body.String(), created.Credential) || strings.Contains(listW.Body.String(), auth.HashToken(created.Credential)) {
		t.Fatal("list response leaked credential material")
	}

	assertServicePrincipalAuth(t, created.Credential, http.StatusNoContent)

	rotateReq := withURLParams(newRequest(http.MethodPost, "/rotate", nil), "id", testWorkspaceID, "principalId", created.ID)
	rotateW := httptest.NewRecorder()
	testHandler.RotateServicePrincipalCredential(rotateW, rotateReq)
	if rotateW.Code != http.StatusOK {
		t.Fatalf("rotate status = %d: %s", rotateW.Code, rotateW.Body.String())
	}
	var rotated ServicePrincipalCredentialResponse
	if err := json.NewDecoder(rotateW.Body).Decode(&rotated); err != nil {
		t.Fatalf("decode rotate response: %v", err)
	}
	if rotated.Credential == created.Credential || rotated.CredentialVersion != 2 {
		t.Fatalf("rotation did not replace credential: %+v", rotated)
	}
	assertServicePrincipalAuth(t, created.Credential, http.StatusUnauthorized)
	assertServicePrincipalAuth(t, rotated.Credential, http.StatusNoContent)

	revokeReq := withURLParams(newRequest(http.MethodDelete, "/revoke", nil), "id", testWorkspaceID, "principalId", created.ID)
	revokeW := httptest.NewRecorder()
	testHandler.RevokeServicePrincipal(revokeW, revokeReq)
	if revokeW.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d: %s", revokeW.Code, revokeW.Body.String())
	}
	assertServicePrincipalAuth(t, rotated.Credential, http.StatusUnauthorized)

	var actions []string
	rows, err := testPool.Query(context.Background(), `
		SELECT action FROM service_principal_audit
		WHERE service_principal_id = $1 ORDER BY created_at`, parseUUID(created.ID))
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
	}
	if strings.Join(actions, ",") != "created,rotated,revoked" {
		t.Fatalf("audit actions = %v", actions)
	}
}

func assertServicePrincipalAuth(t *testing.T, credential string, want int) {
	t.Helper()
	called := false
	h := middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("X-Actor-Source") != "service_principal" || r.Header.Get("X-Service-Principal-ID") == "" {
			t.Fatalf("missing machine attribution headers")
		}
		if r.Header.Get("X-User-ID") != "" {
			t.Fatal("service principal impersonated its credential owner")
		}
		if r.Header.Get("X-Service-Principal-Scopes") == "forged:scope" || r.Header.Get("X-Credential-Owner-ID") == "forged-owner" {
			t.Fatal("client-supplied machine identity headers were trusted")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/service-principal/identity", nil)
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("X-User-ID", "forged-user")
	req.Header.Set("X-Service-Principal-Scopes", "forged:scope")
	req.Header.Set("X-Credential-Owner-ID", "forged-owner")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("auth status = %d, want %d: %s", w.Code, want, w.Body.String())
	}
	if want == http.StatusNoContent && !called {
		t.Fatal("authenticated request did not reach next handler")
	}
}

func TestCreateServicePrincipalRejectsUnknownScope(t *testing.T) {
	req := withURLParam(newRequest(http.MethodPost, "/", map[string]any{
		"name": "bad", "scopes": []string{"agents:run"},
	}), "id", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.CreateServicePrincipal(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestServicePrincipalHashIsNotRecoverable(t *testing.T) {
	if _, err := testHandler.Queries.GetServicePrincipalByTokenHash(context.Background(), auth.HashToken("msp_missing")); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown hash must fail closed, got %v", err)
	}
}
