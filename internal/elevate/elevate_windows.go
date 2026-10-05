//go:build windows

// Package elevate reports whether the current process is running elevated and
// can relaunch it through the UAC "runas" verb.
package elevate

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsElevated reports whether the current process token is elevated from a UAC
// perspective. It uses the token elevation type rather than group membership,
// so a member of the Administrators group running unelevated still reports
// false.
func IsElevated() bool {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

// shellExecuteW is resolved lazily from the system DLL so no import library is
// needed and the build stays cgo-free.
var shellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

// RelaunchElevated re-launches the current executable with args using the
// ShellExecuteW "runas" verb, which prompts for UAC consent. It returns once
// the elevated process has been launched; it does not wait for it.
func RelaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("elevate: locate executable: %w", err)
	}

	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return fmt.Errorf("elevate: verb: %w", err)
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return fmt.Errorf("elevate: executable: %w", err)
	}
	params, err := windows.UTF16PtrFromString(quoteArgs(args))
	if err != nil {
		return fmt.Errorf("elevate: parameters: %w", err)
	}

	const swShowNormal = 1
	ret, _, callErr := shellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		0,
		swShowNormal,
	)
	// ShellExecuteW returns a value greater than 32 on success.
	if ret <= 32 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return fmt.Errorf("elevate: ShellExecuteW runas failed (code %d): %w", ret, callErr)
		}
		return fmt.Errorf("elevate: ShellExecuteW runas failed (code %d)", ret)
	}
	return nil
}

// quoteArgs renders an argument vector as a Windows command line using the
// CommandLineToArgvW rules.
func quoteArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// quoteArg quotes one argument for CommandLineToArgvW.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			backslashes++
		case '"':
			// Escape the backslashes that precede a quote, then the quote.
			for i := 0; i < backslashes*2+1; i++ {
				b.WriteByte('\\')
			}
			b.WriteByte('"')
			backslashes = 0
		default:
			for i := 0; i < backslashes; i++ {
				b.WriteByte('\\')
			}
			backslashes = 0
			b.WriteRune(r)
		}
	}
	// Trailing backslashes must be doubled so they do not escape the closing
	// quote.
	for i := 0; i < backslashes*2; i++ {
		b.WriteByte('\\')
	}
	b.WriteByte('"')
	return b.String()
}
