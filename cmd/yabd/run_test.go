package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/api"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/proto"
)

func TestReadTokenRequires64Hex(t *testing.T) {
	valid := strings.Repeat("a", 64)
	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{"valid", valid + "\n", false},
		{"valid no newline", valid, false},
		{"short", "abcd", true},
		{"uppercase", strings.Repeat("A", 64), true},
		{"non-hex", strings.Repeat("z", 64), true},
		{"empty", "", true},
		{"whitespace only", "   \n", true},
		{"63 chars", strings.Repeat("a", 63), true},
		{"65 chars", strings.Repeat("a", 65), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readToken(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("readToken = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readToken: %v", err)
			}
			if got != valid {
				t.Errorf("token = %q, want the trimmed valid token", got)
			}
		})
	}
}

func TestSaveConfigAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	body := `{"schema_version":1,"devices":[{"name":"bt","vid":"0a12","pid":"0001"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg config.AgentConfig
	if err := config.Load(path, &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Devices[0].Mode = config.ModeAlways
	if err := saveConfig(path, &cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	var got config.AgentConfig
	if err := config.Load(path, &got); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Devices[0].Mode != config.ModeAlways {
		t.Errorf("mode = %q, want %q", got.Devices[0].Mode, config.ModeAlways)
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".you-as-bee-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSaveConfigInvalidLeavesOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	body := `{"schema_version":1,"devices":[{"name":"bt","vid":"0a12","pid":"0001"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var cfg config.AgentConfig
	if err := config.Load(path, &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Devices[0].VID = "zzzz"
	if err := saveConfig(path, &cfg); err == nil {
		t.Fatal("saveConfig with an invalid config: expected error")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("an invalid config must not modify the existing file")
	}
}

func TestWaitReconciler(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if !waitReconciler(done, time.Second) {
		t.Error("closed channel: waitReconciler = false, want true")
	}

	open := make(chan struct{})
	start := time.Now()
	if waitReconciler(open, 50*time.Millisecond) {
		t.Error("open channel: waitReconciler = true, want false on timeout")
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("waitReconciler returned after %s, want it to wait for the timeout", elapsed)
	}
}

// staticAgent serves one device on every GET.
func staticAgent(t *testing.T, dev proto.Device) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dev)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The timeout error must name the pin and the last state, not just "context
// deadline exceeded".
func TestWaitForExportedTimeoutReportsLastState(t *testing.T) {
	srv := staticAgent(t, proto.Device{Pin: "bt", State: proto.StateUnexported, Present: true})
	client := api.NewClient(srv.URL, strings.Repeat("a", 64), time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := waitForExported(ctx, client, "bt")
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "bt") || !strings.Contains(err.Error(), proto.StateUnexported) {
		t.Fatalf("error = %q, want it to name the pin and the last seen state", err)
	}
}

// An error/quarantine state must be reported at once, not waited out.
func TestWaitForExportedErrorState(t *testing.T) {
	srv := staticAgent(t, proto.Device{Pin: "bt", State: proto.StateError, Present: true})
	client := api.NewClient(srv.URL, strings.Repeat("a", 64), time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := waitForExported(ctx, client, "bt")
	if err == nil || !strings.Contains(err.Error(), "error state") {
		t.Fatalf("error = %v, want an error-state report", err)
	}
}
