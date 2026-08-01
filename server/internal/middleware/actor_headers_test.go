package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
)

// forgedActorHeaders is what an attacker sends: every server-set actor
// attribution header, filled with values it has no credential for.
func forgedActorHeaders(req *http.Request) *http.Request {
	req.Header.Set("X-Actor-Source", servicePrincipalSource)
	req.Header.Set("X-User-ID", "forged-user")
	req.Header.Set("X-User-Email", "forged@example.com")
	req.Header.Set("X-Service-Principal-ID", "forged-principal")
	req.Header.Set("X-Service-Principal-Scopes", "approvals:decide")
	req.Header.Set("X-Credential-Owner-ID", "forged-owner")
	return req
}

// TestStripClientActorHeaders_PublicRoute pins the global strip: a public
// endpoint sits behind no auth middleware at all, so without this middleware
// the handler (and the request logger behind it) would read the caller's own
// forged actor identity straight out of r.Header.
func TestStripClientActorHeaders_PublicRoute(t *testing.T) {
	t.Parallel()

	var seen http.Header
	handler := StripClientActorHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(),
		forgedActorHeaders(httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)))

	for _, h := range clientControlledActorHeaders {
		if got := seen.Get(h); got != "" {
			t.Fatalf("public route saw client-supplied %s = %q", h, got)
		}
	}
}

// TestRequestLogger_ForgedMachineAttributionOnUnauthenticatedRoute is the
// MAIN-329 B1 regression: an anonymous caller must not be able to write
// actor_type=service_principal plus arbitrary principal/owner IDs into the
// structured access log. The chain here is exactly the global one from
// router.go, minus any auth middleware — i.e. a public route.
func TestRequestLogger_ForgedMachineAttributionOnUnauthenticatedRoute(t *testing.T) {
	logs := withCapturedLogs(t)
	handler := StripClientActorHeaders(RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	handler.ServeHTTP(httptest.NewRecorder(),
		forgedActorHeaders(httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)))

	out := logs.String()
	for _, forged := range []string{"actor_type=service_principal", "forged-principal", "forged-owner", "forged-user"} {
		if strings.Contains(out, forged) {
			t.Fatalf("forged %q reached the access log:\n%s", forged, out)
		}
	}
}

// TestRequestLogger_ForgedPrincipalIDWithoutActorSource pins the logger's own
// trust rule independently of the strip: even if a principal ID somehow
// reaches the logger, attribution keys off the server-set X-Actor-Source, so
// the line stays anonymous.
func TestRequestLogger_ForgedPrincipalIDWithoutActorSource(t *testing.T) {
	logs := withCapturedLogs(t)
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.Header.Set("X-Service-Principal-ID", "forged-principal")
	req.Header.Set("X-Credential-Owner-ID", "forged-owner")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if out := logs.String(); strings.Contains(out, "actor_type=service_principal") {
		t.Fatalf("attribution written without a server-set actor source:\n%s", out)
	}
}

// TestRequestLogger_AuthenticatedMachineActorIsAttributed is the positive
// half: once an auth path has stamped X-Actor-Source, the log line still
// names the machine actor and its accountable owner separately. The stand-in
// handler mutates the same header map the msp_ branch of Auth writes to;
// DB-backed coverage of that branch lives in the handler package
// (assertServicePrincipalAuth).
func TestRequestLogger_AuthenticatedMachineActorIsAttributed(t *testing.T) {
	logs := withCapturedLogs(t)
	handler := StripClientActorHeaders(RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Actor-Source", servicePrincipalSource)
		r.Header.Set("X-Service-Principal-ID", "principal-1")
		r.Header.Set("X-Credential-Owner-ID", "owner-1")
		w.WriteHeader(http.StatusOK)
	})))
	handler.ServeHTTP(httptest.NewRecorder(),
		forgedActorHeaders(httptest.NewRequest(http.MethodGet, "/api/service-principal/identity", nil)))

	out := logs.String()
	for _, want := range []string{"actor_type=service_principal", "actor_id=principal-1", "credential_owner_id=owner-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in access log, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "forged-principal") || strings.Contains(out, "forged-owner") {
		t.Fatalf("forged values survived alongside the real ones:\n%s", out)
	}
}

// TestDaemonAuth_StripsForgedActorHeaders covers the daemon path: a valid
// mdt_ token authenticates a daemon, not a person and not a machine identity,
// so the request must reach the handler with no actor attribution at all even
// though the caller supplied a full forged set.
func TestDaemonAuth_StripsForgedActorHeaders(t *testing.T) {
	rdb := newRedisTestClient(t)
	cache := auth.NewDaemonTokenCache(rdb)

	const rawToken = "mdt_forged_actor_headers_token"
	cache.Set(context.Background(), auth.HashToken(rawToken), auth.DaemonTokenIdentity{
		WorkspaceID: "ws-daemon",
		DaemonID:    "daemon-1",
	}, auth.AuthCacheTTL)

	var seen http.Header
	var gotWS, gotDaemon string
	handler := DaemonAuth(nil, nil, cache, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		gotWS = DaemonWorkspaceIDFromContext(r.Context())
		gotDaemon = DaemonIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := forgedActorHeaders(httptest.NewRequest(http.MethodPost, "/api/daemon/heartbeat", nil))
	req.Header.Set("Authorization", "Bearer "+rawToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on daemon token, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, h := range clientControlledActorHeaders {
		if got := seen.Get(h); got != "" {
			t.Fatalf("daemon route saw client-supplied %s = %q", h, got)
		}
	}
	// No regression in the daemon's own attribution: identity still comes
	// from the token, via context rather than headers.
	if gotWS != "ws-daemon" || gotDaemon != "daemon-1" {
		t.Fatalf("daemon attribution lost: got (%q, %q)", gotWS, gotDaemon)
	}
}

// TestDaemonAuth_ForgedHeadersDoNotSurviveCloudPAT guards both directions on
// a daemon path that resolves a real identity: the forged machine attribution
// must be gone, while the identity DaemonAuth itself stamped (owner user +
// cloud_pat actor source) must survive the wider strip untouched.
func TestDaemonAuth_ForgedHeadersDoNotSurviveCloudPAT(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true,"owner_id":"owner-real","instance_id":"i-1"}`))
	}))
	defer srv.Close()

	verifier := auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{FleetBaseURL: srv.URL})
	var seen http.Header
	handler := DaemonAuth(nil, nil, nil, verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))

	req := forgedActorHeaders(httptest.NewRequest(http.MethodPost, "/api/daemon/heartbeat", nil))
	req.Header.Set("Authorization", "Bearer mcn_valid")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on cloud PAT, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := seen.Get("X-User-ID"); got != "owner-real" {
		t.Fatalf("X-User-ID = %q, want the fleet-resolved owner", got)
	}
	if got := seen.Get("X-Actor-Source"); got != "cloud_pat" {
		t.Fatalf("X-Actor-Source = %q, want cloud_pat", got)
	}
	for _, h := range []string{"X-User-Email", "X-Service-Principal-ID", "X-Service-Principal-Scopes", "X-Credential-Owner-ID"} {
		if got := seen.Get(h); got != "" {
			t.Fatalf("daemon route saw client-supplied %s = %q", h, got)
		}
	}
}
