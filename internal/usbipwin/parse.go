package usbipwin

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrUnrecognised is returned when usbip output matches nothing the parser
// understands. It is deliberately distinct from an empty list on a recognised
// empty result.
var ErrUnrecognised = errors.New("usbipwin: unrecognised usbip output")

// PortEntry is one entry from `usbip port`: a local vhci port bound to a remote
// device.
type PortEntry struct {
	// Port is the local vhci port number, e.g. 0 for "Port 00:".
	Port int
	// Host is the remote host from the usbip:// URI.
	Host string
	// RemotePort is the remote TCP port from the usbip:// URI (normally 3240).
	RemotePort int
	// BusID is the remote bus id, e.g. "1-1.4".
	BusID string
	// VID and PID are 4-hex-digit lower-case identifiers when present.
	VID string
	PID string
}

// RemoteDevice is one entry from `usbip list -r <host>`.
type RemoteDevice struct {
	BusID   string
	VID     string
	PID     string
	Product string
}

var (
	portBlockRe = regexp.MustCompile(`^Port\s+(\d+):`)
	// portURLRe accepts both a plain host and a bracketed IPv6 literal, e.g.
	// usbip://192.168.1.42:3240/1-1.4 and usbip://[fe80::1]:3240/1-1.4.
	portURLRe = regexp.MustCompile(`usbip://(\[[^\]]+\]|[^/:]+):(\d+)/(\S+)`)
	vidpidRe  = regexp.MustCompile(`\(([0-9a-fA-F]{4}):([0-9a-fA-F]{4})\)`)

	remoteBusRe = regexp.MustCompile(`^\s*([0-9]+-[0-9]+(?:\.[0-9]+)*):\s*(.*)$`)
)

// emptyMarkers are lines that prove usbip produced output but there is nothing
// to report. Without one of them an empty parse is treated as unrecognised
// rather than as success.
var emptyMarkers = []string{
	"imported usb devices",
	"exportable usb devices",
	"no exported devices",
	"no imported devices",
	"no exportable devices",
}

func hasMarker(out string) bool {
	lower := strings.ToLower(out)
	for _, m := range emptyMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// ParsePort parses the output of `usbip port`. Each `Port N:` line starts a
// block; within a block the usbip:// URI yields host, remote port and bus id,
// and a 4:4 hex pair yields VID/PID. Unknown lines are skipped, never fatal.
//
// Output with no port blocks (a clean machine, or a recognised empty header)
// returns an empty, non-nil slice and a nil error.
func ParsePort(out string) ([]PortEntry, error) {
	entries := []PortEntry{}
	var cur *PortEntry

	for _, line := range strings.Split(out, "\n") {
		if m := portBlockRe.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			entries = append(entries, PortEntry{Port: n})
			cur = &entries[len(entries)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := portURLRe.FindStringSubmatch(line); m != nil {
			cur.Host = unbracketHost(m[1])
			if n, err := strconv.Atoi(m[2]); err == nil {
				cur.RemotePort = n
			}
			cur.BusID = m[3]
		}
		if m := vidpidRe.FindStringSubmatch(line); m != nil {
			cur.VID = strings.ToLower(m[1])
			cur.PID = strings.ToLower(m[2])
		}
	}

	if len(entries) == 0 {
		// `usbip port` exits zero with no port blocks when nothing is attached.
		// That is a successful empty result, not a parse failure: a clean
		// machine must not be treated as "usbip is broken" and backed off
		// forever. (Failures to run usbip surface as an error from Tool.Port
		// before parsing.)
		return entries, nil
	}
	return entries, nil
}

// unbracketHost strips the square brackets from an IPv6 literal in a usbip
// URI so host matching compares the same text the config uses.
func unbracketHost(host string) string {
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		return host[1 : len(host)-1]
	}
	return host
}

// ParseRemote parses the output of `usbip list -r <host>`. Each line of the
// form `1-1.4: Vendor Product (045e:02ea)` yields one device. Unknown lines are
// skipped. As with ParsePort, an empty result is only accepted when the output
// contains a recognised header.
func ParseRemote(out string) ([]RemoteDevice, error) {
	devices := []RemoteDevice{}
	for _, line := range strings.Split(out, "\n") {
		m := remoteBusRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		d := RemoteDevice{BusID: m[1]}
		rest := strings.TrimSpace(m[2])
		if vm := vidpidRe.FindStringSubmatch(rest); vm != nil {
			d.VID = strings.ToLower(vm[1])
			d.PID = strings.ToLower(vm[2])
			rest = strings.TrimSpace(vidpidRe.ReplaceAllString(rest, ""))
			rest = strings.Trim(rest, ": ")
		}
		d.Product = strings.TrimSpace(rest)
		devices = append(devices, d)
	}

	if len(devices) == 0 {
		if hasMarker(out) {
			return devices, nil
		}
		return nil, fmt.Errorf("%w: usbip list -r produced no devices", ErrUnrecognised)
	}
	return devices, nil
}
