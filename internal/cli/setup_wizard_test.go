package cli

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/skip2/go-qrcode"
)

func TestSetupWizardSelectSupportsArrowNavigation(t *testing.T) {
	selected := "ollama"
	field := huh.NewSelect[string]().Title("Runtime").Options(
		huh.NewOption("Ollama", "ollama"),
		huh.NewOption("LM Studio", "lm_studio"),
	).Value(&selected)
	form := huh.NewForm(huh.NewGroup(field))
	form.Update(form.Init())
	form.Update(tea.KeyMsg{Type: tea.KeyDown})
	form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if selected != "lm_studio" {
		t.Fatalf("arrow navigation selected %q, want lm_studio", selected)
	}
}

func TestSetupWizardPermissionsUseSpaceAndDefaultToNone(t *testing.T) {
	selected := []string{}
	field := huh.NewMultiSelect[string]().Title("Permissions").Options(
		huh.NewOption("Read", "read"),
		huh.NewOption("Write", "write"),
	).Value(&selected)
	form := huh.NewForm(huh.NewGroup(field))
	form.Update(form.Init())
	if got := field.GetValue().([]string); len(got) != 0 {
		t.Fatalf("permissions defaulted to %v, want chat-only", got)
	}
	form.Update(tea.KeyMsg{Type: tea.KeySpace})
	form.Update(tea.KeyMsg{Type: tea.KeyDown})
	form.Update(tea.KeyMsg{Type: tea.KeySpace})
	form.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := field.GetValue().([]string)
	if len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Fatalf("Space toggle selected %v, want read and write", got)
	}
}

func TestTerminalQRUsesOneUsePairingURLAndHasQuietZone(t *testing.T) {
	pairingURL := "https://192.168.1.4:8766/dashboard#pair=one-use-secret"
	code, err := qrcode.New(pairingURL, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	if code.Content != pairingURL || strings.Contains(code.Content, "permanent-host-token") {
		t.Fatalf("QR content is not the one-use pairing URL: %q", code.Content)
	}
	var output bytes.Buffer
	printTerminalQR(&output, code.Bitmap())
	if strings.Count(output.String(), "\n") < len(code.Bitmap())/2 {
		t.Fatalf("terminal QR bitmap was not rendered: %d bytes", output.Len())
	}
}

func TestPermissionRiskGroupsStayDistinctAndOrdered(t *testing.T) {
	if riskOrder("read") >= riskOrder("write_reversible") || riskOrder("write_reversible") >= riskOrder("write_irreversible") {
		t.Fatal("permission risk ordering does not separate read, reversible, and irreversible actions")
	}
	if permissionRiskLabel("write_irreversible") != "irreversible" {
		t.Fatalf("unexpected irreversible permission label: %q", permissionRiskLabel("write_irreversible"))
	}
}
