package middleware

import "net/http"

// clientControlledActorHeaders are the request headers that describe WHO the
// caller is. Every one of them is server-set only: an auth path stamps it
// after it has verified a credential, and downstream guards, handlers, audit
// and the request logger read them back as if the server wrote them.
//
// A client can send any header it likes, so each of these must be deleted
// before any code can observe it. Stripping them in one place keeps the
// contract identical no matter which middleware chain a route sits behind —
// Auth, DaemonAuth, or (for public routes) neither.
var clientControlledActorHeaders = []string{
	"X-Actor-Source",
	"X-User-ID",
	"X-User-Email",
	"X-Service-Principal-ID",
	"X-Service-Principal-Scopes",
	"X-Credential-Owner-ID",
}

// stripClientActorHeaders removes every client-supplied actor attribution
// header from r. Auth paths call it before their branches run so that only a
// verified credential can put an identity back.
func stripClientActorHeaders(r *http.Request) {
	for _, h := range clientControlledActorHeaders {
		r.Header.Del(h)
	}
}

// StripClientActorHeaders is the global-router form of stripClientActorHeaders.
// It runs ahead of routing, so routes that have no auth middleware at all
// (public endpoints, webhook ingress, health) also start from an empty actor
// identity. Without it, an unauthenticated caller could forge
// X-Actor-Source/X-Service-Principal-ID and have the request logger attribute
// its request to a machine identity that never authenticated (MAIN-329 B1).
func StripClientActorHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripClientActorHeaders(r)
		next.ServeHTTP(w, r)
	})
}
