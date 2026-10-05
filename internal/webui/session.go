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

// maxSessions caps the in-memory session table. When it is exceeded the least
// recently used session is evicted.
const maxSessions = 32

// OneTimeCodeTTL is how long a login-form code remains redeemable.
const OneTimeCodeTTL = 60 * time.Second

// maxOneTimeCodes caps the in-memory one-time-code table. Codes are issued
// before authentication, so the table must be bounded or an unauthenticated
// caller could grow it without limit. When the cap is reached the oldest code
// is evicted.
const maxOneTimeCodes = 64

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
	lastSeen time.Time
	// done is closed exactly once, when the session is deleted, evicted or
	// observed to have expired. Long-lived handlers watch it.
	done chan struct{}
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
	s.m[id] = sessionEntry{lastSeen: now, done: make(chan struct{})}
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
		s.removeLocked(id)
		return false
	}
	ent.lastSeen = now
	s.m[id] = ent
	return true
}

// Alive reports whether id names a live, unexpired session WITHOUT refreshing
// its idle clock. An idle-expired session is removed, which also closes the
// channel returned by Watch. Long-lived streams use it to notice expiry while
// they are open, since serving the stream is not itself session activity.
func (s *Sessions) Alive(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ent, ok := s.m[id]
	if !ok {
		return false
	}
	if s.now().Sub(ent.lastSeen) >= s.ttl {
		s.removeLocked(id)
		return false
	}
	return true
}

// Watch returns a channel that is closed when the session ends, whether by
// logout (Delete), eviction, or an expiry observed by Validate or Alive. The
// bool is false when id is not a live session.
func (s *Sessions) Watch(id string) (<-chan struct{}, bool) {
	if id == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ent, ok := s.m[id]
	if !ok {
		return nil, false
	}
	return ent.done, true
}

// Delete removes a session, logging the caller out.
func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(id)
}

// removeLocked deletes id and closes its watcher channel exactly once. The
// caller must hold s.mu.
func (s *Sessions) removeLocked(id string) {
	ent, ok := s.m[id]
	if !ok {
		return
	}
	delete(s.m, id)
	close(ent.done)
}

// Len reports the number of live sessions (for tests and diagnostics).
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// evictLocked drops expired sessions and, when the table is still over its
// cap, the least recently used session. Eviction is by lastSeen, not creation
// time, so the session a browser is actively using is not dropped first. The
// caller must hold s.mu.
func (s *Sessions) evictLocked(now time.Time) {
	for id, ent := range s.m {
		if now.Sub(ent.lastSeen) >= s.ttl {
			s.removeLocked(id)
		}
	}
	for len(s.m) >= s.max {
		oldestID := ""
		var oldest time.Time
		for id, ent := range s.m {
			if oldestID == "" || ent.lastSeen.Before(oldest) {
				oldestID, oldest = id, ent.lastSeen
			}
		}
		s.removeLocked(oldestID)
	}
}

// OneTimeCodes issues short-lived, single-use codes. They bind the
// unauthenticated login POST to a server-generated nonce, so a cross-site
// request cannot replay a stolen form.
//
// The table is bounded and issue/redeem are O(1) amortised: codes are tracked
// in issue order and expired or redeemed entries are dropped from the front of
// that order as needed, so a flood of GETs cannot grow the table or make each
// issue scan the whole map.
type OneTimeCodes struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu    sync.Mutex
	m     map[string]time.Time
	order []string
}

// NewOneTimeCodes returns a code store with the default TTL and cap.
func NewOneTimeCodes() *OneTimeCodes {
	return &OneTimeCodes{
		ttl: OneTimeCodeTTL,
		max: maxOneTimeCodes,
		now: time.Now,
		m:   make(map[string]time.Time),
	}
}

// Issue returns a fresh code valid until its TTL elapses. When the table is
// full the oldest code is evicted, so the store never exceeds its cap.
func (c *OneTimeCodes) Issue() string {
	code := randomToken()
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.dropStaleFrontLocked(now)
	for len(c.m) >= c.max {
		c.evictOldestLocked()
	}
	// Redeemed codes leave holes in the queue; compact when enough have
	// accumulated so the queue stays bounded even under constant churn.
	if len(c.order) >= 2*c.max {
		c.compactLocked()
	}
	c.m[code] = now.Add(c.ttl)
	c.order = append(c.order, code)
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

// dropStaleFrontLocked drops the front of the issue-order queue while it names
// a code that is absent (already redeemed) or expired. Because every code is
// issued with the same TTL in issue order, the front is always the first to
// expire, so this keeps live entries and needs no full scan. The caller must
// hold c.mu.
func (c *OneTimeCodes) dropStaleFrontLocked(now time.Time) {
	for len(c.order) > 0 {
		code := c.order[0]
		expiry, live := c.m[code]
		if live && now.Before(expiry) {
			return
		}
		c.order = c.order[1:]
		delete(c.m, code)
	}
}

// evictOldestLocked drops the oldest entry in the queue. A popped entry may
// name an already-redeemed code, in which case the map is unchanged; the caller
// loops until the map is below its cap. The caller must hold c.mu.
func (c *OneTimeCodes) evictOldestLocked() {
	if len(c.order) == 0 {
		return
	}
	code := c.order[0]
	c.order = c.order[1:]
	delete(c.m, code)
}

// compactLocked rebuilds the issue-order queue with only live codes. The caller
// must hold c.mu.
func (c *OneTimeCodes) compactLocked() {
	kept := c.order[:0]
	for _, code := range c.order {
		if _, ok := c.m[code]; ok {
			kept = append(kept, code)
		}
	}
	c.order = kept
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
