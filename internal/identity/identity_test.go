package identity

import (
	"testing"

	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/sysfs"
)

func TestKeyWithAndWithoutSerial(t *testing.T) {
	withSerial := sysfs.Device{VID: "045E", PID: "02E6", Serial: "ABC", BusID: "1-1.4"}
	if got, want := Key(withSerial), "045e:02e6:ABC"; got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}
	without := sysfs.Device{VID: "0A12", PID: "0001", BusID: "1-1.2"}
	if got, want := Key(without), "0a12:0001@1-1.2"; got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}
}

func TestMatchSerial(t *testing.T) {
	dev := sysfs.Device{VID: "045e", PID: "02e6", Serial: "ABC", BusID: "1-1.4"}
	cases := []struct {
		name string
		pin  config.DeviceConfig
		want bool
	}{
		{"vid pid only", config.DeviceConfig{VID: "045e", PID: "02e6"}, true},
		{"serial matches", config.DeviceConfig{VID: "045e", PID: "02e6", Serial: "ABC"}, true},
		{"serial differs", config.DeviceConfig{VID: "045e", PID: "02e6", Serial: "XYZ"}, false},
		{"port matches", config.DeviceConfig{VID: "045e", PID: "02e6", Port: "1-1.4"}, true},
		{"port differs", config.DeviceConfig{VID: "045e", PID: "02e6", Port: "1-1.9"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.pin, dev); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchCaseInsensitiveVIDPID(t *testing.T) {
	dev := sysfs.Device{VID: "0a12", PID: "0001", BusID: "1-1.2"}
	pin := config.DeviceConfig{VID: "0A12", PID: "0001"}
	if !Match(pin, dev) {
		t.Error("Match = false, want case-insensitive VID match")
	}
}

func TestResolveAmbiguityAndAbsent(t *testing.T) {
	pins := []config.DeviceConfig{
		{Name: "bt", VID: "0a12", PID: "0001"},
		{Name: "xbox", VID: "045e", PID: "02e6", Serial: "ABC"},
		{Name: "ghost", VID: "ffff", PID: "ffff"},
	}
	devs := []sysfs.Device{
		{VID: "0a12", PID: "0001", BusID: "1-1.2"},
		{VID: "0a12", PID: "0001", BusID: "1-1.3"},
		{VID: "045e", PID: "02e6", Serial: "ABC", BusID: "1-1.4"},
	}

	resolved, amb, err := Resolve(pins, devs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := resolved["bt"]; ok {
		t.Error("bt should be ambiguous, not resolved")
	}
	if resolved["xbox"].BusID != "1-1.4" {
		t.Errorf("xbox = %+v, want busid 1-1.4", resolved["xbox"])
	}
	if _, ok := resolved["ghost"]; ok {
		t.Error("ghost should be absent")
	}
	if len(amb) != 1 || amb[0].Pin != "bt" || len(amb[0].Devices) != 2 {
		t.Fatalf("ambiguities = %+v, want one for bt with two devices", amb)
	}
}

func TestResolvePortNarrowing(t *testing.T) {
	pins := []config.DeviceConfig{
		{Name: "bt", VID: "0a12", PID: "0001", Port: "1-1.3"},
	}
	devs := []sysfs.Device{
		{VID: "0a12", PID: "0001", BusID: "1-1.2"},
		{VID: "0a12", PID: "0001", BusID: "1-1.3"},
	}
	resolved, amb, err := Resolve(pins, devs)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(amb) != 0 {
		t.Fatalf("ambiguities = %+v, want none", amb)
	}
	if resolved["bt"].BusID != "1-1.3" {
		t.Errorf("bt = %+v, want busid 1-1.3", resolved["bt"])
	}
}
