package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/proto"
)

type fakeBackend struct {
	devices  []proto.Device
	exported []string
	forced   []bool
}

func (f *fakeBackend) Info(context.Context) proto.Info { return proto.Info{Version: "test"} }

func (f *fakeBackend) Devices(context.Context) []proto.Device { return f.devices }

func (f *fakeBackend) Device(_ context.Context, id string) (proto.Device, bool) {
	for _, d := range f.devices {
		if d.Pin == id || d.BusID == id {
			return d, true
		}
	}
	return proto.Device{}, false
}

func (f *fakeBackend) Export(_ context.Context, id string, force bool) error {
	f.exported = append(f.exported, id)
	f.forced = append(f.forced, force)
	return nil
}

func (f *fakeBackend) Unexport(context.Context, string) error { return nil }

func (f *fakeBackend) Reset(context.Context, string) error { return nil }

func (f *fakeBackend) Subscribe(ctx context.Context) (<-chan proto.Event, func()) {
	ch := make(chan proto.Event)
	return ch, func() {}
}

const testToken = "s3cret-token"

func newTestServer(t *testing.T) (*Server, *fakeBackend) {
	t.Helper()
	backend := &fakeBackend{devices: []proto.Device{
		{Pin: "bt", BusID: "1-1.2", VID: "0a12", PID: "0001", Present: true},
	}}
	srv, err := New(Config{
		Listen:         "127.0.0.1:0",
		Token:          testToken,
		AllowedClients: []string{"10.0.0.0/8"},
		Backend:        backend,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, backend
}

func request(t *testing.T, srv *Server, method, target, remoteAddr, token string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthzUnauthenticated(t *testing.T) {
	srv, _ := newTestServer(t)
	// Deliberately outside the allowlist and with no token.
	rec := request(t, srv, http.MethodGet, "/healthz", "203.0.113.9:1234", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestMissingTokenUnauthorized(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := request(t, srv, http.MethodGet, "/v1/devices", "10.0.0.5:1234", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !isErrorCode(t, rec, "unauthorized") {
		t.Error("body did not carry the unauthorized error code")
	}
}

func TestWrongTokenUnauthorized(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := request(t, srv, http.MethodGet, "/v1/devices", "10.0.0.5:1234", "wrong-token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCorrectTokenSucceeds(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := request(t, srv, http.MethodGet, "/v1/devices", "10.0.0.5:1234", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp proto.ListDevicesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Devices) != 1 || resp.Devices[0].Pin != "bt" {
		t.Errorf("devices = %+v", resp.Devices)
	}
}

func TestSourceOutsideAllowlistForbidden(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := request(t, srv, http.MethodGet, "/v1/devices", "203.0.113.5:1234", testToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !isErrorCode(t, rec, "forbidden") {
		t.Error("body did not carry the forbidden error code")
	}
}

func TestForwardedForIgnored(t *testing.T) {
	srv, _ := newTestServer(t)
	// The header claims an allowlisted source, but the connection does not.
	rec := request(t, srv, http.MethodGet, "/v1/devices", "203.0.113.5:1234", testToken,
		map[string]string{"X-Forwarded-For": "10.0.0.5"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (X-Forwarded-For must be ignored)", rec.Code)
	}
}

func TestUnknownDeviceNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := request(t, srv, http.MethodGet, "/v1/devices/ghost", "10.0.0.5:1234", testToken, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestExportForcePassedThrough(t *testing.T) {
	srv, backend := newTestServer(t)
	rec := request(t, srv, http.MethodPost, "/v1/devices/bt/export?force=true", "10.0.0.5:1234", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(backend.exported) != 1 || backend.exported[0] != "bt" {
		t.Fatalf("exported = %v", backend.exported)
	}
	if !backend.forced[0] {
		t.Error("force was not passed through")
	}
}

func TestClientRoundTrip(t *testing.T) {
	backend := &fakeBackend{devices: []proto.Device{{Pin: "bt", BusID: "1-1.2", Present: true}}}
	srv, err := New(Config{
		Token:          testToken,
		AllowedClients: []string{"127.0.0.0/8"},
		Backend:        backend,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := NewClient(ts.URL, testToken, 5*time.Second)
	ctx := context.Background()

	devs, err := client.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) != 1 || devs[0].Pin != "bt" {
		t.Errorf("devices = %+v", devs)
	}
	if err := client.Export(ctx, "bt", true); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if _, err := client.Device(ctx, "ghost"); !IsNotFound(err) {
		t.Errorf("Device(ghost) error = %v, want not found", err)
	}
}

func isErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) bool {
	t.Helper()
	var e proto.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return e.Code == want
}
