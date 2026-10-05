//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/elevate"
	"github.com/zogami00/you-as-bee/internal/tray"
)

// cmdTray runs the supervisor and the system-tray UI together.
func cmdTray(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("tray")
	configPath := fs.String("config", defaultConfigPath(), "path to client config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "yab tray: %v\n", err)
		return 1
	}
	tool, err := locateTool(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "yab tray: %v\n", err)
		return 1
	}
	m, err := newManager(cfg, tool)
	if err != nil {
		fmt.Fprintf(stderr, "yab tray: %v\n", err)
		return 1
	}

	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	go func() { _ = m.Run(ctx) }()

	tray.Run(ctx, &trayController{m: m})
	return 0
}

// trayController adapts *client.Manager to tray.Controller.
type trayController struct{ m *client.Manager }

func (t *trayController) Devices() []tray.Device {
	statuses := t.m.Status()
	out := make([]tray.Device, 0, len(statuses))
	for _, ps := range statuses {
		out = append(out, tray.Device{
			Pin:      ps.Pin,
			Name:     ps.Pin,
			Status:   ps.State,
			Attached: ps.State == client.StateAttached,
			Paused:   ps.Paused,
		})
	}
	return out
}

func (t *trayController) Attach(pin string) error { return t.m.Resume(pin) }

func (t *trayController) Detach(pin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return t.m.Pause(ctx, pin)
}

func (t *trayController) Elevated() bool { return elevate.IsElevated() }

func (t *trayController) RestartElevated() error {
	return elevate.RelaunchElevated(os.Args[1:])
}
