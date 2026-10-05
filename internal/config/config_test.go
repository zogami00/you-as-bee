package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestAgentDefaults(t *testing.T) {
	var cfg AgentConfig
	if err := Load(writeTemp(t, `{}`), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
	if cfg.Listen != DefaultListen {
		t.Errorf("Listen = %q, want %q", cfg.Listen, DefaultListen)
	}
	if cfg.TokenFile != "/etc/you-as-bee/token" {
		t.Errorf("TokenFile = %q", cfg.TokenFile)
	}
	if got := strings.Join(cfg.AllowedClients, ","); got != strings.Join(DefaultAllowedClients, ",") {
		t.Errorf("AllowedClients = %v, want %v", cfg.AllowedClients, DefaultAllowedClients)
	}
	if cfg.PollInterval.Duration() != 5*time.Second {
		t.Errorf("PollInterval = %s, want 5s", cfg.PollInterval)
	}
	if cfg.USBIP.Bin != "/usr/sbin/usbip" || cfg.USBIP.UsbipdBin != "/usr/sbin/usbipd" {
		t.Errorf("USBIP defaults = %+v", cfg.USBIP)
	}
	if cfg.LogLevel != "info" || cfg.LogFormat != "text" {
		t.Errorf("log defaults = %q/%q", cfg.LogLevel, cfg.LogFormat)
	}
	if len(cfg.Devices) != 0 {
		t.Errorf("Devices = %v, want empty", cfg.Devices)
	}
}

func TestClientDefaults(t *testing.T) {
	var cfg ClientConfig
	if err := Load(writeTemp(t, `{}`), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
	if cfg.Reconnect.Initial.Duration() != 1*time.Second {
		t.Errorf("Reconnect.Initial = %s, want 1s", cfg.Reconnect.Initial)
	}
	if cfg.Reconnect.Max.Duration() != 30*time.Second {
		t.Errorf("Reconnect.Max = %s, want 30s", cfg.Reconnect.Max)
	}
	if cfg.CommandTimeout.Duration() != 15*time.Second {
		t.Errorf("CommandTimeout = %s, want 15s", cfg.CommandTimeout)
	}
	if cfg.ReceiveMode != ReceiveModeLowLatency {
		t.Errorf("ReceiveMode = %q, want %q", cfg.ReceiveMode, ReceiveModeLowLatency)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

// B9: receive_mode accepts the two known values and rejects anything else.
func TestClientReceiveMode(t *testing.T) {
	for _, mode := range []string{ReceiveModeLowLatency, ReceiveModeZeroCopy} {
		var cfg ClientConfig
		body := `{"schema_version":1,"receive_mode":"` + mode + `"}`
		if err := Load(writeTemp(t, body), &cfg); err != nil {
			t.Fatalf("Load(%q): %v", mode, err)
		}
		if cfg.ReceiveMode != mode {
			t.Errorf("ReceiveMode = %q, want %q", cfg.ReceiveMode, mode)
		}
	}

	var cfg ClientConfig
	err := Load(writeTemp(t, `{"schema_version":1,"receive_mode":"turbo"}`), &cfg)
	if err == nil {
		t.Fatal("expected an unknown receive_mode to be rejected")
	}
	if !strings.Contains(err.Error(), "receive_mode") {
		t.Errorf("error %q does not name receive_mode", err)
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	var cfg AgentConfig
	err := Load(writeTemp(t, `{"schema_version":1,"not_a_field":true}`), &cfg)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "not_a_field") {
		t.Errorf("error %q does not name the unknown field", err)
	}
}

func TestTrailingDataRejected(t *testing.T) {
	cases := map[string]string{
		"second document": `{"schema_version":1}{"schema_version":1}`,
		"garbage":         `{"schema_version":1} trailing`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			var cfg AgentConfig
			if err := Load(writeTemp(t, content), &cfg); err == nil {
				t.Fatal("expected trailing-data error, got nil")
			}
		})
	}
}

func TestAgentValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			name:    "bad vid",
			body:    `{"schema_version":1,"devices":[{"name":"bt","vid":"zzzz","pid":"3c4d"}]}`,
			wantSub: "vid",
		},
		{
			name:    "bad pid",
			body:    `{"schema_version":1,"devices":[{"name":"bt","vid":"1a2b","pid":"123"}]}`,
			wantSub: "pid",
		},
		{
			name:    "duplicate device names",
			body:    `{"schema_version":1,"devices":[{"name":"bt","vid":"1a2b","pid":"3c4d"},{"name":"bt","vid":"1a2b","pid":"3c4d"}]}`,
			wantSub: "duplicate device name",
		},
		{
			name:    "bad cidr",
			body:    `{"schema_version":1,"allowed_clients":["999.0.0.0/8"]}`,
			wantSub: "CIDR",
		},
		{
			name:    "unknown mode",
			body:    `{"schema_version":1,"devices":[{"name":"bt","vid":"1a2b","pid":"3c4d","mode":"sometimes"}]}`,
			wantSub: "mode",
		},
		{
			name:    "unknown log level",
			body:    `{"schema_version":1,"log_level":"verbose"}`,
			wantSub: "log_level",
		},
		{
			name:    "bad schema version",
			body:    `{"schema_version":2}`,
			wantSub: "schema_version",
		},
		{
			name:    "uppercase name",
			body:    `{"schema_version":1,"devices":[{"name":"BT","vid":"1a2b","pid":"3c4d"}]}`,
			wantSub: "name",
		},
		{
			name:    "empty name",
			body:    `{"schema_version":1,"devices":[{"name":"","vid":"1a2b","pid":"3c4d"}]}`,
			wantSub: "name",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg AgentConfig
			err := Load(writeTemp(t, tc.body), &cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestAgentVIDPIDNormalised(t *testing.T) {
	body := `{"schema_version":1,"devices":[{"name":"bt","vid":"1A2B","pid":"3C4D","mode":"always"}]}`
	var cfg AgentConfig
	if err := Load(writeTemp(t, body), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := cfg.Devices[0]
	if d.VID != "1a2b" || d.PID != "3c4d" {
		t.Errorf("vid/pid = %q/%q, want 1a2b/3c4d", d.VID, d.PID)
	}
	if d.Mode != ModeAlways {
		t.Errorf("Mode = %q, want %q", d.Mode, ModeAlways)
	}
}

func TestAgentDeviceModeDefault(t *testing.T) {
	body := `{"schema_version":1,"devices":[{"name":"bt","vid":"1a2b","pid":"3c4d"}]}`
	var cfg AgentConfig
	if err := Load(writeTemp(t, body), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Devices[0].Mode != ModeOnDemand {
		t.Errorf("Mode = %q, want %q", cfg.Devices[0].Mode, ModeOnDemand)
	}
}

func TestRemovedManageUsbipdIsRejected(t *testing.T) {
	var cfg AgentConfig
	err := Load(writeTemp(t, `{"schema_version":1,"usbip":{"manage_usbipd":false}}`), &cfg)
	if err == nil {
		t.Fatal("expected the removed manage_usbipd field to be rejected")
	}
	if !strings.Contains(err.Error(), "manage_usbipd") {
		t.Errorf("error %q does not name the removed field", err)
	}
}

func TestClientValidationErrors(t *testing.T) {
	validToken := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			name:    "short token",
			body:    `{"schema_version":1,"servers":[{"name":"pi","host":"pi.local","token":"abcd"}]}`,
			wantSub: "token",
		},
		{
			name:    "uppercase token",
			body:    `{"schema_version":1,"servers":[{"name":"pi","host":"pi.local","token":"` + strings.Repeat("A", 64) + `"}]}`,
			wantSub: "token",
		},
		{
			name:    "auto attach unknown server",
			body:    `{"schema_version":1,"auto_attach":[{"server":"ghost","device":"bt"}]}`,
			wantSub: "unknown server",
		},
		{
			name: "auto attach duplicate pair",
			body: `{"schema_version":1,"servers":[{"name":"pi","host":"pi.local","token":"` + validToken + `"}],` +
				`"auto_attach":[{"server":"pi","device":"bt"},{"server":"pi","device":"bt"}]}`,
			wantSub: "duplicate pair",
		},
		{
			name: "reconnect max below initial",
			body: `{"schema_version":1,"servers":[{"name":"pi","host":"pi.local","token":"` + validToken + `"}],` +
				`"reconnect":{"initial":"10s","max":"2s"}}`,
			wantSub: "reconnect.max",
		},
		{
			name:    "bad schema version",
			body:    `{"schema_version":3}`,
			wantSub: "schema_version",
		},
		{
			name:    "duplicate server name",
			body:    `{"schema_version":1,"servers":[{"name":"pi","host":"a.local","token":"` + validToken + `"},{"name":"pi","host":"b.local","token":"` + validToken + `"}]}`,
			wantSub: "duplicate server name",
		},
		{
			name:    "server name with slash",
			body:    `{"schema_version":1,"servers":[{"name":"a/b","host":"pi.local","token":"` + validToken + `"}]}`,
			wantSub: "servers[0].name",
		},
		{
			name:    "server name uppercase",
			body:    `{"schema_version":1,"servers":[{"name":"Pi","host":"pi.local","token":"` + validToken + `"}]}`,
			wantSub: "servers[0].name",
		},
		{
			name:    "bad api port",
			body:    `{"schema_version":1,"servers":[{"name":"pi","host":"a.local","token":"` + validToken + `","api_port":70000}]}`,
			wantSub: "api_port",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg ClientConfig
			err := Load(writeTemp(t, tc.body), &cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestClientServerDefaults(t *testing.T) {
	body := `{"schema_version":1,"servers":[{"name":"pi","host":"pi.local","token":"` + strings.Repeat("f", 64) + `"}]}`
	var cfg ClientConfig
	if err := Load(writeTemp(t, body), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Servers[0].APIPort != DefaultAPIPort {
		t.Errorf("APIPort = %d, want %d", cfg.Servers[0].APIPort, DefaultAPIPort)
	}
}

func TestDurationParsing(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"2s", 2 * time.Second},
		{"2m30s", 2*time.Minute + 30*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			body := `{"schema_version":1,"command_timeout":"` + tc.in + `"}`
			var cfg ClientConfig
			if err := Load(writeTemp(t, body), &cfg); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.CommandTimeout.Duration() != tc.want {
				t.Errorf("CommandTimeout = %s, want %s", cfg.CommandTimeout, tc.want)
			}
		})
	}
}

func TestDurationBareNumberRejected(t *testing.T) {
	body := `{"schema_version":1,"command_timeout":2}`
	var cfg ClientConfig
	err := Load(writeTemp(t, body), &cfg)
	if err == nil {
		t.Fatal("expected error for bare-number duration, got nil")
	}
	if !strings.Contains(err.Error(), "duration") {
		t.Errorf("error %q does not mention duration", err)
	}
}

func TestDurationMarshalRoundTrip(t *testing.T) {
	b, err := Duration(90 * time.Second).MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(b) != `"1m30s"` {
		t.Errorf("MarshalJSON = %s, want \"1m30s\"", b)
	}
	var d Duration
	if err := d.UnmarshalJSON(b); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if d.Duration() != 90*time.Second {
		t.Errorf("round trip = %s, want 1m30s", d)
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("YAB_TEST_HOST", "pi.local")
	got := ExpandEnv(`{"host":"${YAB_TEST_HOST}","port":"${YAB_TEST_MISSING}"}`)
	want := `{"host":"pi.local","port":""}`
	if got != want {
		t.Errorf("ExpandEnv = %q, want %q", got, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	var cfg AgentConfig
	if err := Load(filepath.Join(t.TempDir(), "nope.json"), &cfg); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// web_ui is opt-in: absent or false keeps the bearer-only surface, and true
// turns it on. Defaults are applied before decoding, so an explicit value wins.
func TestAgentWebUIDefaultsOffAndCanBeEnabled(t *testing.T) {
	var off AgentConfig
	if err := Load(writeTemp(t, `{}`), &off); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if off.WebUI {
		t.Error("WebUI default = true, want false")
	}

	var explicitOff AgentConfig
	if err := Load(writeTemp(t, `{"schema_version":1,"web_ui":false}`), &explicitOff); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if explicitOff.WebUI {
		t.Error("WebUI = true for an explicit false")
	}

	var on AgentConfig
	if err := Load(writeTemp(t, `{"schema_version":1,"web_ui":true}`), &on); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !on.WebUI {
		t.Error("WebUI = false for an explicit true")
	}
}

// The client local UI is on by default with a loopback bind address.
func TestClientWebUIDefaults(t *testing.T) {
	var cfg ClientConfig
	if err := Load(writeTemp(t, `{}`), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.WebUI.Enabled {
		t.Error("WebUI.Enabled default = false, want true (on by default)")
	}
	if cfg.WebUI.Listen != DefaultWebUIListen {
		t.Errorf("WebUI.Listen = %q, want %q", cfg.WebUI.Listen, DefaultWebUIListen)
	}
}

func TestClientWebUIExplicitValues(t *testing.T) {
	body := `{"schema_version":1,"web_ui":{"enabled":false,"listen":"127.0.0.1:8080"}}`
	var cfg ClientConfig
	if err := Load(writeTemp(t, body), &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WebUI.Enabled {
		t.Error("WebUI.Enabled = true for an explicit false")
	}
	if cfg.WebUI.Listen != "127.0.0.1:8080" {
		t.Errorf("WebUI.Listen = %q, want 127.0.0.1:8080", cfg.WebUI.Listen)
	}
}

// A local UI bind address that is not loopback must be rejected, so a
// misconfiguration cannot expose the UI beyond the machine.
func TestClientWebUIRejectsNonLoopbackListen(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:0", "192.168.1.5:3242", "localhost:0", ":0", "127.0.0.1"} {
		body := `{"schema_version":1,"web_ui":{"listen":"` + listen + `"}}`
		var cfg ClientConfig
		err := Load(writeTemp(t, body), &cfg)
		if err == nil {
			t.Errorf("Load(web_ui.listen=%q) succeeded, want a loopback error", listen)
			continue
		}
		if !strings.Contains(err.Error(), "web_ui.listen") {
			t.Errorf("error %q does not name web_ui.listen", err)
		}
	}
}
