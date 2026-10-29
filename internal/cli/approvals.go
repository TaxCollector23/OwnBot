package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"net/http"
)

type approvalRecord struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agent_id"`
	RunID       string          `json:"run_id"`
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason"`
	RequestedAt string          `json:"requested_at"`
	DecidedAt   string          `json:"decided_at"`
	Approver    string          `json:"approver"`
}

type approvalDecision struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	DecidedAt string `json:"decided_at"`
	Approver  string `json:"approver"`
}

func runApprovals(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		writeApprovalUsage(stdout)
		return nil
	}
	operation := args[0]
	if operation != "list" && operation != "show" && operation != "approve" && operation != "deny" {
		return fmt.Errorf("unknown approvals command %q; run `ownbot inspect approvals --help`", operation)
	}

	fs := flag.NewFlagSet("inspect approvals "+operation, flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: ownbot inspect approvals %s [--url URL] [--data-dir DIR]", operation)
		if operation != "list" {
			_, _ = fmt.Fprint(stderr, " APPROVAL_ID")
		}
		_, _ = fmt.Fprintln(stderr)
		_, _ = fmt.Fprintln(stderr, "Approval records and decisions are written as JSON. List and show include stored tool arguments.")
		_, _ = fmt.Fprintln(stderr, "Approving authorizes the pending request. Scheduled automation waiting on approval may resume automatically when no pending approvals remain; manual runs require `ownbot inspect runs resume`.")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args[1:], stderr); err != nil {
		return err
	}
	if operation == "list" && fs.NArg() != 0 {
		return errors.New("approvals list does not accept an approval ID")
	}
	if operation != "list" && fs.NArg() != 1 {
		return fmt.Errorf("approvals %s requires exactly one approval ID", operation)
	}
	id := ""
	if operation != "list" {
		id = fs.Arg(0)
		if strings.TrimSpace(id) == "" {
			return errors.New("approval ID cannot be empty")
		}
	}

	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch operation {
	case "list":
		var approvals []approvalRecord
		if err := getApprovalJSON(ctx, client, "/api/approvals", &approvals); err != nil {
			return fmt.Errorf("could not list approvals: %w", err)
		}
		return writeJSON(stdout, approvals)
	case "show":
		var approval approvalRecord
		if err := getApprovalJSON(ctx, client, "/api/approvals/"+urlPath(id), &approval); err != nil {
			return fmt.Errorf("could not show approval: %w", err)
		}
		return writeJSON(stdout, approval)
	case "approve", "deny":
		path := "/api/approvals/" + urlPath(id)
		var approval approvalRecord
		if err := getApprovalJSON(ctx, client, path, &approval); err != nil {
			return fmt.Errorf("could not inspect approval %q before decision: %w", id, err)
		}
		if approval.ID != id {
			return fmt.Errorf("approval lookup returned ID %q for requested ID %q; no decision was submitted", approval.ID, id)
		}
		if approval.Status != "pending" {
			if approval.Status == "" {
				return errors.New("approval response has no status; no decision was submitted")
			}
			return fmt.Errorf("approval is already %q; no decision was submitted", approval.Status)
		}
		decisionPath := path + "/" + operation
		var decision approvalDecision
		if err := client.Do(ctx, http.MethodPost, decisionPath, map[string]any{}, &decision); err != nil {
			return err
		}
		wantStatus := "approved"
		if operation == "deny" {
			wantStatus = "denied"
		}
		if decision.ID != id || decision.Status != wantStatus {
			return errors.New("local API returned an unexpected approval decision; no success was reported")
		}
		if err := writeJSON(stdout, decision); err != nil {
			return err
		}
		if operation == "approve" {
			_, _ = fmt.Fprintln(stderr, "Approval recorded. A scheduled automation waiting on approval may resume when no pending approvals remain; manual runs still require `ownbot inspect runs resume`.")
		}
		return nil
	default:
		return errors.New("unsupported approval operation")
	}
}

func getApprovalJSON(ctx context.Context, client *Client, path string, output any) error {
	var raw json.RawMessage
	if err := client.Do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("local API returned an empty approval record")
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return errors.New("local API returned invalid approval JSON")
	}
	return nil
}

func writeApprovalUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: ownbot inspect approvals list|show|approve|deny [--url URL] [--data-dir DIR] [APPROVAL_ID]")
	_, _ = fmt.Fprintln(w, "list shows the latest 500 approval requests. show fetches one exact ID, including older records.")
	_, _ = fmt.Fprintln(w, "approve and deny accept exactly one pending ID. Records and decisions are JSON; list/show include stored tool arguments.")
	_, _ = fmt.Fprintln(w, "Approving authorizes the pending request. Scheduled automation waiting on approval may resume automatically when no pending approvals remain; manual runs require `ownbot inspect runs resume`.")
}
