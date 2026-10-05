package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/webui"
)

func newWebUIServer(t *testing.T, webUI bool) *Server {
	t.Helper()
	backend := &fakeBackend{devices: []proto.Device{
		{Pin: "bt", BusID: "1-1.2", VID: "0a12", PID: "0001", Present: true},
	}}
	srv, err := New(Config{
		Token:          testToken,
		AllowedClients: []string{"10.0.0.0/8"},
		Backend:        backend,
		WebUI:          webUI,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func doUI(t *testing.T, srv *Server, method, target string, vals url.Values, cookie string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return doUIFrom(t, srv, method, target, vals, "10.0.0.5:1234", cookie, headers)
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == webui.SessionCookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no session cookie set; headers = %v", rec.Result().Header)
	return ""
}

func TestWebUIDisabledReturns404(t *testing.T) {
	srv := newWebUIServer(t, false)
	for _, target := range []string{"/ui/", "/ui/login", "/ui/assets/app.css", "/ui/assets/app.js"} {
		rec := doUI(t, srv, http.MethodGet, target, nil, "", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with web_ui=false = %d, want 404", target, rec.Code)
		}
	}
}

func TestSessionRejectedWhenWebUIDisabled(t *testing.T) {
	srv := newWebUIServer(t, false)
	session := srv.sessions.Create()

	rec := authedRequest(t, srv, http.MethodGet, "/v1/devices", "10.0.0.5:1234", "", session, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session with web_ui=false = %d, want 401", rec.Code)
	}
	// The bearer path is untouched.
	rec = request(t, srv, http.MethodGet, "/v1/devices", "10.0.0.5:1234", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("bearer with web_ui=false = %d, want 200", rec.Code)
	}
}

func TestLoginIssuesSessionAndIndexRenders(t *testing.T) {
	srv := newWebUIServer(t, true)

	form := doUI(t, srv, http.MethodGet, "/ui/login", nil, "", nil)
	if form.Code != http.StatusOK {
		t.Fatalf("GET /ui/login = %d, want 200", form.Code)
	}

	login := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"token": {testToken}}, "", nil)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("POST /ui/login = %d, want 303 (body %s)", login.Code, login.Body.String())
	}
	session := sessionCookie(t, login)

	// The cookie authenticates /v1 reads on its own.
	check := authedRequest(t, srv, http.MethodGet, "/v1/devices", "10.0.0.5:1234", "", session, nil)
	if check.Code != http.StatusOK {
		t.Fatalf("GET /v1/devices with a session cookie = %d, want 200", check.Code)
	}

	index := doUI(t, srv, http.MethodGet, "/ui/", nil, session, nil)
	if index.Code != http.StatusOK || !strings.Contains(index.Body.String(), "you-as-bee") {
		t.Fatalf("GET /ui/ with a session = %d, body %q", index.Code, index.Body.String())
	}
}

// TestLoginFormHasNoOneTimeCode: the Pi login flow no longer issues a one-time
// code (it was the pre-auth flooding vector). The shared form only carries the
// code field when the renderer supplies one, which the Windows flow will.
func TestLoginFormHasNoOneTimeCode(t *testing.T) {
	srv := newWebUIServer(t, true)
	form := doUI(t, srv, http.MethodGet, "/ui/login", nil, "", nil)
	if form.Code != http.StatusOK {
		t.Fatalf("GET /ui/login = %d, want 200", form.Code)
	}
	if strings.Contains(form.Body.String(), `name="code"`) {
		t.Errorf("Pi login form still carries a one-time code field:\n%s", form.Body.String())
	}
}

func TestUIIndexRedirectsWithoutSession(t *testing.T) {
	srv := newWebUIServer(t, true)
	rec := doUI(t, srv, http.MethodGet, "/ui/", nil, "", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /ui/ without a session = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/login" {
		t.Errorf("Location = %q, want /ui/login", loc)
	}
}

func TestLoginRejectsWrongToken(t *testing.T) {
	srv := newWebUIServer(t, true)
	rec := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"token": {"wrong-token"}}, "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestLoginIgnoresUnknownCodeField: a stray code field (as an older cached page
