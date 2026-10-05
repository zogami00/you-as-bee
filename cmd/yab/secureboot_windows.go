//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// secureBoot reports the UEFI Secure Boot state from the firmware state key.
// The registry is read directly (the equivalent of the Confirm-SecureBootUEFI
// cmdlet) so no PowerShell shell is invoked. It only observes.
func secureBoot() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\SecureBoot\State`, registry.QUERY_VALUE)
	if err != nil {
		return "unknown"
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("UEFISecureBootEnabled")
	if err != nil {
		return "unknown"
	}
	if v != 0 {
		return "enabled"
	}
	return "disabled"
}
