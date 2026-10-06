package webui

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/proto"
)

// fakeBackend is an in-memory webui.Backend for the local-server tests.
type fakeBackend struct {
	mu         sync.Mutex
	pins       []PinStatus
	servers    []ServerStatus
	attachErr  error
	detachErr  error
	attached   []string
	detached   []string
	serverCall int
}

func (b *fakeBackend) Status() []PinStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]PinStatus(nil), b.pins...)
}

func (b *fakeBackend) Servers() []ServerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serverCall++
	return append([]ServerStatus(nil), b.servers...)
}

func (b *fakeBackend) Attach(pin string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attached = append(b.attached, pin)
	return b.attachErr
}

func (b *fakeBackend) Detach(pin string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.detached = append(b.detached, pin)
	return b.detachErr
}

func (b *fakeBackend) setPins(pins ...PinStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pins = pins
}

func (b *fakeBackend) attachedPins() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.attached...)
}

func (b *fakeBackend) detachedPins() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.detached...)
}

func (b *fakeBackend) serverCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.serverCall
}

// fakeClock is a mutex-guarded clock for tests whose background server probe
// reads s.now concurrently.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// waitForServerView waits until the background probe has published a view.
func waitForServerView(t *testing.T, s *LocalServer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.serversMu.Lock()
		published := !s.serversAt.IsZero()
		s.serversMu.Unlock()
		if published {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the background server probe never published a view")
}

// waitForServerCalls waits until Servers() has been called at least n times.
func waitForServerCalls(t *testing.T, b *fakeBackend, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.serverCalls() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("Servers() called %d times, want at least %d", b.serverCalls(), n)
}

// startLocal binds the server to a real loopback port and serves it. The
// returned httptest.Server owns the listener so ts.URL and the Host header the
// server requires agree exactly.
func startLocal(t *testing.T, b Backend, logs *LogRing) (*LocalServer, *httptest.Server) {
	t.Helper()
	return startLocalCfg(t, LocalConfig{Backend: b, Logs: logs, Listen: "127.0.0.1:0"})
}

// startLocalCfg is startLocal with a caller-supplied LocalConfig, for tests
// that need to set UnknownErr or another field.
func startLocalCfg(t *testing.T, cfg LocalConfig) (*LocalServer, *httptest.Server) {
	t.Helper()
	s, err := NewLocal(cfg)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	s.sseTick = 10 * time.Millisecond
	ln, err := s.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ts := httptest.NewUnstartedServer(s.Handler())
	_ = ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)
	return s, ts
}

func newTestClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func doGET(t *testing.T, c *http.Client, target string) *http.Response {
	t.Helper()
	resp, err := c.Get(target)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	return resp
}

func doPOST(t *testing.T, c *http.Client, target string, form url.Values, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", target, err)
	}
	return resp
}

// login performs the full GET-bind, POST-redeem flow for a fresh code.
func login(t *testing.T, s *LocalServer, ts *httptest.Server, c *http.Client) {
	t.Helper()
	code := s.codes.Issue()
	resp := doGET(t, c, ts.URL+"/ui/login?code="+url.QueryEscape(code))
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GET /ui/login = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	resp.Body.Close()

	resp = doPOST(t, c, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("POST /ui/login = %d, want 303 (body %s)", resp.StatusCode, body)
	}
	resp.Body.Close()
}

func TestLocalRejectsForeignHost(t *testing.T) {
	b := &fakeBackend{}
	_, ts := startLocal(t, b, nil)

	for _, target := range []string{"/ui/login", "/ui/", "/ui/api/state"} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+target, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Host = "evil.example"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s with a foreign Host: %v", target, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s with Host evil.example = %d, want 403", target, resp.StatusCode)
		}
	}
}

func TestLocalRejectsNonLoopbackListen(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:0", "192.168.1.5:8080", "localhost:0", ":0", "not-an-address"} {
		if _, err := NewLocal(LocalConfig{Backend: &fakeBackend{}, Listen: listen}); err == nil {
			t.Errorf("NewLocal(%q) succeeded, want a loopback validation error", listen)
		}
	}
	if _, err := NewLocal(LocalConfig{Backend: &fakeBackend{}, Listen: "127.0.0.1:0"}); err != nil {
		t.Errorf("NewLocal(127.0.0.1:0) = %v, want nil", err)
	}
}

