// Package webui holds the shared, embedded web UI assets and the small pieces
// of server-side state they rely on: in-memory browser sessions, one-time
// login codes and an in-memory ring of recent log records.
//
// It depends only on the Go standard library (plus internal/proto, which is
// itself stdlib-only) and deliberately does not import internal/client or
// internal/agent, so it can be embedded by either binary.
package webui

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// SessionCookieName is the name of the HttpOnly session cookie. The browser
// never sees the bearer token; it holds only this opaque value.
const SessionCookieName = "yab_session"

// CSRFHeader is the custom header every state-changing request must carry when
// it authenticates with a session cookie. It is a simple double-submit guard:
// a cross-site form or image cannot set a custom header, and SameSite=Strict
// already blocks the cookie on cross-site requests.
const CSRFHeader = "X-YAB-CSRF"

// CSRFHeaderValue is the required value of CSRFHeader.
const CSRFHeaderValue = "1"

// SessionTTL is the inactivity timeout of a browser session. Validating a
// session refreshes it; an idle session is dropped.
const SessionTTL = 12 * time.Hour

// maxSessions caps the in-memory session table. When it is exceeded the oldest
// session is evicted.
const maxSessions = 32

// OneTimeCodeTTL is how long a login-form code remains redeemable.
const OneTimeCodeTTL = 60 * time.Second

// tokenBytes is the size of a session id and of a one-time code, in bytes.
const tokenBytes = 32

// Sessions is an in-memory, concurrency-safe session table. It is not
// persisted: restarting the process logs every browser out.
type Sessions struct {
	ttl time.Duration
	max int

	now func() time.Time

	mu sync.Mutex
	m  map[string]sessionEntry
}

type sessionEntry struct {
	created  time.Time
	lastSeen time.Time
}

// NewSessions returns a session table with the default TTL and cap.
func NewSessions() *Sessions {
	return &Sessions{
		ttl: SessionTTL,
		max: maxSessions,
		now: time.Now,
		m:   make(map[string]sessionEntry),
	}
}

// Create allocates a new session and returns its opaque id.
func (s *Sessions) Create() string {
	id := randomToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.evictLocked(now)
	s.m[id] = sessionEntry{created: now, lastSeen: now}
	return id
}

// Validate reports whether id names a live session, refreshing its idle clock
// when it does.
func (s *Sessions) Validate(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	ent, ok := s.m[id]
	if !ok {
		return false
	}
	if now.Sub(ent.lastSeen) >= s.ttl {
		delete(s.m, id)
		return false
	}
	ent.lastSeen = now
	s.m[id] = ent
	return true
}

// Delete removes a session, logging the caller out.
func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}

// Len reports the number of live sessions (for tests and diagnostics).
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// evictLocked drops expired sessions and, when the table is still over its
// cap, the oldest remaining session. The caller must hold s.mu.
func (s *Sessions) evictLocked(now time.Time) {
	for id, ent := range s.m {
		if now.Sub(ent.lastSeen) >= s.ttl {
			delete(s.m, id)
		}
	}
	for len(s.m) >= s.max {
		oldestID := ""
		var oldest time.Time
		for id, ent := range s.m {
			if oldestID == "" || ent.created.Before(oldest) {
				oldestID, oldest = id, ent.created
			}
		}
		delete(s.m, oldestID)
	}
}

// OneTimeCodes issues short-lived, single-use codes. They bind the
// unauthenticated login POST to a server-generated nonce, so a cross-site
// request cannot replay a stolen form.
type OneTimeCodes struct {
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	m  map[string]time.Time
}

// NewOneTimeCodes returns a code store with the default TTL.
func NewOneTimeCodes() *OneTimeCodes {
	return &OneTimeCodes{
		ttl: OneTimeCodeTTL,
		now: time.Now,
		m:   make(map[string]time.Time),
	}
}

// Issue returns a fresh code valid until its TTL elapses.
func (c *OneTimeCodes) Issue() string {
	code := randomToken()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(c.now())
	c.m[code] = c.now().Add(c.ttl)
	return code
}

// Redeem consumes code. It returns true only the first time a code is
// presented and only before it expires.
func (c *OneTimeCodes) Redeem(code string) bool {
	if code == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.pruneLocked(now)
	expiry, ok := c.m[code]
	if !ok {
		return false
	}
	delete(c.m, code)
	return now.Before(expiry)
}

// Len reports the number of live codes (for tests and diagnostics).
func (c *OneTimeCodes) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

func (c *OneTimeCodes) pruneLocked(now time.Time) {
	for code, expiry := range c.m {
		if !now.Before(expiry) {
			delete(c.m, code)
		}
	}
}

// SetCookie writes the session cookie. It is HttpOnly so JavaScript cannot
// read it, and SameSite=Strict so it is not attached to cross-site requests.
// Secure is false because the API has no TLS; see ADR 0014 and docs/security.md.
func SetCookie(w http.ResponseWriter, id string, maxAge time.Duration) {
	c := &http.Cookie{
		Name:     SessionCookieName,
		Value:    id,
		Path:     "/",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false,
	}
	http.SetCookie(w, c)
}

// ClearCookie expires the session cookie in the browser.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false,
	})
}

// randomToken returns tokenBytes of cryptographically random data, hex-encoded.
func randomToken() string {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read never fails on a supported platform; a failure
		// means the process cannot be trusted to issue credentials.
		panic("webui: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
