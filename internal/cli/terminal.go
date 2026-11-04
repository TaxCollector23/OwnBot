package cli

import (
	"io"
	"os"
	"strings"
)

func terminalAccent(w io.Writer, text string) string {
	if !terminalColorsEnabled(w, os.Getenv("NO_COLOR"), os.Getenv("TERM")) {
		return text
	}
	return "\x1b[1;36m" + text + "\x1b[0m"
}

func terminalColorsEnabled(w io.Writer, noColor, term string) bool {
	file, ok := w.(*os.File)
	if !ok || noColor != "" || strings.EqualFold(term, "dumb") {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
