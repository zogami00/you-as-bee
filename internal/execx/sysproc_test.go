package execx

import (
	"runtime"
	"testing"
)

// TestChildSysProcAttrPlatform asserts the attribute is platform-appropriate:
// set on Windows, nil elsewhere. It runs on every platform, so a Linux build
// proves the runner's behaviour is unchanged there.
func TestChildSysProcAttrPlatform(t *testing.T) {
	attr := childSysProcAttr()
	if runtime.GOOS == "windows" {
		if attr == nil {
			t.Fatal("childSysProcAttr() = nil on Windows, want non-nil")
		}
		return
	}
	if attr != nil {
		t.Fatalf("childSysProcAttr() = %+v on %s, want nil", attr, runtime.GOOS)
	}
}