// might submit) does not authenticate and does not break a valid token.
func TestLoginIgnoresUnknownCodeField(t *testing.T) {
	srv := newWebUIServer(t, true)
	rec := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {"deadbeef"}, "token": {testToken}}, "", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("valid token with an unrelated code field = %d, want 303", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv := newWebUIServer(t, true)
	now := time.Unix(1000, 0)
	srv.login.now = func() time.Time { return now }

	first := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"token": {"wrong-token"}}, "", nil)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first failed attempt = %d, want 401", first.Code)
	}

	immediate := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"token": {testToken}}, "", nil)
	if immediate.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate second attempt = %d, want 429", immediate.Code)
	}

	now = now.Add(time.Second)
	after := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"token": {testToken}}, "", nil)
	if after.Code != http.StatusSeeOther {
		t.Fatalf("attempt after the limiter window = %d, want 303", after.Code)
	}
}

// TestLoginLimiterIsPerPeer: one peer failing cannot throttle another peer. A
// global limiter would have let any client lock the operator out.
func TestLoginLimiterIsPerPeer(t *testing.T) {
	srv := newWebUIServer(t, true)
	now := time.Unix(1000, 0)
	srv.login.now = func() time.Time { return now }

	if !srv.login.allow("10.0.0.5") {
		t.Fatal("first attempt from peer A was refused")
	}
	// Peer A is now throttled...
	if srv.login.allow("10.0.0.5") {
		t.Fatal("peer A was allowed twice inside the window")
	}
	// ...but peer B is untouched.
	if !srv.login.allow("10.0.0.6") {
		t.Fatal("peer B was throttled by peer A's failure")
	}
}

// TestLoginLimiterAtomicBurst: allow() records the attempt in the same critical
// section as the check, so a concurrent burst cannot all pass before the first
// failure registers.
func TestLoginLimiterAtomicBurst(t *testing.T) {
	l := newLoginLimiter()
	l.now = func() time.Time { return time.Unix(1000, 0) }

	const n = 64
	var wg sync.WaitGroup
	var allowed int32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if l.allow("10.0.0.5") {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("allowed = %d concurrent attempts, want exactly 1", allowed)
	}
}

func TestLogoutDeletesSessionAndNeedsCSRF(t *testing.T) {
	srv := newWebUIServer(t, true)
	session := srv.sessions.Create()

	noCSRF := doUI(t, srv, http.MethodPost, "/ui/logout", nil, session, nil)
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF = %d, want 403", noCSRF.Code)
	}
	if !srv.sessions.Validate(session) {
		t.Fatal("a rejected logout must not delete the session")
	}

	ok := doUI(t, srv, http.MethodPost, "/ui/logout", nil, session,
		map[string]string{webui.CSRFHeader: webui.CSRFHeaderValue})
	if ok.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d, want 303", ok.Code)
	}
	if srv.sessions.Validate(session) {
		t.Fatal("logout did not delete the session")
	}
}

func TestSessionWriteNeedsCSRFHeader(t *testing.T) {
	srv := newWebUIServer(t, true)
	session := srv.sessions.Create()

	rec := authedRequest(t, srv, http.MethodPost, "/v1/devices/bt/export", "10.0.0.5:1234", "", session, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("write without CSRF = %d, want 403", rec.Code)
	}
	rec = authedRequest(t, srv, http.MethodPost, "/v1/devices/bt/export", "10.0.0.5:1234", "", session,
		map[string]string{webui.CSRFHeader: webui.CSRFHeaderValue})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("write with CSRF = %d, want 202", rec.Code)
	}
}

