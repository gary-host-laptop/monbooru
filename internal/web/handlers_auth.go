package web

import (
	"net/http"

	"github.com/monbooru/monbooru/internal/logx"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		// Render the login page with an inline notice instead of silently
		// redirecting - a user who bookmarked /login after disabling auth
		// otherwise gets no explanation for why the page vanished. The
		// template hides the form itself; a dead field and button only
		// suggest a login that could work.
		s.renderTemplate(w, "login.html", s.loginPageData(map[string]any{
			"Error":        "Password authentication is disabled. Enable it from Settings → Authentication.",
			"AuthDisabled": true,
		}))
		return
	}
	s.renderTemplate(w, "login.html", s.loginPageData(nil))
}

// loginPageData builds the data map for login.html. The login screen does
// not run through s.base(), so the brand-name, logo, and custom-stylesheet
// overrides have to be threaded explicitly - otherwise the configured brand
// would change every page except the login one.
func (s *Server) loginPageData(extra map[string]any) map[string]any {
	data := map[string]any{
		"Title":        "Login - " + s.booruName(),
		"CSRFToken":    s.csrfToken("anon"),
		"BooruName":    s.booruName(),
		"BooruFavicon": s.booruFaviconURL(),
		"CustomCSS":    s.customCSSPath() != "",
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	ip := clientIP(r)
	if !s.loginRL.check(ip) {
		logx.Warnf("login rate-limited from %s", ip)
		s.renderTemplate(w, "login.html", s.loginPageData(map[string]any{
			"Error": "Too many attempts. Please wait before trying again.",
		}))
		return
	}

	password := r.FormValue("password")
	if err := bcrypt.CompareHashAndPassword(
		[]byte(s.passwordHash()), []byte(password),
	); err != nil {
		s.loginRL.recordFailure(ip)
		logx.Warnf("login failed from %s", ip)
		s.renderTemplate(w, "login.html", s.loginPageData(map[string]any{
			"Error": "Invalid password",
		}))
		return
	}
	s.loginRL.recordSuccess(ip)
	logx.Infof("login success from %s", ip)

	sessID, err := s.sessions.NewSession(s.sessionLifetimeDays())
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "monbooru_session",
		Value:    sessID,
		Path:     "/",
		MaxAge:   s.sessionLifetimeDays() * 86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logoutPost(w http.ResponseWriter, r *http.Request) {
	sessID := sessionFromContext(r.Context())
	s.sessions.DeleteSession(sessID)
	http.SetCookie(w, &http.Cookie{
		Name:   "monbooru_session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// renderAuthPasswordOOB writes an out-of-band swap for the password subsection
// so the "currently enabled/disabled" text and form fields reflect the latest
// auth state without requiring a page reload.
func (s *Server) renderAuthPasswordOOB(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "partials/auth_password_section.html", map[string]any{
		"AuthEnabled": s.authEnabled(),
		"CSRFToken":   s.csrfToken(sessionFromContext(r.Context())),
		"OOB":         true,
	})
}