func TestLocalRejectsPreflight(t *testing.T) {
	b := &fakeBackend{}
	_, ts := startLocal(t, b, nil)
	req, err := http.NewRequest(http.MethodOptions, ts.URL+"/ui/api/state", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("OPTIONS = %d, want 403", resp.StatusCode)
	}
}

// TestLocalLoginBindsCodeToBrowser: a code can only be redeemed by the browser
// that loaded the issuing GET (the one holding the binding cookie).
func TestLocalLoginBindsCodeToBrowser(t *testing.T) {
	b := &fakeBackend{}
	s, ts := startLocal(t, b, nil)

	code := s.codes.Issue()

	owner := newTestClient()
	resp := doGET(t, owner, ts.URL+"/ui/login?code="+url.QueryEscape(code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issuing GET = %d, want 200", resp.StatusCode)
	}

	// A different client has no binding cookie and must not be able to redeem.
	thief := newTestClient()
	denied := doPOST(t, thief, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unbound redemption = %d, want 401", denied.StatusCode)
	}

	// The owner can still redeem.
	ok := doPOST(t, owner, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("bound redemption = %d, want 303", ok.StatusCode)
	}

	// Single use: the owner cannot redeem it twice.
	again := doPOST(t, owner, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	again.Body.Close()
	if again.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second redemption = %d, want 401", again.StatusCode)
	}
}

// TestLocalLoginCodeExpiry: a bound code stops working at the TTL.
func TestLocalLoginCodeExpiry(t *testing.T) {
	b := &fakeBackend{}
	s, ts := startLocal(t, b, nil)

	now := time.Unix(1000, 0)
	s.codes.now = func() time.Time { return now }

	code := s.codes.Issue()
	c := newTestClient()
	resp := doGET(t, c, ts.URL+"/ui/login?code="+url.QueryEscape(code))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issuing GET = %d, want 200", resp.StatusCode)
	}

	now = now.Add(OneTimeCodeTTL)
	expired := doPOST(t, c, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	expired.Body.Close()
	if expired.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired redemption = %d, want 401", expired.StatusCode)
	}
}

// TestNewLoginURLHasNoQuery guards against regressing the tray's browser
// launch: explorer.exe treats a URL containing "?" as a filesystem path, opens
// a folder window and launches no browser, so the one-time code must live in a
// path segment.
func TestNewLoginURLHasNoQuery(t *testing.T) {
	s, _ := startLocal(t, &fakeBackend{}, nil)
	loginURL := s.NewLoginURL()
	if strings.Contains(loginURL, "?") {
		t.Fatalf("NewLoginURL contains a query string: %q", loginURL)
	}
	if !strings.HasPrefix(loginURL, s.BaseURL()+"/ui/login/") {
		t.Fatalf("NewLoginURL = %q, want %s/ui/login/<code>", loginURL, s.BaseURL())
	}
}

// TestLocalLoginPathForm: GET /ui/login/<code> behaves exactly like the query
// form. It binds the code to the browser, only that browser may redeem it with
// the same single-use code, and redemption 303-redirects to the shell (so the
// code leaves the address bar).
func TestLocalLoginPathForm(t *testing.T) {
	b := &fakeBackend{}
	s, ts := startLocal(t, b, nil)

	code := s.codes.Issue()
	owner := newTestClient()
	resp := doGET(t, owner, ts.URL+"/ui/login/"+code)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("path issuing GET = %d, want 200", resp.StatusCode)
	}

	// A different client has no binding cookie and must not be able to redeem.
	thief := newTestClient()
	denied := doPOST(t, thief, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unbound path redemption = %d, want 401", denied.StatusCode)
	}

	// The owner can still redeem, and the redirect drops the code.
	ok := doPOST(t, owner, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("bound path redemption = %d, want 303", ok.StatusCode)
	}
	if loc := ok.Header.Get("Location"); loc != "/ui/" {
		t.Fatalf("path redemption Location = %q, want /ui/", loc)
	}

	// Single use.
	again := doPOST(t, owner, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	again.Body.Close()
	if again.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second path redemption = %d, want 401", again.StatusCode)
	}
}

