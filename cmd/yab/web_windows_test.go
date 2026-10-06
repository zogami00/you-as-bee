//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/webui"
)

// TestWebBackendNeverMarshalsToken is the Windows-side half of the "the
// browser never receives a Pi token" guarantee: the adapter's Status and Servers
// types have no token field, so marshalling them cannot leak the configured
// credential even though the manager holds it.
func TestWebBackendNeverMarshalsToken(t *testing.T) {
	token := strings.Repeat("a", 64)
	cfg := config.ClientConfig{
		Servers: []config.ServerConfig{
			{Name: "pi", Host: "127.0.0.1", APIPort: 1, Token: token},
		},
		AutoAttach:     []config.AutoAttach{{Server: "pi", Device: "xbox"}},
		CommandTimeout: config.Duration(200 * time.Millisecond),
	}
	m, err := client.New(client.Options{Config: cfg, USBIP: noopUSBIP{}})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	b := newWebBackend(nil, m)

	payload, err := json.Marshal(struct {
		Pins    any `json:"pins"`
		Servers any `json:"servers"`
	}{Pins: b.Status(), Servers: b.Servers()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(payload, []byte(token)) {
		t.Fatalf("the web backend payload contains the Pi token:\n%s", payload)
	}
	// A boolean status such as "token_valid" is fine; a credential-bearing key
	// named exactly "token" (or similar) is not.
	for _, key := range []string{"token", "authorization", "credential", "bearer", "api_token"} {
		if bytes.Contains(payload, []byte(`"`+key+`":`)) {
			t.Errorf("the web backend payload has a %q key:\n%s", key, payload)
		}
	}
}

// stubWebBackend satisfies webui.Backend for tests that only exercise the
// local server shell.
type stubWebBackend struct{}

func (stubWebBackend) Status() []webui.PinStatus     { return nil }
func (stubWebBackend) Servers() []webui.ServerStatus { return nil }
func (stubWebBackend) Attach(string) error           { return nil }
func (stubWebBackend) Detach(string) error           { return nil }

// TestOpenUIFailureDoesNotLeakLoginCode: when the browser launch fails, neither
// the log ring nor the returned error may contain the live one-time code. Only
// the base URL is logged.
func TestOpenUIFailureDoesNotLeakLoginCode(t *testing.T) {
	srv, err := webui.NewLocal(webui.LocalConfig{Backend: stubWebBackend{}, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	ring := webui.NewLogRing()
	tc := &trayController{web: srv, log: slog.New(ring)}

	var launched string
	orig := launchBrowser
	launchBrowser = func(u string) error {
		launched = u
		return errors.New("boom")
	}
	t.Cleanup(func() { launchBrowser = orig })

	err = tc.OpenUI()
	if err == nil {
		t.Fatal("OpenUI with a failing launch = nil, want an error")
	}

	u, perr := url.Parse(launched)
	if perr != nil {
		t.Fatalf("parse launched URL %q: %v", launched, perr)
	}
	// The code must be a path segment, not a query value: explorer.exe rejects
	// a URL containing "?" and opens a folder instead of the browser.
	if u.RawQuery != "" {
		t.Errorf("the login URL carries a query string: %q", launched)
	}
	const codePrefix = "/ui/login/"
	if !strings.HasPrefix(u.Path, codePrefix) {
		t.Fatalf("launchBrowser was not called with a path code URL: %q", launched)
	}
	code := strings.TrimPrefix(u.Path, codePrefix)
	if code == "" {
		t.Fatalf("launchBrowser was not called with a code URL: %q", launched)
	}
	if strings.Contains(err.Error(), code) {
		t.Errorf("the OpenUI error leaks the one-time code %q: %v", code, err)
	}
	if !strings.Contains(err.Error(), srv.BaseURL()) {
		t.Errorf("the OpenUI error does not name the base URL: %v", err)
	}

	for _, e := range ring.Entries(0, webui.DefaultLogsLimit).Entries {
		if strings.Contains(e.Msg, code) {
			t.Errorf("the log ring message leaks the one-time code %q: %q", code, e.Msg)
		}
		for k, v := range e.Attrs {
			if strings.Contains(v, code) {
				t.Errorf("log attr %s leaks the one-time code %q: %q", k, code, v)
			}
		}
	}
}

func TestExplorerPathIsAbsolute(t *testing.T) {
	t.Setenv("SystemRoot", `D:\Windows`)
	got := explorerPath()
	want := filepath.Join(`D:\Windows`, "explorer.exe")
	if got != want {
		t.Errorf("explorerPath() = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("explorerPath() = %q, want an absolute path", got)
	}

	// An empty SystemRoot still yields an absolute fallback, never a bare name.
	t.Setenv("SystemRoot", "")
	got = explorerPath()
	if !filepath.IsAbs(got) || !strings.HasSuffix(strings.ToLower(got), "explorer.exe") {
		t.Errorf("explorerPath() with an empty SystemRoot = %q, want an absolute path ending in explorer.exe", got)
	}
}

func TestRundll32PathIsAbsolute(t *testing.T) {
	t.Setenv("SystemRoot", `D:\Windows`)
	got := rundll32Path()
	want := filepath.Join(`D:\Windows`, "System32", "rundll32.exe")
	if got != want {
		t.Errorf("rundll32Path() = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("rundll32Path() = %q, want an absolute path", got)
	}

	t.Setenv("SystemRoot", "")
	got = rundll32Path()
	if !filepath.IsAbs(got) || !strings.HasSuffix(strings.ToLower(got), "rundll32.exe") {
		t.Errorf("rundll32Path() with an empty SystemRoot = %q, want an absolute path ending in rundll32.exe", got)
	}
}

// TestWebUIHTTPNeverExposesToken exercises the real HTTP responses: with a real
// Manager that holds the token and has a populated last_error (a failed attach
// to an unreachable server), the 64-hex token must appear nowhere in the state,
// events or logs bodies. The existing marshalling test covers only the values.
func TestWebUIHTTPNeverExposesToken(t *testing.T) {
	token := strings.Repeat("ab", 32) // exactly 64 lower-case hex chars
	cfg := config.ClientConfig{
		Servers: []config.ServerConfig{
			{Name: "pi", Host: "127.0.0.1", APIPort: 1, Token: token},
		},
		AutoAttach:     []config.AutoAttach{{Server: "pi", Device: "xbox"}},
		CommandTimeout: config.Duration(200 * time.Millisecond),
		Reconnect:      config.ReconnectConfig{Initial: config.Duration(time.Second), Max: config.Duration(time.Second)},
	}
	ring := webui.NewLogRing()
	logger := slog.New(ring)

	m, err := client.New(client.Options{Config: cfg, USBIP: noopUSBIP{}, Log: logger})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	// The unreachable server makes the first reconcile fail, populating
	// last_error on the pin.
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	statuses := m.Status()
	if len(statuses) != 1 || statuses[0].LastError == "" {
		t.Fatalf("manager status = %+v, want one pin with a populated last_error", statuses)
	}

	srv, err := startLocalWebUI(cfg, newWebBackend(nil, m), ring, logger)
	if err != nil {
		t.Fatalf("startLocalWebUI: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	loginURL := srv.NewLoginURL()
	u, err := url.Parse(loginURL)
	if err != nil {
		t.Fatalf("parse login URL: %v", err)
	}
	const codePrefix = "/ui/login/"
	if u.RawQuery != "" || !strings.HasPrefix(u.Path, codePrefix) {
		t.Fatalf("NewLoginURL must use the query-free path form: %q", loginURL)
	}
	code := strings.TrimPrefix(u.Path, codePrefix)
	if code == "" {
		t.Fatalf("NewLoginURL carried no code: %q", loginURL)
	}

	resp, err := hc.Get(loginURL)
	if err != nil {
		t.Fatalf("GET login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET login = %d, want 200", resp.StatusCode)
	}

	resp, err = hc.PostForm(srv.BaseURL()+"/ui/login", url.Values{"code": {code}})
	if err != nil {
		t.Fatalf("POST login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST login = %d, want 303", resp.StatusCode)
	}

	for _, path := range []string{"/ui/api/state", "/ui/api/logs"} {
		resp, err := hc.Get(srv.BaseURL() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if strings.Contains(string(body), token) {
			t.Errorf("GET %s leaks the token:\n%s", path, body)
		}
		if path == "/ui/api/state" && !strings.Contains(string(body), statuses[0].LastError) {
			t.Errorf("state body does not carry the populated last_error %q:\n%s", statuses[0].LastError, body)
		}
	}

	// The event stream is long-lived and emits nothing initially; read for a
	// bounded window and check whatever arrives.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.BaseURL()+"/ui/api/events", nil)
	if err != nil {
		t.Fatalf("new events request: %v", err)
	}
	resp, err = hc.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), token) {
		t.Errorf("GET /ui/api/events leaks the token:\n%s", body)
	}
}
