package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"log"
	"mime"
	"net/http"
	"strings"
)

// mustRandBytes returns n cryptographically-random bytes and terminates the
// process if the system RNG is unavailable; the CSRF secret is computed once
// at server startup so failing loudly is preferable to silently rolling a
// deterministic value.
func mustRandBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("FATAL: failed to generate CSRF secret: %v", err)
	}
	return b
}

// csrfToken computes a token for the given session ID using HMAC-SHA256
// with the Server's per-instance secret.
func (s *Server) csrfToken(sessionID string) string {
	mac := hmac.New(sha256.New, s.csrfSecret)
	mac.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validateCSRF checks whether the token matches the session ID.
func (s *Server) validateCSRF(sessionID, token string) bool {
	expected := s.csrfToken(sessionID)
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

// parseFormOK wraps r.ParseForm and surfaces a malformed body as a 400
// instead of letting the caller silently observe blank form values.
// Returns false when the body could not be parsed; the response has
// already been written so the caller should just `return`. HTMX
// requests get a flash-err so the partial swap shows the failure
// inline, everything else gets a plain http.Error.
func parseFormOK(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		flashErr(w, r, "Bad form data: "+err.Error())
		return false
	}
	return true
}

// CSRFMiddleware validates the CSRF token on mutating requests.
// /api/v1/ routes are exempt (bearer token serves as CSRF mitigation).
func (s *Server) CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only validate on state-changing methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// API routes are exempt
		if len(r.URL.Path) >= 8 && r.URL.Path[:8] == "/api/v1/" {
			next.ServeHTTP(w, r)
			return
		}

		// So is a mounted plugin's own page: its forms are the peer's, with
		// no monbooru token to carry. The mount is session-gated and reaches
		// nothing but a peer the operator approved.
		if strings.HasPrefix(r.URL.Path, pluginMountPrefix) {
			next.ServeHTTP(w, r)
			return
		}

		sessID := sessionFromContext(r.Context())

		// Header first so a caller can skip implicit form parsing, which
		// would otherwise drain the entire body before the handler can
		// validate sensitive fields (the gallery import path's
		// type-to-confirm gate is the canonical example). Fall back to
		// the hidden form input so existing form submissions still work.
		token := r.Header.Get("X-CSRF-Token")
		if token == "" && !isMultipart(r) {
			// r.FormValue on multipart drains the entire body through
			// stdlib's 32 MiB ParseMultipartForm, defeating any handler-
			// side MaxBytesReader. Multipart callers must supply the
			// token via the header (set by htmx-on-multipart hx-headers
			// or by an explicit XHR header).
			token = r.FormValue("_csrf")
		}

		if !s.validateCSRF(sessID, token) {
			http.Error(w, "CSRF token invalid", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isMultipart returns true when the request's Content-Type top-level
// type is multipart (multipart/form-data, multipart/related, etc.).
// Used to skip the body-draining FormValue fallback in CSRFMiddleware.
func isMultipart(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return len(mt) > 10 && mt[:10] == "multipart/"
}
