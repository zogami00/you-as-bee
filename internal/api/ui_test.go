package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
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
	var body *strings.Reader
	req := httptest.NewRequest(method, target, nil)
	if vals != nil {
		body = strings.NewReader(vals.Encode())
		req = httptest.NewRequest(method, target, body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.RemoteAddr = "10.0.0.5:1234"
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

var codeRe = regexp.MustCompile(`name="code" value="([0-9a-f]+)"`)

func extractCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := codeRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no one-time code in login form:\n%s", rec.Body.String())
	}
	return m[1]
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
	code := extractCode(t, form)

	login := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {code}, "token": {testToken}}, "", nil)
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
	form := doUI(t, srv, http.MethodGet, "/ui/login", nil, "", nil)
	code := extractCode(t, form)

	rec := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {code}, "token": {"wrong-token"}}, "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestLoginCodeIsSingleUse(t *testing.T) {
	srv := newWebUIServer(t, true)
	form := doUI(t, srv, http.MethodGet, "/ui/login", nil, "", nil)
	code := extractCode(t, form)

	first := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {code}, "token": {testToken}}, "", nil)
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first login = %d, want 303", first.Code)
	}

	// The same code is dead, so a replay fails even with the right token.
	replay := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {code}, "token": {testToken}}, "", nil)
	if replay.Code == http.StatusSeeOther {
		t.Fatal("a spent one-time code was accepted")
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv := newWebUIServer(t, true)
	now := time.Unix(1000, 0)
	srv.login.now = func() time.Time { return now }

	form := doUI(t, srv, http.MethodGet, "/ui/login", nil, "", nil)
	code := extractCode(t, form)

	first := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {code}, "token": {"wrong-token"}}, "", nil)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first failed attempt = %d, want 401", first.Code)
	}

	immediate := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {code}, "token": {testToken}}, "", nil)
	if immediate.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate second attempt = %d, want 429", immediate.Code)
	}

	now = now.Add(time.Second)
	form = doUI(t, srv, http.MethodGet, "/ui/login", nil, "", nil)
	code = extractCode(t, form)
	after := doUI(t, srv, http.MethodPost, "/ui/login",
		url.Values{"code": {code}, "token": {testToken}}, "", nil)
	if after.Code != http.StatusSeeOther {
		t.Fatalf("attempt after the limiter window = %d, want 303", after.Code)
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
