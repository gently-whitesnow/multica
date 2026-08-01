package middleware

import (
	"net/http"
	"strings"
)

const servicePrincipalSource = "service_principal"

// RequireServicePrincipalScope is the explicit opt-in gate for integration
// endpoints. Missing, malformed and unknown scopes all fail closed.
func RequireServicePrincipalScope(required string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Actor-Source") != servicePrincipalSource || required == "" {
				writeError(w, http.StatusForbidden, "service principal scope required")
				return
			}
			for _, scope := range strings.Split(r.Header.Get("X-Service-Principal-Scopes"), ",") {
				if scope == required {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeError(w, http.StatusForbidden, "insufficient service principal scope")
		})
	}
}

// RequireServicePrincipal authenticates a machine identity without granting a
// business capability. It is used only for identity introspection.
func RequireServicePrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Actor-Source") != servicePrincipalSource || r.Header.Get("X-Service-Principal-ID") == "" {
			writeError(w, http.StatusForbidden, "service principal required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
