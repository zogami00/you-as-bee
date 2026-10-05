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

func TestParsePortUnrecognised(t *testing.T) {
	_, err := ParsePort(readFixture(t, "port_unrecognised.txt"))
	if !errors.Is(err, ErrUnrecognised) {
		t.Fatalf("want ErrUnrecognised, got %v", err)
	}
}

func TestParsePortTrulyEmpty(t *testing.T) {
	if _, err := ParsePort("   \n\n"); !errors.Is(err, ErrUnrecognised) {
		t.Fatalf("want ErrUnrecognised for blank output, got %v", err)
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
	want := []string{"attach", "-r", "192.168.1.42", "-b", "1-1.4"}
	if len(got.Args) != len(want) {
		t.Fatalf("args = %v, want %v", got.Args, want)
	}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", got.Args, want)
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
