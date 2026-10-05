package webui

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
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

// startLocal binds the server to a real loopback port and serves it. The
// returned httptest.Server owns the listener so ts.URL and the Host header the
// server requires agree exactly.
func startLocal(t *testing.T, b Backend, logs *LogRing) (*LocalServer, *httptest.Server) {
	t.Helper()
	s, err := NewLocal(LocalConfig{Backend: b, Logs: logs, Listen: "127.0.0.1:0"})
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
	// This test is about binding, not rate limiting; do not let the thief's
	// failed attempt throttle the owner.
	s.login.interval = 0

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

func TestLocalLoginRateLimit(t *testing.T) {
	b := &fakeBackend{}
	s, ts := startLocal(t, b, nil)
	now := time.Unix(1000, 0)
	s.login.now = func() time.Time { return now }

	first := doPOST(t, newTestClient(), ts.URL+"/ui/login", url.Values{"code": {"wrong"}}, nil)
	first.Body.Close()
	if first.StatusCode != http.StatusUnauthorized {
		t.Fatalf("first failed attempt = %d, want 401", first.StatusCode)
	}

	immediate := doPOST(t, newTestClient(), ts.URL+"/ui/login", url.Values{"code": {"wrong"}}, nil)
	immediate.Body.Close()
	if immediate.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("immediate second attempt = %d, want 429", immediate.StatusCode)
	}

	now = now.Add(time.Second)
	after := doPOST(t, newTestClient(), ts.URL+"/ui/login", url.Values{"code": {"wrong"}}, nil)
	after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("attempt after the limiter window = %d, want 401", after.StatusCode)
	}
}

func TestLocalStateServesPinsAndCachesServers(t *testing.T) {
	b := &fakeBackend{
		pins:    []PinStatus{{Pin: "pi/xbox", Server: "pi", State: "attached", Port: 3}},
		servers: []ServerStatus{{Name: "pi", Host: "pi.local", APIPort: 3241, Reachable: true, TokenValid: true}},
	}
	s, ts := startLocal(t, b, nil)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	c := newTestClient()
	login(t, s, ts, c)

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
	if len(st.Servers) != 1 || st.Servers[0].Name != "pi" {
		t.Fatalf("servers = %+v", st.Servers)
	}

	// A second request inside the TTL must not probe again.
	doGET(t, c, ts.URL+"/ui/api/state").Body.Close()
	if got := b.serverCalls(); got != 1 {
		t.Fatalf("Servers() called %d times inside the TTL, want 1", got)
	}

	// Past the TTL it probes again.
	now = now.Add(serversCacheTTL + time.Second)
	doGET(t, c, ts.URL+"/ui/api/state").Body.Close()
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
