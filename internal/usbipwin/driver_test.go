package usbipwin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zogami00/you-as-bee/internal/execx"
)

func pnputilPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "pnputil.exe")
}

func TestClassifyFailureZeroIsNil(t *testing.T) {
	if err := ClassifyFailure(context.Background(), execx.NewFakeRunner(), "usbip.exe", 0, ""); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestClassifyFailureUnrelated(t *testing.T) {
	err := ClassifyFailure(context.Background(), execx.NewFakeRunner(), "usbip.exe", 1, "some other problem")
	if err == nil || errors.Is(err, ErrDriverMissing) || errors.Is(err, ErrDriverBlocked) {
		t.Fatalf("want a generic error, got %v", err)
	}
}

func TestClassifyFailureDriverMissing(t *testing.T) {
	fr := execx.NewFakeRunner().OnName(pnputilPath(), execx.FakeResponse{
		Stdout: "Instance ID:                USB\\VID_1234&PID_5678\r\n" +
			"Device Description:         Some other device\r\n" +
			"Problem Code:               28\r\n\r\n",
	})
	err := ClassifyFailure(context.Background(), fr, "usbip.exe", 1, "cannot open the vhci driver")
	if !errors.Is(err, ErrDriverMissing) {
		t.Fatalf("want ErrDriverMissing, got %v", err)
	}
}

func TestClassifyFailureDriverBlocked(t *testing.T) {
	fr := execx.NewFakeRunner().OnName(pnputilPath(), execx.FakeResponse{
		Stdout: "Instance ID:                ROOT\\USBIP\\0000\r\n" +
			"Device Description:         USB/IP VHCI Driver\r\n" +
			"Problem Code:               52 (CM_PROB_UNSIGNED_DRIVER)\r\n\r\n",
	})
	err := ClassifyFailure(context.Background(), fr, "usbip.exe", 1, "vhci: error")
	if !errors.Is(err, ErrDriverBlocked) {
		t.Fatalf("want ErrDriverBlocked, got %v", err)
	}
}

func TestHasUSBIPProblem52IgnoresOtherDevices(t *testing.T) {
	out := "Instance ID:                PCI\\VEN_1234\r\n" +
		"Device Description:         Network controller\r\n" +
		"Problem Code:               52\r\n\r\n" +
		"Instance ID:                ROOT\\USBIP\\0000\r\n" +
		"Device Description:         USB/IP VHCI Driver\r\n" +
		"Problem Code:               0\r\n\r\n"
	if hasUSBIPProblem52(out) {
		t.Fatal("want false: the usbip device has no problem and the problem device is not usbip")
	}
}

func TestParsePortViaTool(t *testing.T) {
	fr := execx.NewFakeRunner().OnName("usbip.exe", execx.FakeResponse{
		Stdout: readFixture(t, "port_one.txt"),
	})
	tool := New("usbip.exe", fr)
	entries, err := tool.Port(context.Background())
	if err != nil {
		t.Fatalf("Port: %v", err)
	}
	if len(entries) != 1 || entries[0].BusID != "1-1.4" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}
