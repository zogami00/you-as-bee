//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// B6: an access-denied read of the token-bearing config must produce actionable
// guidance instead of a raw "Access is denied", while other errors pass through
// unchanged.
func TestConfigReadErrorPermissionIsActionable(t *testing.T) {
	path := `C:\ProgramData\you-as-bee\client.json`
	err := configReadError(path, fmt.Errorf("open %s: %w", path, fs.ErrPermission))
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	for _, want := range []string{path, "run from an elevated prompt", "API token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestConfigReadErrorOtherPassesThrough(t *testing.T) {
	base := errors.New("syntax error")
	if got := configReadError("client.json", base); got != base {
		t.Fatalf("configReadError = %v, want the original error", got)
	}
}
