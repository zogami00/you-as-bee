//go:build !windows

// This file must not import fyne.io/systray so that non-Windows builds stay
// free of third-party dependencies.
package tray

import "context"

// run blocks until ctx is cancelled: there is no tray outside Windows.
func run(ctx context.Context, _ Controller) {
	<-ctx.Done()
}
