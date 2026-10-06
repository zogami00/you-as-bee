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

	// st owns the one-off menu-action error. The periodic refresh owns the
	// base summary and renders both, so the line is never left stale on
	// "starting..." and an error is not silently overwritten a tick later.
	st := &statusState{}

	status := systray.AddMenuItem(summaryText(c.Devices()), "")
	status.Disable()
	systray.AddSeparator()

	openUI := systray.AddMenuItem("Open web UI", "")
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-openUI.ClickedCh:
				if err := c.OpenUI(); err != nil {
					st.fail(err)
				} else {
					st.clear()
				}
			}
		}
	}()

	pins := make([]string, 0, len(c.Devices()))
	toggles := make([]*systray.MenuItem, 0)
	lines := make([]*systray.MenuItem, 0)

	for _, d := range c.Devices() {
		parent := systray.AddMenuItem(fmt.Sprintf("%s (%s)", d.Name, d.Status), "")
		line := parent.AddSubMenuItem(statusText(d), "")
		line.Disable()
		toggle := parent.AddSubMenuItem(toggleLabel(d), "")
		pins = append(pins, d.Pin)
		toggles = append(toggles, toggle)
		lines = append(lines, line)
		go watchToggle(ctx, c, st, d.Pin, toggle)
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
						st.fail(err)
					} else {
						st.clear()
					}
				}
			}
		}()
	}

	quit := systray.AddMenuItem("Quit (devices stay attached)", "")
	go func() {
		select {
		case <-ctx.Done():
		case <-quit.ClickedCh:
		}
		systray.Quit()
	}()

	go refresh(ctx, c, status, st, pins, toggles, lines)
}

// watchToggle handles one device's attach/detach item, reading the live status
// from the controller on every click so it never acts on a stale view.
func watchToggle(ctx context.Context, c Controller, st *statusState, pin string, toggle *systray.MenuItem) {
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
				st.fail(err)
			} else {
				st.clear()
			}
		}
	}
}

// refresh owns the status line on every tick: it recomputes the summary from
// the live device states, so the line is always accurate and never stuck on its
// startup value, and renders any pending menu-action error beside it.
func refresh(ctx context.Context, c Controller, status *systray.MenuItem, st *statusState, pins []string, toggles, lines []*systray.MenuItem) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			devices := c.Devices()
			byPin := make(map[string]Device, len(devices))
			for _, d := range devices {
				byPin[d.Pin] = d
			}
			for i, pin := range pins {
				d, ok := byPin[pin]
				if !ok {
					continue
				}
				lines[i].SetTitle(statusText(d))
				toggles[i].SetTitle(toggleLabel(d))
			}
			status.SetTitle(st.line(summaryText(devices)))
		}
	}
}

func toggleLabel(d Device) string {
	if d.Attached {
		return "Detach " + d.Pin
	}
	return "Attach " + d.Pin
}

// statusText renders the live status line: state, and the pause reason or the
// last attach error when there is one. Both used to be dropped silently.
func statusText(d Device) string {
	s := "status: " + d.Status
	if d.Paused && d.PauseReason != "" {
		s += " (" + d.PauseReason + ")"
	}
	if d.LastError != "" {
		s += " - " + d.LastError
	}
	return s
}
