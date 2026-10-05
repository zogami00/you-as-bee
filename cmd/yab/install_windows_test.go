//go:build windows

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/execx"
	"github.com/zogami00/you-as-bee/internal/usbipwin"
)

func argContains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// M3/B1: the Go installer must register the task with the settings that stop
// Windows killing it after 72 hours or refusing to start it on battery.
func TestRegisterTaskUsesScheduledTaskSettings(t *testing.T) {
	fr := execx.NewFakeRunner()
	if err := registerTask(context.Background(), fr, `C:\Program Files\you-as-bee\yab.exe`); err != nil {
		t.Fatalf("registerTask: %v", err)
	}
	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d: %+v", len(calls), calls)
	}
	c := calls[0]
	if !strings.EqualFold(c.Name, "powershell.exe") {
		t.Fatalf("command = %q, want powershell.exe", c.Name)
	}
	joined := strings.Join(c.Args, " ")
	for _, want := range []string{
		"Register-ScheduledTask",
		"New-ScheduledTaskAction",
		"New-ScheduledTaskTrigger -AtLogOn",
		"-Trigger $t",
		"New-ScheduledTaskSettingsSet",
		"-ExecutionTimeLimit 0",
		"-AllowStartIfOnBatteries",
		"-DontStopIfGoingOnBatteries",
		"-StartWhenAvailable",
		"-RunLevel Highest",
		"-Argument 'tray'",
		"'C:\\Program Files\\you-as-bee\\yab.exe'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("task script missing %q:\n%s", want, joined)
		}
	}
}

// M4: log_file is actually opened and written, and an unset log_file discards.
func TestNewLoggerHonoursLogFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "yab-log")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	// The logger keeps the file open for the process lifetime (as the tray
	// does), so a t.TempDir cleanup would fail on the still-open handle.
	path := filepath.Join(dir, "yab.log")
	lg, err := newLogger(config.ClientConfig{LogFile: path, LogLevel: "warn"})
	if err != nil {
		t.Fatalf("newLogger: %v", err)
	}
	lg.Warn("attach failed", "pin", "pi/xbox")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "attach failed") {
		t.Fatalf("log file = %q, want the warning", data)
	}
}

func TestNewLoggerDiscardsWhenUnset(t *testing.T) {
	lg, err := newLogger(config.ClientConfig{})
	if err != nil {
		t.Fatalf("newLogger: %v", err)
	}
	if lg == nil {
		t.Fatal("want a non-nil logger")
	}
}

func TestPsSingleQuoteEscapesQuote(t *testing.T) {
	if got := psSingleQuote(`C:\O'Brien\yab.exe`); got != `'C:\O''Brien\yab.exe'` {
		t.Fatalf("psSingleQuote = %s", got)
	}
}

func TestStopClientEndsTaskAndExcludesSelf(t *testing.T) {
	fr := execx.NewFakeRunner()
	stopClient(context.Background(), fr)

	var sawEnd, sawKill bool
	for _, c := range fr.Calls() {
		if c.Name == "schtasks" && argContains(c.Args, "/End") {
			sawEnd = true
		}
		if c.Name == "taskkill" {
			sawKill = true
			if !argContains(c.Args, fmt.Sprintf("PID ne %d", os.Getpid())) {
				t.Errorf("taskkill must exclude this process: %v", c.Args)
			}
		}
	}
	if !sawEnd || !sawKill {
		t.Fatalf("calls = %+v, want schtasks /End and taskkill", fr.Calls())
	}
}

// A detach with no bus id must not fall back to detaching every port on the
// server; when the API is unreachable it must fail instead.
func TestDetachDeviceRequiresBusID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	cfg := config.ClientConfig{
		Servers:        []config.ServerConfig{{Name: "pi", Host: host, APIPort: port, Token: strings.Repeat("a", 64)}},
		AutoAttach:     []config.AutoAttach{{Server: "pi", Device: "xbox"}},
		CommandTimeout: config.Duration(2 * time.Second),
	}
	fr := execx.NewFakeRunner()
	tool := usbipwin.New("usbip.exe", fr)

	err = detachDevice(context.Background(), cfg, tool, "pi/xbox")
	if err == nil || !strings.Contains(err.Error(), "cannot determine bus id") {
		t.Fatalf("err = %v, want a cannot-determine-bus-id error", err)
	}
	if fr.CallCount() != 0 {
		t.Fatalf("runner invoked %d times; it must not detach when the bus id is unknown", fr.CallCount())
	}
}
