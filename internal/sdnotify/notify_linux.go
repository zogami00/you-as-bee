//go:build linux

package sdnotify

import (
	"net"
	"os"
)

// Notify sends a single sd_notify state string to the socket named by
// NOTIFY_SOCKET. It is a no-op when NOTIFY_SOCKET is unset, which is the case
// when the process is not supervised by systemd.
func Notify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	// An abstract namespace socket is encoded with a leading '@' in the
	// environment and a leading NUL byte in the address.
	if socket[0] == '@' {
		socket = "\x00" + socket[1:]
	}
	conn, err := net.Dial("unixgram", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(state)); err != nil {
		return err
	}
	return nil
}