// TestLocalLoginTrailingSlash: a trailing slash is not the path form. It must
// not issue a binding or consume the code, and the code must still be usable
// through the exact form afterwards.
func TestLocalLoginTrailingSlash(t *testing.T) {
	s, ts := startLocal(t, &fakeBackend{}, nil)

	code := s.codes.Issue()
	resp := doGET(t, newTestClient(), ts.URL+"/ui/login/"+code+"/")
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("GET /ui/login/<code>/ = 200, want no binding")
	}

	// The code was not bound or consumed: the exact form still works.
	owner := newTestClient()
	ok := doGET(t, owner, ts.URL+"/ui/login/"+code)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("exact path form after a trailing-slash GET = %d, want 200", ok.StatusCode)
	}
}

// TestLocalLoginEncodedSlash: a %2F inside the code segment must not be decoded
// into a path separator that selects another route, must not match a real code,
// and must not consume it.
func TestLocalLoginEncodedSlash(t *testing.T) {
	s, ts := startLocal(t, &fakeBackend{}, nil)

	code := s.codes.Issue()
	resp := doGET(t, newTestClient(), ts.URL+"/ui/login/"+code+"%2F")
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("GET /ui/login/<code>%%2F = 200, want no binding")
	}

	// The real code remains unused and single-use.
	owner := newTestClient()
	ok := doGET(t, owner, ts.URL+"/ui/login/"+code)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("exact path form after an encoded-slash GET = %d, want 200", ok.StatusCode)
	}
	redeem := doPOST(t, owner, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	redeem.Body.Close()
	if redeem.StatusCode != http.StatusSeeOther {
		t.Fatalf("redemption after an encoded-slash GET = %d, want 303", redeem.StatusCode)
	}
}

// TestLocalLoginMixedFormsSingleUse: a code bound through the path form cannot
// be reused through the query form, or vice versa; single-use spans both.
func TestLocalLoginMixedFormsSingleUse(t *testing.T) {
	s, ts := startLocal(t, &fakeBackend{}, nil)

	code := s.codes.Issue()
	owner := newTestClient()

	// Bind through the path form, redeem through POST, then present the same
	// code through the query form: it must already be spent.
	resp := doGET(t, owner, ts.URL+"/ui/login/"+code)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("path issuing GET = %d, want 200", resp.StatusCode)
	}
	redeem := doPOST(t, owner, ts.URL+"/ui/login", url.Values{"code": {code}}, nil)
	redeem.Body.Close()
	if redeem.StatusCode != http.StatusSeeOther {
		t.Fatalf("path redemption = %d, want 303", redeem.StatusCode)
	}

	again := doGET(t, owner, ts.URL+"/ui/login?code="+url.QueryEscape(code))
	again.Body.Close()
	if again.StatusCode == http.StatusOK {
		t.Fatalf("query-form GET of a spent path code = 200, want no binding")
	}
}

// TestLocalLoginCookieIsHardened: the binding cookie is HttpOnly and
// SameSite=Strict.
func TestLocalLoginCookieIsHardened(t *testing.T) {
	b := &fakeBackend{}
	s, ts := startLocal(t, b, nil)

	code := s.codes.Issue()
	resp := doGET(t, newTestClient(), ts.URL+"/ui/login?code="+url.QueryEscape(code))
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name != LoginCookieName {
			continue
		}
		if !c.HttpOnly {
			t.Error("binding cookie is not HttpOnly")
		}
		if c.SameSite != http.SameSiteStrictMode {
			t.Errorf("binding cookie SameSite = %v, want Strict", c.SameSite)
		}
		return
	}
	t.Fatalf("no %s cookie was set", LoginCookieName)
}

