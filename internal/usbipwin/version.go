package usbipwin

import (
	"fmt"
	"regexp"
	"strconv"
)

// MinimumVersion is the oldest usbip-win2 release whose `usbip attach` accepts
// --receive-mode. v0.9.7.7 offers --once but not --receive-mode, so an attach
// on an older binary fails permanently with a generic "usbip exited N"; there
// is no way to ask Tool to omit the flag without regressing the stall fix.
var MinimumVersion = Version{Major: 0, Minor: 9, Patch: 8, Build: 0}

// versionRe matches a dotted numeric release with at least three components.
// It deliberately allows text around it so it can be run against arbitrary
// `usbip version` output.
var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?`)

// Version is a parsed usbip-win2 release. A missing build component is treated
// as zero.
type Version struct {
	Major int
	Minor int
	Patch int
	Build int
}

// String renders the version as "major.minor.patch.build".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", v.Major, v.Minor, v.Patch, v.Build)
}

// AtLeast reports whether v is greater than or equal to min.
func (v Version) AtLeast(min Version) bool {
	switch {
	case v.Major != min.Major:
		return v.Major > min.Major
	case v.Minor != min.Minor:
		return v.Minor > min.Minor
	case v.Patch != min.Patch:
		return v.Patch > min.Patch
	default:
		return v.Build >= min.Build
	}
}

// ParseVersion extracts a release number from `usbip version` (or similar)
// output. It returns ok=false when no dotted three-part number is present, so
// callers can warn instead of failing on unrecognised output.
//
// usbip-win2 prints its own release first, so the first dotted number in the
// text is the one used.
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	// The regex has exactly four groups; the first three always matched and the
	// build is optional.
	major, err1 := strconv.Atoi(m[1])
	minor, err2 := strconv.Atoi(m[2])
	patch, err3 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return Version{}, false
	}
	var build int
	if m[4] != "" {
		b, err := strconv.Atoi(m[4])
		if err != nil {
			return Version{}, false
		}
		build = b
	}
	return Version{Major: major, Minor: minor, Patch: patch, Build: build}, true
}