func TestLogsEndpointPages(t *testing.T) {
	ring := webui.NewLogRing()
	logger := slog.New(ring)
	logger.Info("one")
	logger.Info("two")
	logger.Info("three")

	srv, err := New(Config{
		Token:          testToken,
		AllowedClients: []string{"10.0.0.0/8"},
		Backend:        &fakeBackend{},
		Logs:           ring,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := request(t, srv, http.MethodGet, "/v1/logs?after=0&limit=2", "10.0.0.5:1234", testToken, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("GET /v1/logs = %d, want 200", first.Code)
	}
	var page1 proto.LogsResponse
	if err := json.Unmarshal(first.Body.Bytes(), &page1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page1.Entries) != 2 || page1.Entries[0].Seq != 1 || page1.Next != 2 {
		t.Fatalf("page1 = %+v", page1)
	}

	second := request(t, srv, http.MethodGet, "/v1/logs?after=2&limit=2", "10.0.0.5:1234", testToken, nil)
	var page2 proto.LogsResponse
	if err := json.Unmarshal(second.Body.Bytes(), &page2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page2.Entries) != 1 || page2.Entries[0].Seq != 3 || page2.Next != 3 {
		t.Fatalf("page2 = %+v", page2)
	}
}

// TestUIRoutesRejectNonAllowlisted is the BLOCKER 1 regression: every /ui/ path,
// including login, logout and the assets, must run the CIDR allowlist first.
// Before the fix these were registered directly on the mux, so a disallowed
// peer got 200 and a live login surface (and issued codes).
func TestUIRoutesRejectNonAllowlisted(t *testing.T) {
	srv := newWebUIServer(t, true)
	const outside = "203.0.113.9:1234"
	cases := []struct {
		method string
		target string
		vals   url.Values
	}{
		{http.MethodGet, "/ui/login", nil},
		{http.MethodPost, "/ui/login", url.Values{"token": {testToken}}},
		{http.MethodPost, "/ui/logout", nil},
		{http.MethodGet, "/ui/", nil},
		{http.MethodGet, "/ui/assets/app.css", nil},
		{http.MethodGet, "/ui/assets/app.js", nil},
		{http.MethodGet, "/ui/anything", nil},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := doUIFrom(t, srv, tc.method, tc.target, tc.vals, outside, "", nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s from a non-allowlisted peer = %d, want 403", tc.method, tc.target, rec.Code)
			}
		})
	}
}

// TestUIRouteRejectsNonAllowlistedEvenWithSession: a valid session cookie does
// not excuse a non-allowlisted peer; the allowlist is checked first.
func TestUIRouteRejectsNonAllowlistedEvenWithSession(t *testing.T) {
	srv := newWebUIServer(t, true)
	session := srv.sessions.Create()

	rec := doUIFrom(t, srv, http.MethodGet, "/ui/", nil, "203.0.113.9:1234", session, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /ui/ with a session from a disallowed peer = %d, want 403", rec.Code)
	}
	// The same cookie from an allowlisted peer is accepted.
	rec = doUI(t, srv, http.MethodGet, "/ui/", nil, session, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/ with a session from an allowlisted peer = %d, want 200", rec.Code)
	}
}

// TestUISecurityHeaders checks the browser hardening applied to UI responses,
// including the allowlist 403 for a disallowed peer: the header wrapper is
// outermost, so an error response carries them too.
func TestUISecurityHeaders(t *testing.T) {
	srv := newWebUIServer(t, true)
	const outside = "203.0.113.9:1234"
	cases := []struct {
		name       string
		target     string
		remoteAddr string
		wantStatus int
	}{
		{"login", "/ui/login", "10.0.0.5:1234", http.StatusOK},
		{"asset", "/ui/assets/app.js", "10.0.0.5:1234", http.StatusOK},
		{"allowlist 403", "/ui/login", outside, http.StatusForbidden},
		{"unknown ui path from disallowed peer", "/ui/does-not-exist", outside, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doUIFrom(t, srv, http.MethodGet, tc.target, nil, tc.remoteAddr, "", nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s from %s = %d, want %d", tc.target, tc.remoteAddr, rec.Code, tc.wantStatus)
			}
			h := rec.Result().Header
			if got := h.Get("Content-Security-Policy"); got == "" || strings.Contains(got, "unsafe-inline") {
				t.Errorf("%s Content-Security-Policy = %q", tc.target, got)
			}
			if h.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s X-Content-Type-Options = %q, want nosniff", tc.target, h.Get("X-Content-Type-Options"))
			}
			if h.Get("X-Frame-Options") != "DENY" {
				t.Errorf("%s X-Frame-Options = %q, want DENY", tc.target, h.Get("X-Frame-Options"))
			}
			if h.Get("Referrer-Policy") != "no-referrer" {
				t.Errorf("%s Referrer-Policy = %q, want no-referrer", tc.target, h.Get("Referrer-Policy"))
			}
		})
	}
}