func TestLocalCSRFRequiredOnNonGET(t *testing.T) {
	b := &fakeBackend{}
	s, ts := startLocal(t, b, nil)
	c := newTestClient()
	login(t, s, ts, c)

	noCSRF := doPOST(t, c, ts.URL+"/ui/api/pins/pi/xbox/attach", nil, nil)
	noCSRF.Body.Close()
	if noCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("attach without CSRF = %d, want 403", noCSRF.StatusCode)
	}
	if got := b.attachedPins(); len(got) != 0 {
		t.Fatalf("attach without CSRF reached the backend: %v", got)
	}

	withCSRF := doPOST(t, c, ts.URL+"/ui/api/pins/pi/xbox/attach", nil,
		map[string]string{CSRFHeader: CSRFHeaderValue})
	withCSRF.Body.Close()
	if withCSRF.StatusCode != http.StatusOK {
		t.Fatalf("attach with CSRF = %d, want 200", withCSRF.StatusCode)
	}
}

func TestLocalAttachDetachHitsBackend(t *testing.T) {
	b := &fakeBackend{}
	s, ts := startLocal(t, b, nil)
	c := newTestClient()
	login(t, s, ts, c)

	resp := doPOST(t, c, ts.URL+"/ui/api/pins/pi/xbox/attach", nil,
		map[string]string{CSRFHeader: CSRFHeaderValue})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach = %d, want 200", resp.StatusCode)
	}

	resp = doPOST(t, c, ts.URL+"/ui/api/pins/pi/xbox/detach", nil,
		map[string]string{CSRFHeader: CSRFHeaderValue})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detach = %d, want 200", resp.StatusCode)
	}

	if got := b.attachedPins(); len(got) != 1 || got[0] != "pi/xbox" {
		t.Fatalf("attached = %v, want [pi/xbox]", got)
	}
	if got := b.detachedPins(); len(got) != 1 || got[0] != "pi/xbox" {
		t.Fatalf("detached = %v, want [pi/xbox]", got)
	}
}

func TestLocalLoginBodyIsCapped(t *testing.T) {
	b := &fakeBackend{}
	_, ts := startLocal(t, b, nil)

	big := url.Values{"code": {strings.Repeat("a", maxLocalLoginBody*2)}}
	resp := doPOST(t, newTestClient(), ts.URL+"/ui/login", big, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized login body = %d, want 400", resp.StatusCode)
	}
}

func TestLocalStateServesPinsAndCachesServers(t *testing.T) {
	b := &fakeBackend{
		pins:    []PinStatus{{Pin: "pi/xbox", Server: "pi", State: "attached", Port: 3}},
		servers: []ServerStatus{{Name: "pi", Host: "pi.local", APIPort: 3241, Reachable: true, TokenValid: true}},
	}
	s, ts := startLocal(t, b, nil)
	clock := newFakeClock(time.Unix(1000, 0))
	s.now = clock.now
	c := newTestClient()
	login(t, s, ts, c)

	// The pins are served immediately; the server view starts empty and is
	// filled by the background probe (see the blocking test for the point).
	resp := doGET(t, c, ts.URL+"/ui/api/state")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/api/state = %d, want 200", resp.StatusCode)
	}
	var st LocalState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(st.Pins) != 1 || st.Pins[0].Pin != "pi/xbox" || st.Pins[0].Port != 3 {
		t.Fatalf("pins = %+v", st.Pins)
	}

	// Wait for the background probe to publish, then it is cached for the TTL.
	waitForServerView(t, s)
	resp = doGET(t, c, ts.URL+"/ui/api/state")
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(st.Servers) != 1 || st.Servers[0].Name != "pi" {
		t.Fatalf("servers = %+v", st.Servers)
	}
	if got := b.serverCalls(); got != 1 {
		t.Fatalf("Servers() called %d times inside the TTL, want 1", got)
	}

	// Past the TTL it probes again, in the background.
	clock.advance(serversCacheTTL + time.Second)
	doGET(t, c, ts.URL+"/ui/api/state").Body.Close()
	waitForServerCalls(t, b, 2)
	if got := b.serverCalls(); got != 2 {
		t.Fatalf("Servers() called %d times after the TTL, want 2", got)
	}
}

