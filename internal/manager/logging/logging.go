package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"
)

const (
	HeaderLogID     = "X-Log-ID"
	HeaderRequestID = "X-Request-ID"
)

type contextKey struct{}

type Fields []slog.Attr

func With(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	current := FromContext(ctx)
	next := make(Fields, 0, len(current)+len(attrs))
	next = append(next, current...)
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		next = append(next, attr)
	}
	return context.WithValue(ctx, contextKey{}, next)
}

func EnsureLogID(ctx context.Context, candidates ...string) (context.Context, string) {
	if logID := LogID(ctx); logID != "" {
		return ctx, logID
	}
	logID := firstNonEmpty(candidates...)
	if logID == "" {
		logID = NewLogID()
	}
	return With(ctx, slog.String("log_id", logID)), logID
}

func LogID(ctx context.Context) string {
	for _, attr := range FromContext(ctx) {
		if attr.Key == "log_id" {
			return attr.Value.String()
		}
	}
	return ""
}

func FromContext(ctx context.Context) Fields {
	if ctx == nil {
		return nil
	}
	fields, _ := ctx.Value(contextKey{}).(Fields)
	return fields
}

func Info(ctx context.Context, msg string, attrs ...slog.Attr) {
	slog.Default().LogAttrs(ctx, slog.LevelInfo, msg, appendContextAttrs(ctx, attrs)...)
}

func Warn(ctx context.Context, msg string, attrs ...slog.Attr) {
	slog.Default().LogAttrs(ctx, slog.LevelWarn, msg, appendContextAttrs(ctx, attrs)...)
}

func Error(ctx context.Context, msg string, attrs ...slog.Attr) {
	slog.Default().LogAttrs(ctx, slog.LevelError, msg, appendContextAttrs(ctx, attrs)...)
}

func Err(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	return slog.String("error", err.Error())
}

func NewLogID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "log_" + strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "")
	}
	return "log_" + hex.EncodeToString(b[:])
}

func appendContextAttrs(ctx context.Context, attrs []slog.Attr) []slog.Attr {
	contextAttrs := FromContext(ctx)
	if len(contextAttrs) == 0 {
		return attrs
	}
	out := make([]slog.Attr, 0, len(contextAttrs)+len(attrs))
	out = append(out, contextAttrs...)
	out = append(out, attrs...)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
