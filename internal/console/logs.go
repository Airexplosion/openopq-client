package console

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type LogEntry struct {
	ID      uint64         `json:"id"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// LogBuffer is a bounded slog handler shared by the console and the CLI.
type LogBuffer struct {
	mu       sync.Mutex
	entries  []LogEntry
	sequence uint64
	level    slog.Level
}

func NewLogBuffer(level slog.Level) *LogBuffer                    { return &LogBuffer{level: level} }
func (b *LogBuffer) Enabled(_ context.Context, l slog.Level) bool { return l >= b.level }
func (b *LogBuffer) Handle(_ context.Context, r slog.Record) error {
	e := LogEntry{Time: r.Time, Level: r.Level.String(), Message: r.Message, Fields: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		value := a.Value.Resolve().Any()
		if err, ok := value.(error); ok {
			value = err.Error()
		}
		e.Fields[a.Key] = value
		return true
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sequence++
	e.ID = b.sequence
	if len(b.entries) == 1000 {
		copy(b.entries, b.entries[1:])
		b.entries = b.entries[:999]
	}
	b.entries = append(b.entries, e)
	return nil
}
func (b *LogBuffer) WithAttrs(a []slog.Attr) slog.Handler { return &attrHandler{base: b, attrs: a} }
func (b *LogBuffer) WithGroup(name string) slog.Handler   { return &attrHandler{base: b, group: name} }
func (b *LogBuffer) Since(id uint64) []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]LogEntry, 0)
	for _, e := range b.entries {
		if e.ID > id {
			out = append(out, e)
		}
	}
	return out
}

type attrHandler struct {
	base  slog.Handler
	attrs []slog.Attr
	group string
}

func (h *attrHandler) Enabled(c context.Context, l slog.Level) bool { return h.base.Enabled(c, l) }
func (h *attrHandler) Handle(c context.Context, r slog.Record) error {
	r = r.Clone()
	if h.group != "" {
		a := append([]slog.Attr(nil), h.attrs...)
		r.Attrs(func(v slog.Attr) bool { a = append(a, v); return true })
		r = slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		r.AddAttrs(slog.Attr{Key: h.group, Value: slog.GroupValue(a...)})
	} else {
		r.AddAttrs(h.attrs...)
	}
	return h.base.Handle(c, r)
}
func (h *attrHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &attrHandler{base: h, attrs: append([]slog.Attr(nil), a...)}
}
func (h *attrHandler) WithGroup(n string) slog.Handler { return &attrHandler{base: h, group: n} }

type multiHandler []slog.Handler

func MultiHandler(handlers ...slog.Handler) slog.Handler { return multiHandler(handlers) }
func (h multiHandler) Enabled(c context.Context, l slog.Level) bool {
	for _, v := range h {
		if v.Enabled(c, l) {
			return true
		}
	}
	return false
}
func (h multiHandler) Handle(c context.Context, r slog.Record) error {
	for _, v := range h {
		if v.Enabled(c, r.Level) {
			if err := v.Handle(c, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}
func (h multiHandler) WithAttrs(a []slog.Attr) slog.Handler {
	out := make(multiHandler, len(h))
	for i, v := range h {
		out[i] = v.WithAttrs(a)
	}
	return out
}
func (h multiHandler) WithGroup(n string) slog.Handler {
	out := make(multiHandler, len(h))
	for i, v := range h {
		out[i] = v.WithGroup(n)
	}
	return out
}