func TestLocalLogsPaging(t *testing.T) {
	ring := NewLogRing()
	logger := slog.New(ring)
	logger.Info("one")
	logger.Info("two")
	logger.Info("three")

	b := &fakeBackend{}
	s, ts := startLocal(t, b, ring)
	c := newTestClient()
	login(t, s, ts, c)

	var page1 proto.LogsResponse
	resp := doGET(t, c, ts.URL+"/ui/api/logs?after=0&limit=2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui/api/logs = %d, want 200", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&page1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(page1.Entries) != 2 || page1.Entries[0].Seq != 1 || page1.Next != 2 {
		t.Fatalf("page1 = %+v", page1)
	}

	var page2 proto.LogsResponse
	resp = doGET(t, c, ts.URL+"/ui/api/logs?after=2&limit=2")
	if err := json.NewDecoder(resp.Body).Decode(&page2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(page2.Entries) != 1 || page2.Entries[0].Seq != 3 || page2.Next != 3 {
		t.Fatalf("page2 = %+v", page2)
	}
}

// TestLocalSSEEmitsOnlyOnChange connects to the stream and asserts it sends
// nothing while the snapshot is unchanged and one event after a change.
func TestLocalSSEEmitsOnlyOnChange(t *testing.T) {
	b := &fakeBackend{pins: []PinStatus{{Pin: "pi/xbox", Server: "pi", State: "idle"}}}
	s, ts := startLocal(t, b, nil)
	c := newTestClient()
	login(t, s, ts, c)

	resp := doGET(t, c, ts.URL+"/ui/api/events")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GET /ui/api/events = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	defer resp.Body.Close()

	events := make(chan string, 8)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(resp.Body)
		var data []string
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if len(data) > 0 {
					events <- strings.Join(data, "\n")
					data = nil
				}
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()

	select {
	case ev := <-events:
		t.Fatalf("unexpected event before any change: %q", ev)
	case <-time.After(150 * time.Millisecond):
	}

	b.setPins(PinStatus{Pin: "pi/xbox", Server: "pi", State: "attached", Port: 2})
	select {
	case ev := <-events:
		if !strings.Contains(ev, "attached") {
			t.Fatalf("state event does not carry the change: %q", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no state event after the snapshot changed")
	}
}

// TestLocalTypesHaveNoCredentialFields is a structural guard: the payload types
// the browser receives must not grow a credential field.
func TestLocalTypesHaveNoCredentialFields(t *testing.T) {
	// Exact field names, not substrings: TokenValid is a boolean status, not a
	// credential, and must stay.
	forbidden := []string{"token", "credential", "authorization", "secret", "password", "apikey", "api_token"}
	for _, typ := range []reflect.Type{reflect.TypeOf(PinStatus{}), reflect.TypeOf(ServerStatus{}), reflect.TypeOf(LocalState{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, bad := range forbidden {
				if name == bad {
					t.Errorf("%s.%s looks like a credential field", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
}

// TestLocalStateJSONHasNoCredentialKeys walks the state payload's keys and
// asserts none names a credential. The types carry no token field, so the
// browser cannot be handed one.
func TestLocalStateJSONHasNoCredentialKeys(t *testing.T) {
	b := &fakeBackend{
		pins:    []PinStatus{{Pin: "pi/xbox", Server: "pi", State: "attached", LastError: "attach failed"}},
		servers: []ServerStatus{{Name: "pi", Host: "pi.local", APIPort: 3241, Err: "unreachable"}},
	}
	ring := NewLogRing()
	slog.New(ring).Info("client started", "servers", "1")
	s, ts := startLocal(t, b, ring)
	c := newTestClient()
	login(t, s, ts, c)

	resp := doGET(t, c, ts.URL+"/ui/api/state")
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	// Exact key names, not substrings: token_valid is a boolean status.
	forbidden := []string{"token", "credential", "authorization", "secret", "password", "apikey", "api_token"}
	var walk func(path string, raw []byte)
	walk = func(path string, raw []byte) {
		trimmed := strings.TrimSpace(string(raw))
		switch {
		case strings.HasPrefix(trimmed, "{"):
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			for k, v := range obj {
				lower := strings.ToLower(k)
				for _, bad := range forbidden {
					if lower == bad {
						t.Errorf("state key %s.%s looks like a credential", path, k)
					}
				}
				walk(path+"."+k, v)
			}
		case strings.HasPrefix(trimmed, "["):
			var arr []json.RawMessage
			if err := json.Unmarshal(raw, &arr); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			for _, elem := range arr {
				walk(path+"[]", elem)
			}
		}
	}
	walk("state", body)
}

// blockingBackend blocks in Servers() until release is closed, modelling a
// probe against a powered-off Pi that does not answer until the per-client
// timeout.
type blockingBackend struct {
	*fakeBackend
	once    sync.Once
	started chan struct{}
	release chan struct{}
	calls   int32
}

func newBlockingBackend() *blockingBackend {
	return &blockingBackend{
		fakeBackend: &fakeBackend{
			pins: []PinStatus{{Pin: "pi/xbox", Server: "pi", State: "attached", Port: 3}},
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingBackend) Servers() []ServerStatus {
	atomic.AddInt32(&b.calls, 1)
	b.once.Do(func() { close(b.started) })
	<-b.release
	return nil
}

func (b *blockingBackend) serverCalls() int { return int(atomic.LoadInt32(&b.calls)) }

// TestLocalStateDoesNotBlockOnServerProbe is the regression for the state
// endpoint queuing behind a server probe. Servers() blocks forever here; the
// state request must still return promptly with the pins.
func TestLocalStateDoesNotBlockOnServerProbe(t *testing.T) {
	b := newBlockingBackend()
	defer close(b.release)

	s, ts := startLocal(t, b, nil)
	c := newTestClient()
	login(t, s, ts, c)

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := c.Get(ts.URL + "/ui/api/state")
		done <- result{resp, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("GET /ui/api/state: %v", r.err)
		}
		defer r.resp.Body.Close()
		if r.resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /ui/api/state = %d, want 200", r.resp.StatusCode)
		}
		var st LocalState
		if err := json.NewDecoder(r.resp.Body).Decode(&st); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(st.Pins) != 1 || st.Pins[0].Pin != "pi/xbox" {
			t.Fatalf("pins = %+v, want the pin served despite the blocked probe", st.Pins)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GET /ui/api/state blocked on the server probe")
	}

	// The probe runs off the request path, so it did start (and is blocked).
	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatal("the background server probe never started")
	}
}

// TestLocalServerProbeIsSingleFlight: a burst of state requests must not start
// more than one probe.
func TestLocalServerProbeIsSingleFlight(t *testing.T) {
	b := newBlockingBackend()
	defer close(b.release)

	s, ts := startLocal(t, b, nil)
	c := newTestClient()
	login(t, s, ts, c)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Get(ts.URL + "/ui/api/state")
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	// Give a wrong implementation a chance to start extra probes.
	time.Sleep(50 * time.Millisecond)
	if got := b.serverCalls(); got != 1 {
		t.Fatalf("Servers() called %d times for a burst, want 1 (single-flight)", got)
	}
}

func TestOriginMatchesHost(t *testing.T) {
	const host = "127.0.0.1:54321"
	cases := []struct {
		name   string
		origin string
		ref    string
		want   bool
	}{
		{"no origin or referer", "", "", true},
		{"matching origin", "http://" + host, "", true},
		{"matching origin is case-insensitive", "http://127.0.0.1:54321", "", true},
		{"mismatching origin", "http://evil.example", "", false},
		{"origin null", "null", "", false},
		{"matching referer fallback", "", "http://" + host + "/ui/login", true},
		{"mismatching referer fallback", "", "http://evil.example/ui/login", false},
		{"malformed origin", "http://[::1", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
			req.Host = host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.ref != "" {
				req.Header.Set("Referer", tc.ref)
			}
			if got := originMatchesHost(req); got != tc.want {
				t.Errorf("originMatchesHost = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOneTimeCodesBindRedeemBound(t *testing.T) {
	c := NewOneTimeCodes()

	code := c.Issue()
	if !c.Bind(code, "browser-a") {
		t.Fatal("Bind(code, browser-a) = false, want true")
	}
	// A second browser cannot claim an already-bound code.
	if c.Bind(code, "browser-b") {
		t.Error("Bind(code, browser-b) = true, want false")
	}
	// A cookie belonging to a different code is rejected.
	other := c.Issue()
	if !c.Bind(other, "browser-b") {
		t.Fatal("Bind(other, browser-b) = false, want true")
	}
	if c.RedeemBound(code, "browser-b") {
		t.Error("RedeemBound(code, browser-b) = true, want false")
	}
	if c.RedeemBound(other, "browser-a") {
		t.Error("RedeemBound(other, browser-a) = true, want false")
	}
	// The owner redeems exactly once.
	if !c.RedeemBound(code, "browser-a") {
		t.Fatal("RedeemBound(code, browser-a) = false, want true")
	}
	if c.RedeemBound(code, "browser-a") {
		t.Error("second RedeemBound(code, browser-a) = true, want single use")
	}
	// An unbound code cannot be redeemed by anyone.
	unbound := c.Issue()
	if c.RedeemBound(unbound, "browser-a") {
		t.Error("RedeemBound(unbound code) = true, want false")
	}
}

// TestOneTimeCodesConcurrentRedeemAdmitsOne: concurrent redemptions of one bound
// code must admit exactly one winner.
func TestOneTimeCodesConcurrentRedeemAdmitsOne(t *testing.T) {
	c := NewOneTimeCodes()
	code := c.Issue()
	if !c.Bind(code, "browser-a") {
		t.Fatal("Bind failed")
	}

	const n = 32
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.RedeemBound(code, "browser-a") {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("concurrent RedeemBound admitted %d winners, want exactly 1", wins)
	}
}

func TestLocalHostCheckVariants(t *testing.T) {
	s, err := NewLocal(LocalConfig{Backend: &fakeBackend{}, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	setAddr := func(a string) {
		s.mu.Lock()
		s.addr = a
		s.mu.Unlock()
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.security(next)

	cases := []struct {
		name string
		addr string
		host string
		want int
	}{
		{"exact bound host", "127.0.0.1:54321", "127.0.0.1:54321", http.StatusOK},
		{"localhost name is not the bound address", "127.0.0.1:54321", "localhost:54321", http.StatusForbidden},
		{"wrong port", "127.0.0.1:54321", "127.0.0.1:54322", http.StatusForbidden},
		{"case-insensitive match", "ExampleHost:54321", "examplehost:54321", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setAddr(tc.addr)
			req := httptest.NewRequest(http.MethodGet, "http://example/", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("Host %q with addr %q = %d, want %d", tc.host, tc.addr, rec.Code, tc.want)
			}
		})
	}
}

func assertHardeningHeaders(t *testing.T, resp *http.Response) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": localCSP,
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
	}
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// TestLocalEarlyForbiddenCarriesHardeningHeaders: the 403s written by the
// security wrapper before a handler runs must still carry the hardening
// headers.
func TestLocalEarlyForbiddenCarriesHardeningHeaders(t *testing.T) {
	b := &fakeBackend{}
	_, ts := startLocal(t, b, nil)

	// Foreign Host.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/ui/login", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("foreign Host request: %v", err)
	}
	assertHardeningHeaders(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host = %d, want 403", resp.StatusCode)
	}

	// CORS preflight.
	req, err = http.NewRequest(http.MethodOptions, ts.URL+"/ui/api/state", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight request: %v", err)
	}
	assertHardeningHeaders(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("preflight = %d, want 403", resp.StatusCode)
	}

	// Cross-origin GET.
	req, err = http.NewRequest(http.MethodGet, ts.URL+"/ui/login", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", "http://evil.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cross-origin request: %v", err)
	}
	assertHardeningHeaders(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin = %d, want 403", resp.StatusCode)
	}
}

// TestLocalUnknownPinReturns404: a Backend "unknown pin" sentinel maps to 404,
// matching the Pi-side API, rather than 500.
func TestLocalUnknownPinReturns404(t *testing.T) {
	sentinel := errors.New("unknown pin")
	b := &fakeBackend{attachErr: sentinel}
	s, ts := startLocalCfg(t, LocalConfig{
		Backend:    b,
		Listen:     "127.0.0.1:0",
		UnknownErr: sentinel,
	})
	c := newTestClient()
	login(t, s, ts, c)

	resp := doPOST(t, c, ts.URL+"/ui/api/pins/pi/nope/attach", nil,
		map[string]string{CSRFHeader: CSRFHeaderValue})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown pin attach = %d, want 404 (body %s)", resp.StatusCode, body)
	}
}
