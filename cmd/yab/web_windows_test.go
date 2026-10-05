//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/config"
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
