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
	return "\x1b[1;34m" + text + "\x1b[0m"
}

func terminalColorsEnabled(w io.Writer, noColor, term string) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && shouldEnableTerminalColors(info.Mode()&os.ModeCharDevice != 0, noColor, term)
}

func shouldEnableTerminalColors(isTerminal bool, noColor, term string) bool {
	return isTerminal && noColor == "" && !strings.EqualFold(term, "dumb")
}
