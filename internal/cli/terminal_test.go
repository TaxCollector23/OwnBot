package cli

import (
	"io"
	"strings"
	"testing"
)

func TestTerminalColorsRequireInteractiveOutput(t *testing.T) {
	if terminalColorsEnabled(io.Discard, "", "xterm-256color") {
		t.Fatal("non-file output enabled terminal colors")
	}
	if got := terminalAccent(io.Discard, "OwnBot"); strings.Contains(got, "\x1b[") || got != "OwnBot" {
		t.Fatalf("non-terminal heading = %q", got)
	}
}

func TestTerminalColorsHonorNO_COLORAndDumbTerm(t *testing.T) {
	if shouldEnableTerminalColors(true, "1", "xterm-256color") {
		t.Fatal("NO_COLOR did not disable terminal colors")
	}
	if shouldEnableTerminalColors(true, "", "dumb") {
		t.Fatal("dumb terminal enabled terminal colors")
	}
	if shouldEnableTerminalColors(false, "", "xterm-256color") {
		t.Fatal("non-terminal output enabled terminal colors")
	}
}

func TestJSONOutputHasNoTerminalControlCodes(t *testing.T) {
	var output strings.Builder
	if err := writeJSON(&output, map[string]string{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("JSON output contains terminal styling: %q", output.String())
	}
}
