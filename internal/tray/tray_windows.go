//go:build windows

package tray

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconData []byte

func run(ctx context.Context, c Controller) {
	systray.Run(func() { onReady(ctx, c) }, func() {})
}

func onReady(ctx context.Context, c Controller) {
	systray.SetIcon(iconData)
	systray.SetTitle("you-as-bee")
	systray.SetTooltip("you-as-bee USB-over-LAN")

	status := systray.AddMenuItem("starting...", "")
	status.Disable()
	systray.AddSeparator()

	pins := make([]string, 0, len(c.Devices()))
	toggles := make([]*systray.MenuItem, 0)
	lines := make([]*systray.MenuItem, 0)

	for _, d := range c.Devices() {
		parent := systray.AddMenuItem(fmt.Sprintf("%s (%s)", d.Name, d.Status), "")
		line := parent.AddSubMenuItem("status: "+d.Status, "")
		line.Disable()
		toggle := parent.AddSubMenuItem(toggleLabel(d), "")
		pins = append(pins, d.Pin)
		toggles = append(toggles, toggle)
		lines = append(lines, line)
		go watchToggle(ctx, c, status, d.Pin, toggle)
	}

	if !c.Elevated() {
		systray.AddSeparator()
		elevateItem := systray.AddMenuItem("Restart as administrator", "")
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-elevateItem.ClickedCh:
					if err := c.RestartElevated(); err != nil {
						status.SetTitle("restart failed: " + err.Error())
					}
				}
			}
		}()
	}

	quit := systray.AddMenuItem("Quit", "")
	go func() {
		select {
		case <-ctx.Done():
		case <-quit.ClickedCh:
		}
		systray.Quit()
	}()

	go refresh(ctx, c, status, pins, toggles, lines)
}

// watchToggle handles one device's attach/detach item, reading the live status
// from the controller on every click so it never acts on a stale view.
func watchToggle(ctx context.Context, c Controller, status *systray.MenuItem, pin string, toggle *systray.MenuItem) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-toggle.ClickedCh:
			attached := false
			for _, d := range c.Devices() {
				if d.Pin == pin {
					attached = d.Attached
				}
			}
			var err error
			if attached {
				err = c.Detach(pin)
			} else {
				err = c.Attach(pin)
			}
			if err != nil {
				status.SetTitle("error: " + err.Error())
			}
		}
	}
}

func refresh(ctx context.Context, c Controller, status *systray.MenuItem, pins []string, toggles, lines []*systray.MenuItem) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			byPin := make(map[string]Device)
			for _, d := range c.Devices() {
				byPin[d.Pin] = d
			}
			for i, pin := range pins {
				d, ok := byPin[pin]
				if !ok {
					continue
				}
				lines[i].SetTitle("status: " + d.Status)
				toggles[i].SetTitle(toggleLabel(d))
			}
		}
	}
}

func toggleLabel(d Device) string {
	if d.Attached {
		return "Detach " + d.Pin
	}
	return "Attach " + d.Pin
}