// TestLoginBodyIsCapped: an unauthenticated POST cannot make the server buffer
// a large body.
func TestLoginBodyIsCapped(t *testing.T) {
	srv := newWebUIServer(t, true)
	rec := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"token": {strings.Repeat("a", maxLoginBody*2)}}, "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized login body = %d, want 400", rec.Code)
	}
}

// TestSessionCookieSlidesWithIdleTTL: a session-authenticated request refreshes
// the cookie Max-Age to the idle TTL rather than leaving a fixed window from
// login.
func TestSessionCookieSlidesWithIdleTTL(t *testing.T) {
	srv := newWebUIServer(t, true)
	session := srv.sessions.Create()

	rec := authedRequest(t, srv, http.MethodGet, "/v1/devices", "10.0.0.5:1234", "", session, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("session GET = %d, want 200", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == webui.SessionCookieName {
			if want := int(webui.SessionTTL.Seconds()); c.MaxAge != want {
				t.Fatalf("refreshed cookie Max-Age = %d, want %d", c.MaxAge, want)
			}
			return
		}
	}
	t.Fatal("no session cookie was refreshed on the request")
}

// TestSessionWriteRejectsForeignOrigin: the Origin header is defence in depth
// for destructive session writes.
func TestSessionWriteRejectsForeignOrigin(t *testing.T) {
	srv := newWebUIServer(t, true)
	session := srv.sessions.Create()
	headers := map[string]string{
		webui.CSRFHeader: webui.CSRFHeaderValue,
		"Origin":         "http://evil.example",
	}
	rec := authedRequest(t, srv, http.MethodPost, "/v1/devices/bt/reset", "10.0.0.5:1234", "", session, headers)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign Origin write = %d, want 403", rec.Code)
	}
	// A same-origin write is accepted.
	headers["Origin"] = "http://example.com"
	rec = authedRequest(t, srv, http.MethodPost, "/v1/devices/bt/reset", "10.0.0.5:1234", "", session, headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin write = %d, want 200", rec.Code)
	}
}

// TestAssetsServedAndUnknownUIFallsThrough documents finding 10: /ui/<anything>
// serves the shell (an intentional SPA fallthrough) while the more specific
// /ui/assets/ pattern still serves real files, not HTML.
func TestAssetsServedAndUnknownUIFallsThrough(t *testing.T) {
	srv := newWebUIServer(t, true)
	session := srv.sessions.Create()

	asset := doUI(t, srv, http.MethodGet, "/ui/assets/app.js", nil, "", nil)
	if asset.Code != http.StatusOK {
		t.Fatalf("GET /ui/assets/app.js = %d, want 200", asset.Code)
	}
	if !strings.Contains(asset.Body.String(), "use strict") {
		t.Fatalf("app.js was shadowed by the shell:\n%s", asset.Body.String())
	}

	shell := doUI(t, srv, http.MethodGet, "/ui/does-not-exist", nil, session, nil)
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "<!doctype html>") {
		t.Fatalf("GET /ui/does-not-exist = %d, want the shell (200)", shell.Code)
	}
}

// TestLogsEndpointRedactsPaths: the /v1/logs mirror must not leak sysfs paths
// even though the console copy keeps them.
func TestLogsEndpointRedactsPaths(t *testing.T) {
	ring := webui.NewLogRing()
	slog.New(ring).Warn("bind 1-1.2 failed: usbiphost: write /sys/bus/usb/drivers/usbip-host/bind: permission denied",
		"path", "/sys/bus/usb/devices/1-1.2")

	srv, err := New(Config{
		Token:          testToken,
		AllowedClients: []string{"10.0.0.0/8"},
		Backend:        &fakeBackend{},
		Logs:           ring,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := request(t, srv, http.MethodGet, "/v1/logs", "10.0.0.5:1234", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/logs = %d, want 200", rec.Code)
	}
	var resp proto.LogsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(resp.Entries))
	}
	if strings.Contains(resp.Entries[0].Msg, "/sys/") {
		t.Errorf("log message leaked a sysfs path: %q", resp.Entries[0].Msg)
	}
	if strings.Contains(resp.Entries[0].Attrs["path"], "/sys/") {
		t.Errorf("log attrs leaked a sysfs path: %q", resp.Entries[0].Attrs["path"])
	}
}

