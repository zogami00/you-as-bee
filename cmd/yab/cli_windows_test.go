//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/zogami00/you-as-bee/internal/usbipwin"
)

// usbipVersionWarning is advisory only: doctor must never fail on it. It warns
// when the detected usbip.exe is older than the minimum that understands
// --receive-mode, and when the version cannot be parsed at all.
func TestUsbipVersionWarning(t *testing.T) {
	cases := []struct {
		name        string
		version     string
		wantWarning bool
		wantSubstr  string
	}{
		{
			name:        "older version warns",
			version:     "usbip-win2 0.9.7.7 (usbip 1.1.1)",
			wantWarning: true,
			wantSubstr:  "older than",
		},
		{
			name:        "equal version has no warning",
			version:     "usbip-win2 " + usbipwin.MinimumVersion.String(),
			wantWarning: false,
		},
		{
			name:        "newer version has no warning",
			version:     "usbip-win2 1.0.0.0",
			wantWarning: false,
		},
		{
			name:        "unparseable version warns it could not be determined",
			version:     "usbip version unknown",
			wantWarning: true,
			wantSubstr:  "could not determine",
		},
		{
			name:        "empty version warns it could not be determined",
			version:     "",
			wantWarning: true,
			wantSubstr:  "could not determine",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usbipVersionWarning(tc.version)
			if tc.wantWarning && got == "" {
				t.Fatalf("usbipVersionWarning(%q) = empty, want a warning", tc.version)
			}
			if !tc.wantWarning && got != "" {
				t.Fatalf("usbipVersionWarning(%q) = %q, want no warning", tc.version, got)
			}
			if tc.wantSubstr != "" && !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("usbipVersionWarning(%q) = %q, want it to contain %q", tc.version, got, tc.wantSubstr)
			}
		})
	}
}
