package webui_test

import (
	_ "embed"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/zogami00/you-as-bee/internal/agent"
	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/proto"
)

// appJS is the shipped client that renders the badges. This is an external test
// package so it can import the state sources (client, agent, proto) without an
// import cycle: internal/api imports webui, but the external test binary may
// import a package that does.
//
//go:embed assets/app.js
var appJS []byte

// stateClassKeys extracts the keys of the JS STATE_CLASS map.
func stateClassKeys(t *testing.T) map[string]bool {
	t.Helper()
	src := string(appJS)
	const marker = "STATE_CLASS = {"
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatal("STATE_CLASS map not found in app.js")
	}
	rest := src[start+len(marker):]
	end := strings.Index(rest, "};")
	if end < 0 {
		t.Fatal("end of the STATE_CLASS map not found in app.js")
	}
	re := regexp.MustCompile(`(?m)^\s*([a-z_]+)\s*:`)
	keys := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(rest[:end], -1) {
		keys[m[1]] = true
	}
	if len(keys) == 0 {
		t.Fatal("parsed zero STATE_CLASS keys; the map format changed")
	}
	return keys
}

// goStates is every state string the Go side can emit to the browser:
//   - the Windows client supervisor's pin states (Backend.Status),
//   - the agent's management API device states (proto.State*),
//   - the agent reconciler's own states (agent.State.String), kept so a future
//     change that exposes them cannot ship unstyled.
func goStates() []string {
	set := make(map[string]bool)
	for _, s := range []string{
		client.StateIdle, client.StateAbsent, client.StateAttached,
		client.StateBackoff, client.StatePaused, client.StateNetworkDown,
	} {
		set[s] = true
	}
	for _, s := range []string{
		proto.StateUnexported, proto.StateExported, proto.StateInUse,
		proto.StateAbsent, proto.StateError,
	} {
		set[s] = true
	}
	for st := agent.Absent; st <= agent.Quarantined; st++ {
		set[st.String()] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestAppJSStateClassCoversEveryGoState ensures every state the Go side can emit
// has a badge class. Without this, a new state silently renders as "unknown"
// and the UI's primary function (showing pin state) is broken.
func TestAppJSStateClassCoversEveryGoState(t *testing.T) {
	keys := stateClassKeys(t)
	for _, state := range goStates() {
		if !keys[state] {
			t.Errorf("STATE_CLASS in app.js has no entry for state %q; it would render as \"unknown\"", state)
		}
	}
}