// TestLogsEndpointConcurrentReadsDoNotMutateRingAttrs drives many concurrent
// /v1/logs requests over entries that carry Attrs maps. Entries returns the
// ring's own Attrs maps by reference, so an implementation that redacts in
// place is a concurrent map write; Go aborts the whole process on one, and
// net/http cannot recover it. This asserts correctness and, under CI's -race
// (the race detector needs cgo, unavailable locally with CGO_ENABLED=0), would
// also flag the in-place mutation. The final check proves redaction never
// writes through to ring storage.
func TestLogsEndpointConcurrentReadsDoNotMutateRingAttrs(t *testing.T) {
	ring := webui.NewLogRing()
	logger := slog.New(ring)
	for i := 0; i < 50; i++ {
		logger.Info("bind failed", "path", fmt.Sprintf("/sys/bus/usb/devices/1-1.%d", i))
	}

	srv, err := New(Config{
		Token:          testToken,
		AllowedClients: []string{"10.0.0.0/8"},
		Backend:        &fakeBackend{},
		Logs:           ring,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const goroutines = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 25; i++ {
				rec := request(t, srv, http.MethodGet, "/v1/logs?limit=500", "10.0.0.5:1234", testToken, nil)
				if rec.Code != http.StatusOK {
					t.Errorf("GET /v1/logs = %d, want 200", rec.Code)
					return
				}
				var resp proto.LogsResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Errorf("decode: %v", err)
					return
				}
				for _, e := range resp.Entries {
					if strings.Contains(e.Msg, "/sys/") {
						t.Errorf("log message leaked a sysfs path: %q", e.Msg)
						return
					}
					for k, v := range e.Attrs {
						if strings.Contains(v, "/sys/") {
							t.Errorf("attr %q leaked a sysfs path: %q", k, v)
							return
						}
					}
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	// The ring's stored map must still hold the original path: redaction is
	// per response, never in storage.
	stored := ring.Entries(0, 1)
	if len(stored.Entries) != 1 || !strings.Contains(stored.Entries[0].Attrs["path"], "/sys/") {
		t.Fatalf("ring storage was mutated by a reader: %+v", stored.Entries)
	}
}

// streamBackend signals when a stream handler has subscribed, so a test can
// delete the session knowing the handler is already watching it.
type streamBackend struct {
	fakeBackend
	subscribed chan struct{}
}

func (b *streamBackend) Subscribe(context.Context) (<-chan proto.Event, func()) {
	close(b.subscribed)
	return make(chan proto.Event), func() {}
}

// TestEventStreamClosesWhenSessionDeleted: a logged-out session must stop
// receiving /v1/events immediately, not at client disconnect.
func TestEventStreamClosesWhenSessionDeleted(t *testing.T) {
	backend := &streamBackend{
		fakeBackend: fakeBackend{devices: []proto.Device{{Pin: "bt", BusID: "1-1.2", Present: true}}},
		subscribed:  make(chan struct{}),
	}
	srv, err := New(Config{
		Token:          testToken,
		AllowedClients: []string{"10.0.0.0/8"},
		Backend:        backend,
		WebUI:          true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session := srv.sessions.Create()

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.AddCookie(&http.Cookie{Name: webui.SessionCookieName, Value: session})
	rec := httptest.NewRecorder()

	finished := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(rec, req)
		close(finished)
	}()

	select {
	case <-backend.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("event stream did not subscribe")
	}
	srv.sessions.Delete(session)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("event stream stayed open after the session was deleted")
	}
}

// doUIFrom is doUI with an explicit peer address.
func doUIFrom(t *testing.T, srv *Server, method, target string, vals url.Values, remoteAddr, cookie string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if vals != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if remoteAddr == "" {
		remoteAddr = "10.0.0.5:1234"
	}
	req.RemoteAddr = remoteAddr
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: webui.SessionCookieName, Value: cookie})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}
