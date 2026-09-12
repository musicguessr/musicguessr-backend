// Package requestid generates a short, opaque ID for every incoming HTTP
// request, threads it through the request's context, and returns it to the
// caller via a response header. It exists so a user hitting a bug can hand
// over one short string (surfaced in the frontend and easy to paste into a
// GitHub issue) that a maintainer can grep straight to the matching backend
// log lines — without it, correlating a specific user's report to our logs
// means matching on approximate timestamps and hoping nothing else
// happened at the same moment.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type ctxKey struct{}

// Header is the response header the generated ID is returned under.
const Header = "X-Request-Id"

// New returns a random 16-character hex ID. Collisions are not checked for
// — this is a debugging aid, not a uniqueness guarantee — but 8 random
// bytes makes one astronomically unlikely at this app's request volume.
func New() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is effectively unheard of on any real OS; if it
		// ever does, a fixed placeholder is still better than panicking a
		// request over what is, again, just a debugging aid.
		return "unavailable"
	}
	return hex.EncodeToString(b)
}

func withContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the request ID stored by Middleware, or "" if none is
// present (e.g. called outside a request handled through Middleware).
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Middleware generates a request ID for every request, sets it on the
// response header, and stores it on the request's context so handlers can
// attach it to log lines and JSON responses.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := New()
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(withContext(r.Context(), id)))
	})
}
