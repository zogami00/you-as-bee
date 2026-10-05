package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/zogami00/you-as-bee/internal/webui"
)

// maxLoginPeers caps the per-peer login limiter table. The allowlist already
// bounds which peers can reach the route; this only stops that bounded set from
// growing the map without limit. When the cap is reached the oldest entry is
// evicted.
const maxLoginPeers = 1024

// maxLoginBody bounds the POST /ui/login body. The whole body is at most a
// token and (for the Windows flow) a one-time code, so anything larger is junk
// and must not be read into memory by an unauthenticated caller.
const maxLoginBody = 4 << 10

// loginLimiter enforces a per-peer "one login attempt per second" rate.
//
// It is per-peer, not global: a global limiter let any client hold /ui/login at
// 429 and lock the real operator out, and the pre-auth login route needs no
// token to reach. Keying on the allowlisted peer address means one client can
// only throttle itself.
type loginLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	max      int
	now      func() time.Time
	last     map[string]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		interval: time.Second,
		max:      maxLoginPeers,
		now:      time.Now,
		last:     make(map[string]time.Time),
	}
}

// allow reports whether an attempt from peer may proceed, and records it in the
// same critical section. Reserving on entry is what closes the check-then-act
// gap: a concurrent burst cannot all observe "no recent failure" before the
// first one is recorded. A valid token clears the reservation with succeed; a
// wrong token leaves it in place, which is the failure the limiter slows down.
func (l *loginLimiter) allow(peer string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if last, ok := l.last[peer]; ok {
		if now.Sub(last) < l.interval {
			return false
		}
	} else if len(l.last) >= l.max {
		l.evictOldestLocked()
	}
	l.last[peer] = now
	return true
}

// succeed clears a peer's reservation after a valid token, so a successful
// login is not counted against the limiter.
func (l *loginLimiter) succeed(peer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.last, peer)
}

// evictOldestLocked drops the entry with the oldest timestamp. The caller must
// hold l.mu.
func (l *loginLimiter) evictOldestLocked() {
	oldest := ""
	var at time.Time
	for peer, t := range l.last {
		if oldest == "" || t.Before(at) {
			oldest, at = peer, t
		}
	}
	delete(l.last, oldest)
}

// handleLoginForm renders the token form. The Pi flow does not use a one-time
// code: the token in the form already authenticates the caller, and issuing a
// code on every unauthenticated GET was the main way the pre-auth state could be
// flooded. The code type is retained for the Windows client's login flow.
func (s *Server) handleLoginForm(w http.ResponseWriter, _ *http.Request) {
	s.renderLogin(w, http.StatusOK, "")
}

// handleLoginSubmit verifies the token and issues a session cookie. The session
// lives in memory, so restarting the agent logs every browser out.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	// Cap the body before parsing: ParseForm otherwise buffers up to 10 MB from
	// an unauthenticated caller.
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, "Malformed form submission.")
		return
	}
	peer := peerKey(r)
	if !s.login.allow(peer) {
		s.renderLogin(w, http.StatusTooManyRequests, "Too many attempts. Wait a second and try again.")
		return
	}
	if !s.tokenEqual(r.PostFormValue("token")) {
		// A wrong token is the failure the limiter records; do not clear it.
		s.renderLogin(w, http.StatusUnauthorized, "Invalid token.")
		return
	}
	s.login.succeed(peer)

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
		if !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "forbidden", "cross-origin request refused")
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
	id, ok := s.sessionID(r)
	if !ok {
		http.Redirect(w, r, "/ui/login", http.StatusSeeOther)
		return
	}
	// Refresh the cookie's Max-Age on each shell load so it tracks the idle
	// session instead of a fixed window from login.
	webui.SetCookie(w, id, webui.SessionTTL)
	// yabd is the agent; the same shell is reused by the Windows client with
	// Mode "client".
	_ = webui.RenderIndex(w, webui.Page{Mode: "agent", APIBase: "/v1"})
}

// renderLogin writes the login form.
func (s *Server) renderLogin(w http.ResponseWriter, status int, message string) {
	_ = webui.RenderLogin(w, webui.LoginPage{Error: message}, status)
}
