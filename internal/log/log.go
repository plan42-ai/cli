package log

import (
	"context"
	"log/slog"
)

type ContextHandler struct {
	wrapped slog.Handler
}

func NewContextHandler(wrapped slog.Handler) slog.Handler {
	return ContextHandler{
		wrapped: wrapped,
	}
}

type contextKeyType string

var contextKey contextKeyType = "logValues"

func (c ContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return c.wrapped.Enabled(ctx, level)
}

func (c ContextHandler) Handle(ctx context.Context, record slog.Record) error {
	return c.wrapped.WithAttrs(getContextAttrs(ctx)).Handle(ctx, record)
}

func getContextAttrs(ctx context.Context) []slog.Attr {
	values, _ := ctx.Value(contextKey).([]slog.Attr)
	return values
}

func WithContextAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	existingAttr := getContextAttrs(ctx)
	newAttrs := make([]slog.Attr, len(existingAttr)+len(attrs))
	copy(newAttrs, existingAttr)
	copy(newAttrs[len(existingAttr):], attrs)

	return context.WithValue(ctx, contextKey, newAttrs)
}

func (c ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ContextHandler{
		wrapped: c.wrapped.WithAttrs(attrs),
	}
}

func (c ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{
		wrapped: c.wrapped.WithGroup(name),
	}
}
