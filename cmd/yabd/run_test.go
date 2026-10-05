package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/config"
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
