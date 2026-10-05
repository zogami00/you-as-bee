package main

import "testing"

// B3: Raspberry Pi OS reports the onboard Bluetooth driver as hci_uart_bcm, so
// the doctor check must accept the hci_uart prefix instead of an exact match.
// A USB adapter's btusb must still fail: the whole premise is that onboard BT
// is on UART, which is why blacklisting btusb is safe.
func TestOnboardBluetoothOnUART(t *testing.T) {
	cases := []struct {
		driver string
		want   bool
	}{
		{"hci_uart", true},
		{"hci_uart_bcm", true},
		{"btusb", false},
		{"", false},
		{"hci_vhci", false},
	}
	for _, tc := range cases {
		if got := onboardBluetoothOnUART(tc.driver); got != tc.want {
			t.Errorf("onboardBluetoothOnUART(%q) = %v, want %v", tc.driver, got, tc.want)
		}
	}
}
