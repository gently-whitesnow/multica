package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireServicePrincipalScope(t *testing.T) {
	tests := []struct {
		name, source, scopes string
		want                 int
	}{
		{name: "exact scope", source: "service_principal", scopes: "approvals:read,projections:update", want: http.StatusNoContent},
		{name: "missing scope", source: "service_principal", scopes: "approvals:read", want: http.StatusForbidden},
		{name: "prefix is not enough", source: "service_principal", scopes: "projections:update:any", want: http.StatusForbidden},
		{name: "human cannot inject scopes", scopes: "projections:update", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			h := RequireServicePrincipalScope("projections:update")(next)
			req := httptest.NewRequest(http.MethodPatch, "/api/integration/projections/1", nil)
			req.Header.Set("X-Actor-Source", tt.source)
			req.Header.Set("X-Service-Principal-Scopes", tt.scopes)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

func TestRequireServicePrincipalScopeEmptyRequirementFailsClosed(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next called") })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Actor-Source", "service_principal")
	w := httptest.NewRecorder()
	RequireServicePrincipalScope("")(next).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
