package usbipwin

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"0.9.8.0", Version{0, 9, 8, 0}, true},
		{"usbip-win2 0.9.8.0", Version{0, 9, 8, 0}, true},
		{"usbip-win2 0.9.7.7 (usbip 1.1.1)", Version{0, 9, 7, 7}, true},
		{"usbip-win2 v1.2.3 build 4", Version{1, 2, 3, 0}, true},
		{"release 0.9.8", Version{0, 9, 8, 0}, true},
		{"", Version{}, false},
		{"no version here", Version{}, false},
		{"0.9", Version{}, false},
	}
	for _, tc := range cases {
		got, ok := ParseVersion(tc.in)
		if ok != tc.ok {
			t.Errorf("ParseVersion(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	min := MinimumVersion // 0.9.8.0
	cases := []struct {
		in   Version
		want bool
	}{
		{Version{0, 9, 8, 0}, true},
		{Version{0, 9, 8, 1}, true},
		{Version{0, 9, 9, 0}, true},
		{Version{0, 10, 0, 0}, true},
		{Version{1, 0, 0, 0}, true},
		{Version{0, 9, 7, 7}, false}, // has --once but not --receive-mode
		{Version{0, 8, 99, 99}, false},
		{Version{0, 9, 7, 99}, false}, // build must not rescue a lower patch
	}
	for _, tc := range cases {
		if got := tc.in.AtLeast(min); got != tc.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tc.in, min, got, tc.want)
		}
	}
}
