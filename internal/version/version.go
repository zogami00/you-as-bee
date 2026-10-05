// Package version carries build-time version metadata for both binaries.
//
// The variables are intended to be overridden at link time with:
//
//	go build -ldflags "-X github.com/zogami00/you-as-bee/internal/version.Version=1.2.3 \
//	                   -X github.com/zogami00/you-as-bee/internal/version.Commit=abc1234 \
//	                   -X github.com/zogami00/you-as-bee/internal/version.Date=2026-10-05T00:00:00Z"
package version

import "fmt"

// Build metadata. The defaults describe an un-stamped development build.
var (
	// Version is the human-facing release version.
	Version = "dev"
	// Commit is the short source revision the binary was built from.
	Commit = "none"
	// Date is the build timestamp (RFC 3339 recommended).
	Date = "unknown"
)

// String returns a single-line description of the build.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
