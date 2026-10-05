package webui

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/zogami00/you-as-bee/internal/proto"
)

// LogRingCapacity is the number of records the ring retains.
const LogRingCapacity = 1000

// Logs endpoint bounds. The API clamps `limit` into this range.
const (
	DefaultLogsLimit = 100
	MaxLogsLimit     = 500
)

// ringState is the shared storage behind a LogRing and its derived handlers.
type ringState struct {
	mu      sync.Mutex
	entries []proto.LogEntry
	start   int
	count   int
	next    uint64
}

// LogRing is a slog.Handler that keeps the most recent records in memory. It
// accepts info and above; debug records are dropped. It is safe for concurrent
// use.
type LogRing struct {
	state  *ringState
	level  slog.Level
	attrs  []slog.Attr
	groups []string
}

// NewLogRing returns an empty ring accepting info and above.
func NewLogRing() *LogRing {
	return &LogRing{
		state: &ringState{entries: make([]proto.LogEntry, LogRingCapacity)},
		level: slog.LevelInfo,
	}
}

// Enabled implements slog.Handler.
func (h *LogRing) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle implements slog.Handler.
func (h *LogRing) Handle(_ context.Context, r slog.Record) error {
	if r.Level < h.level {
		return nil
	}
	attrs := make(map[string]string, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		appendAttr(attrs, h.groups, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(attrs, h.groups, a)
		return true
	})

	entry := proto.LogEntry{
		Time:  r.Time,
		Level: levelString(r.Level),
		Msg:   r.Message,
	}
	if len(attrs) > 0 {
		entry.Attrs = attrs
	}

	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	h.state.next++
	entry.Seq = h.state.next
	size := len(h.state.entries)
	if h.state.count < size {
		h.state.entries[(h.state.start+h.state.count)%size] = entry
		h.state.count++
		return nil
	}
	// Full: overwrite the oldest record and advance the window.
	h.state.entries[h.state.start] = entry
	h.state.start = (h.state.start + 1) % size
	return nil
}

// WithAttrs implements slog.Handler.
func (h *LogRing) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

// WithGroup implements slog.Handler.
func (h *LogRing) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

// Entries returns log records with a sequence number greater than after, oldest
// first, at most limit of them. Next is the sequence number to pass as the
// following `after` value; when no record is returned it equals after.
func (h *LogRing) Entries(after uint64, limit int) proto.LogsResponse {
	if limit <= 0 {
		limit = DefaultLogsLimit
	}
	if limit > MaxLogsLimit {
		limit = MaxLogsLimit
	}

	h.state.mu.Lock()
	defer h.state.mu.Unlock()

	size := len(h.state.entries)
	entries := make([]proto.LogEntry, 0, limit)
	next := after
	for i := 0; i < h.state.count; i++ {
		e := h.state.entries[(h.state.start+i)%size]
		if e.Seq <= after {
			continue
		}
		if len(entries) == limit {
			break
		}
		entries = append(entries, e)
		next = e.Seq
	}
	return proto.LogsResponse{Entries: entries, Next: next}
}

// appendAttr flattens a slog attribute into dst. Group attributes recurse and
// their keys are prefixed with the active group path joined by ".".
func appendAttr(dst map[string]string, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		nested := append(append([]string(nil), groups...), a.Key)
		for _, sub := range a.Value.Group() {
			appendAttr(dst, nested, sub)
		}
		return
	}
	key := a.Key
	if key == "" {
		key = "attr"
	}
	if len(groups) > 0 {
		key = strings.Join(append(append([]string(nil), groups...), key), ".")
	}
	dst[key] = a.Value.String()
}

// levelString renders a level in lower case, matching the API's field style.
func levelString(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}
