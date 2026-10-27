package daemon

import (
	"io"
	"log/slog"
	"strings"
)

func NewLogger(w io.Writer) *slog.Logger {
	replace := func(groups []string, a slog.Attr) slog.Attr {
		key := strings.ToLower(strings.Join(append(append([]string(nil), groups...), a.Key), "."))
		for _, marker := range []string{"token", "secret", "password", "credential", "api_key", "apikey", "authorization", "cookie"} {
			if strings.Contains(key, marker) {
				a.Value = slog.StringValue("[REDACTED]")
				return a
			}
		}
		return a
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: replace}))
}
