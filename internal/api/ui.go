package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/zogami00/you-as-bee/internal/webui"
)

// loginLimiter enforces a global "one failed login per second" rate. It is
// intentionally global rather than per-address: there is one operator and one
// token, so a global bound is enough to make online guessing impractical
// without tracking clients.
type loginLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	now      func() time.Time
	lastFail time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{interval: time.Second, now: time.Now}
}

// allow reports whether a login attempt may proceed. It does not record a
// failure; call fail after a rejected attempt.
func (l *loginLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastFail.IsZero() || l.now().Sub(l.lastFail) >= l.interval
}

func (l *loginLimiter) fail() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastFail = l.now()
}

// handleLoginForm renders the token form with a fresh one-time code.
func (s *Server) handleLoginForm(w http.ResponseWriter, _ *http.Request) {
	s.renderLogin(w, http.StatusOK, "")
}

// handleLoginSubmit verifies the one-time code and the token, then issues a
// session cookie. The session lives in memory, so restarting the agent logs
// every browser out.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.login.allow() {
		s.renderLogin(w, http.StatusTooManyRequests, "Too many attempts. Wait a second and try again.")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, "Malformed form submission.")
		return
	}
	if !s.codes.Redeem(r.PostFormValue("code")) {
		s.login.fail()
		s.renderLogin(w, http.StatusBadRequest, "This sign-in form expired. Reload it and try again.")
		return
	}
	if !s.tokenEqual(r.PostFormValue("token")) {
		s.login.fail()
		s.renderLogin(w, http.StatusUnauthorized, "Invalid token.")
		return
	}

	id := s.sessions.Create()
	webui.SetCookie(w, id, webui.SessionTTL)
	http.Redirect(w, r, "/ui/", http.StatusSeeOther)
}

// handleLogout deletes the session and clears the cookie. It requires the CSRF
// header, like every other session-authenticated write.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(webui.SessionCookieName)
	if err == nil && cookie.Value != "" {
		if r.Header.Get(webui.CSRFHeader) != webui.CSRFHeaderValue {
			writeError(w, http.StatusForbidden, "forbidden", "missing CSRF header")
			return
		}
		s.sessions.Delete(cookie.Value)
	}
	webui.ClearCookie(w)
	http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
}

// handleUIIndex serves the application shell, redirecting a browser without a
// session to the login form.
func (s *Server) handleUIIndex(w http.ResponseWriter, r *http.Request) {
	if !s.sessionOK(r) {
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
		return
	}
	// yabd is the agent; the same shell is reused by the Windows client with
	// Mode "client".
	_ = webui.RenderIndex(w, webui.Page{Mode: "agent", APIBase: "/v1"})
}

// renderLogin writes the login form with a fresh one-time code.
func (s *Server) renderLogin(w http.ResponseWriter, status int, message string) {
	_ = webui.RenderLogin(w, webui.LoginPage{Code: s.codes.Issue(), Error: message}, status)
}
