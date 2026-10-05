package usbipwin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zogami00/you-as-bee/internal/execx"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func TestParsePortOne(t *testing.T) {
	entries, err := ParsePort(readFixture(t, "port_one.txt"))
	if err != nil {
		t.Fatalf("ParsePort: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	got := entries[0]
	want := PortEntry{Port: 0, Host: "192.168.1.42", RemotePort: 3240, BusID: "1-1.4", VID: "045e", PID: "02ea"}
	if got != want {
		t.Fatalf("entry mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestParsePortTwo(t *testing.T) {
	entries, err := ParsePort(readFixture(t, "port_two.txt"))
	if err != nil {
		t.Fatalf("ParsePort: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Port != 0 || entries[0].BusID != "1-1.4" || entries[0].VID != "045e" {
		t.Errorf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].Port != 1 || entries[1].BusID != "1-2" || entries[1].VID != "0bda" || entries[1].PID != "c820" {
		t.Errorf("unexpected second entry: %+v", entries[1])
	}
}

func TestParsePortEmptyIsRecognised(t *testing.T) {
	entries, err := ParsePort(readFixture(t, "port_empty.txt"))
	if err != nil {
		t.Fatalf("ParsePort(empty): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %+v", entries)
	}
	if entries == nil {
		t.Fatal("want non-nil empty slice")
	}
}

// `usbip port` exits zero with no port blocks when nothing is attached; that
// must be a successful empty result, not a parse failure that makes the
// supervisor back off forever on a clean machine.
func TestParsePortNoBlocksIsEmptySuccess(t *testing.T) {
	entries, err := ParsePort(readFixture(t, "port_unrecognised.txt"))
	if err != nil {
		t.Fatalf("ParsePort(no blocks): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %+v", entries)
	}
	if entries == nil {
		t.Fatal("want non-nil empty slice")
	}
}

func TestParsePortTrulyEmpty(t *testing.T) {
	entries, err := ParsePort("   \n\n")
	if err != nil {
		t.Fatalf("want empty success for blank output, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want 0 entries, got %+v", entries)
	}
}

func TestParsePortBracketedIPv6Host(t *testing.T) {
	out := "Imported USB devices\n" +
		"Port 00: <Port in Use>\n" +
		"       3-1 -> usbip://[fe80::1]:3240/1-1.4\n"
	entries, err := ParsePort(out)
	if err != nil {
		t.Fatalf("ParsePort: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", entries)
	}
	if got := entries[0]; got.Host != "fe80::1" || got.BusID != "1-1.4" || got.RemotePort != 3240 {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestParseRemoteList(t *testing.T) {
	devices, err := ParseRemote(readFixture(t, "remote_list.txt"))
	if err != nil {
		t.Fatalf("ParseRemote: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("want 2 devices, got %d: %+v", len(devices), devices)
	}
	first := devices[0]
	if first.BusID != "1-1.4" || first.VID != "045e" || first.PID != "02ea" {
		t.Errorf("unexpected first device: %+v", first)
	}
	if first.Product != "Microsoft Corp. : Xbox Wireless Controller" {
		t.Errorf("unexpected product %q", first.Product)
	}
	if devices[1].BusID != "1-2" || devices[1].VID != "0bda" || devices[1].PID != "c820" {
		t.Errorf("unexpected second device: %+v", devices[1])
	}
}

func TestParseRemoteUnrecognised(t *testing.T) {
	if _, err := ParseRemote("nothing here\n"); !errors.Is(err, ErrUnrecognised) {
		t.Fatalf("want ErrUnrecognised, got %v", err)
	}
}

func TestAttachRejectsInvalidBusIDBeforeRunning(t *testing.T) {
	for _, busid := range []string{"", "abc", "1", "-1-2", "1-2-3", "1-2/3", "1-2.3.4.x"} {
		fr := execx.NewFakeRunner()
		tool := New(`C:\fake\usbip.exe`, fr)
		err := tool.Attach(context.Background(), "192.168.1.42", busid)
		if !errors.Is(err, ErrInvalidBusID) {
			t.Errorf("busid %q: want ErrInvalidBusID, got %v", busid, err)
		}
		if fr.CallCount() != 0 {
			t.Errorf("busid %q: runner was invoked %d times", busid, fr.CallCount())
		}
	}
}

// B7/B9: attach must pass --once (or the driver starts its own endless retry
// loop) and --receive-mode low-latency (the default when none is set).
func TestAttachPassesArgumentVector(t *testing.T) {
	fr := execx.NewFakeRunner()
	tool := New(`C:\fake\usbip.exe`, fr)
	if err := tool.Attach(context.Background(), "192.168.1.42", "1-1.4"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	got := calls[0]
	if got.Name != `C:\fake\usbip.exe` {
		t.Errorf("name = %q", got.Name)
	}
	want := []string{"attach", "-r", "192.168.1.42", "-b", "1-1.4", "--once", "--receive-mode", "low-latency"}
	if len(got.Args) != len(want) {
		t.Fatalf("args = %v, want %v", got.Args, want)
	}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", got.Args, want)
		}
	}
}

// B9: a configured receive mode is passed through verbatim.
func TestAttachHonoursConfiguredReceiveMode(t *testing.T) {
	fr := execx.NewFakeRunner()
	tool := New(`C:\fake\usbip.exe`, fr)
	tool.ReceiveMode = "zero-copy"
	if err := tool.Attach(context.Background(), "192.168.1.42", "1-1.4"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	calls := fr.Calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	want := []string{"attach", "-r", "192.168.1.42", "-b", "1-1.4", "--once", "--receive-mode", "zero-copy"}
	got := calls[0].Args
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

// B7: detach must also stop any lingering automatic attach attempt the driver
// may have started before --once was added.
func TestDetachStopsLingeringAttachAttempts(t *testing.T) {
	fr := execx.NewFakeRunner()
	tool := New(`C:\fake\usbip.exe`, fr)
	if err := tool.Detach(context.Background(), 3); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	calls := fr.Calls()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls (detach, attach --stop-all), got %d: %+v", len(calls), calls)
	}
	assertArgs(t, calls[0].Args, []string{"detach", "-p", "3"})
	assertArgs(t, calls[1].Args, []string{"attach", "--stop-all"})
}

func TestDetachRejectsNegativePortBeforeRunning(t *testing.T) {
	fr := execx.NewFakeRunner()
	tool := New(`C:\fake\usbip.exe`, fr)
	if err := tool.Detach(context.Background(), -1); err == nil {
		t.Fatal("want an error for a negative port")
	}
	if fr.CallCount() != 0 {
		t.Fatalf("runner invoked %d times", fr.CallCount())
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
}

func TestBusIDRegex(t *testing.T) {
	valid := []string{"1-1", "1-1.4", "2-3.1.7", "10-20"}
	invalid := []string{"", "1", "abc", "1-", "-1", "1-2-3", "1-2.", "0x1-2"}
	for _, s := range valid {
		if !busIDRe.MatchString(s) {
			t.Errorf("want %q valid", s)
		}
	}
	for _, s := range invalid {
		if busIDRe.MatchString(s) {
			t.Errorf("want %q invalid", s)
		}
	}
}
