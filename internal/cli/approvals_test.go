package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectApprovalsListAndShowAreReadOnlyJSON(t *testing.T) {
	dataDir := tokenDir(t)
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	untrustedTool := "calendar.read\n$(touch " + marker + ")"
	listPayload, err := json.Marshal([]approvalRecord{{ID: "approval-1", Tool: untrustedTool, Args: json.RawMessage(`{"title":"line one\nline two"}`), Status: "pending"}})
	if err != nil {
		t.Fatal(err)
	}
	showPayload, err := json.Marshal(approvalRecord{ID: "old/approval", Tool: untrustedTool, Args: json.RawMessage(`{"query":"q"}`), Status: "denied"})
	if err != nil {
		t.Fatal(err)
	}
	var requests []string
	api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("missing owner authorization on %s", r.URL.EscapedPath())
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutating request %s %s", r.Method, r.URL.EscapedPath())
			http.Error(w, "unexpected write", http.StatusMethodNotAllowed)
			return
		}
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/api/approvals":
			_, _ = w.Write(listPayload)
		case "/api/approvals/old%2Fapproval":
			_, _ = w.Write(showPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	var listOut, showOut, stderr strings.Builder
	if err := runApprovals([]string{"list", "--url", api.URL, "--data-dir", dataDir}, &listOut, &stderr); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if err := runApprovals([]string{"show", "--url", api.URL, "--data-dir", dataDir, "old/approval"}, &showOut, &stderr); err != nil {
		t.Fatalf("show failed: %v", err)
	}
	var listed []approvalRecord
	if err := json.Unmarshal([]byte(listOut.String()), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("list output is not approval JSON: err=%v output=%s", err, listOut.String())
	}
	var shown approvalRecord
	if err := json.Unmarshal([]byte(showOut.String()), &shown); err != nil || shown.ID != "old/approval" {
		t.Fatalf("show output is not approval JSON: err=%v output=%s", err, showOut.String())
	}
	if listed[0].Tool != untrustedTool || strings.Contains(stderr.String(), "touch") {
		t.Fatalf("untrusted tool metadata was changed or sent to stderr: %+v stderr=%s", listed[0], stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("untrusted tool name was executed, marker stat err=%v", err)
	}
	want := []string{"GET /api/approvals", "GET /api/approvals/old%2Fapproval"}
	if strings.Join(requests, ",") != strings.Join(want, ",") {
		t.Fatalf("requests=%v, want %v", requests, want)
	}
	if strings.Contains(listOut.String()+showOut.String()+stderr.String(), testToken) {
		t.Fatal("approval output exposed the local API token")
	}
}

func TestInspectApprovalsApproveAndDenyUseOneExplicitPendingID(t *testing.T) {
	for _, operation := range []string{"approve", "deny"} {
		t.Run(operation, func(t *testing.T) {
			dataDir := tokenDir(t)
			var requests []string
			api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Errorf("missing owner authorization on %s", r.URL.EscapedPath())
				}
				requests = append(requests, r.Method+" "+r.URL.EscapedPath())
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/approvals/approval-abc":
					_, _ = io.WriteString(w, `{"id":"approval-abc","tool":"write_file","args":{"path":"x"},"status":"pending"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/api/approvals/approval-abc/"+operation:
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "{}" {
						t.Errorf("decision body=%q err=%v, want empty JSON object", body, err)
					}
					status := "approved"
					if operation == "deny" {
						status = "denied"
					}
					_, _ = io.WriteString(w, `{"id":"approval-abc","status":"`+status+`","decided_at":"2026-10-29T00:00:00Z","approver":"local-user"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer api.Close()

			var stdout, stderr strings.Builder
			err := runApprovals([]string{operation, "--url", api.URL, "--data-dir", dataDir, "approval-abc"}, &stdout, &stderr)
			if err != nil {
				t.Fatalf("%s failed: %v", operation, err)
			}
			var decision approvalDecision
			if err := json.Unmarshal([]byte(stdout.String()), &decision); err != nil {
				t.Fatalf("decision output is not JSON: %v %s", err, stdout.String())
			}
			wantStatus := "approved"
			if operation == "deny" {
				wantStatus = "denied"
			}
			if decision.ID != "approval-abc" || decision.Status != wantStatus {
				t.Fatalf("unexpected decision response: %+v", decision)
			}
			if operation == "approve" && !strings.Contains(stderr.String(), "scheduled automation") {
				t.Fatalf("approve output omitted scheduled-run note: %s", stderr.String())
			}
			if operation == "deny" && stderr.Len() != 0 {
				t.Fatalf("deny unexpectedly printed resume note: %s", stderr.String())
			}
			want := []string{"GET /api/approvals/approval-abc", "POST /api/approvals/approval-abc/" + operation}
			if strings.Join(requests, ",") != strings.Join(want, ",") {
				t.Fatalf("requests=%v, want exactly %v", requests, want)
			}
		})
	}
}

func TestInspectApprovalsAlreadyDecidedAndConflictDoNotRetry(t *testing.T) {
	t.Run("already decided skips POST", func(t *testing.T) {
		dataDir := tokenDir(t)
		var posts int
		api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts++
			}
			if r.Method == http.MethodGet && r.URL.Path == "/api/approvals/a1" {
				_, _ = io.WriteString(w, `{"id":"a1","status":"approved"}`)
				return
			}
			http.NotFound(w, r)
		}))
		defer api.Close()
		var stdout, stderr strings.Builder
		err := runApprovals([]string{"approve", "--url", api.URL, "--data-dir", dataDir, "a1"}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "already \"approved\"") || posts != 0 {
			t.Fatalf("err=%v posts=%d; want already-decided error and no POST", err, posts)
		}
	})

	t.Run("prefetch ID mismatch skips POST", func(t *testing.T) {
		dataDir := tokenDir(t)
		var posts int
		api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts++
			}
			if r.Method == http.MethodGet && r.URL.Path == "/api/approvals/a1" {
				_, _ = io.WriteString(w, `{"id":"different","status":"pending"}`)
				return
			}
			http.NotFound(w, r)
		}))
		defer api.Close()
		var stdout, stderr strings.Builder
		err := runApprovals([]string{"approve", "--url", api.URL, "--data-dir", dataDir, "a1"}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "no decision was submitted") || posts != 0 || stdout.Len() != 0 {
			t.Fatalf("err=%v posts=%d stdout=%q; want fail-closed lookup mismatch", err, posts, stdout.String())
		}
	})

	t.Run("409 is surfaced without retry", func(t *testing.T) {
		dataDir := tokenDir(t)
		var posts int
		api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/approvals/a1" {
				_, _ = io.WriteString(w, `{"id":"a1","status":"pending"}`)
				return
			}
			if r.Method == http.MethodPost && r.URL.Path == "/api/approvals/a1/approve" {
				posts++
				http.Error(w, `{"error":"approval not found or already decided"}`, http.StatusConflict)
				return
			}
			http.NotFound(w, r)
		}))
		defer api.Close()
		var stdout, stderr strings.Builder
		err := runApprovals([]string{"approve", "--url", api.URL, "--data-dir", dataDir, "a1"}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "409 Conflict") || posts != 1 {
			t.Fatalf("err=%v posts=%d; want surfaced 409 and exactly one POST", err, posts)
		}
		if stdout.Len() != 0 {
			t.Fatalf("failed decision wrote success JSON: %s", stdout.String())
		}
	})

	for _, response := range []struct {
		name string
		body string
	}{
		{name: "decision ID mismatch", body: `{"id":"different","status":"approved"}`},
		{name: "decision status mismatch", body: `{"id":"a1","status":"denied"}`},
	} {
		t.Run(response.name, func(t *testing.T) {
			dataDir := tokenDir(t)
			posts := 0
			api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/approvals/a1" {
					_, _ = io.WriteString(w, `{"id":"a1","status":"pending"}`)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == "/api/approvals/a1/approve" {
					posts++
					_, _ = io.WriteString(w, response.body)
					return
				}
				http.NotFound(w, r)
			}))
			defer api.Close()
			var stdout, stderr strings.Builder
			err := runApprovals([]string{"approve", "--url", api.URL, "--data-dir", dataDir, "a1"}, &stdout, &stderr)
			if err == nil || posts != 1 || stdout.Len() != 0 {
				t.Fatalf("err=%v posts=%d stdout=%q; want response mismatch and no success output", err, posts, stdout.String())
			}
		})
	}
}

func TestInspectApprovalsIdentityTokenAndInvalidArgsBoundaries(t *testing.T) {
	t.Run("wrong daemon receives no token", func(t *testing.T) {
		dataDir := tokenDir(t)
		var authSeen bool
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authSeen = authSeen || r.Header.Get("Authorization") != ""
			_, _ = io.WriteString(w, `{"ok":true,"service":"not-ownbot"}`)
		}))
		defer api.Close()
		var stdout, stderr strings.Builder
		err := runApprovals([]string{"list", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
		if err == nil || authSeen {
			t.Fatalf("err=%v authSeen=%t; wrong daemon must be rejected before token auth", err, authSeen)
		}
	})

	t.Run("wrong token is rejected", func(t *testing.T) {
		dataDir := tokenDir(t)
		var authSeen bool
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/status" && r.Header.Get("Authorization") == "" {
				_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
				return
			}
			authSeen = r.Header.Get("Authorization") == "Bearer "+testToken
			http.Error(w, `{"error":"token rejected"}`, http.StatusUnauthorized)
		}))
		defer api.Close()
		var stdout, stderr strings.Builder
		err := runApprovals([]string{"list", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
		if err == nil || !authSeen || stdout.Len() != 0 || strings.Contains(stdout.String()+stderr.String(), testToken) {
			t.Fatalf("err=%v authSeen=%t stdout=%q stderr=%q", err, authSeen, stdout.String(), stderr.String())
		}
	})

	t.Run("invalid argument counts send no API requests", func(t *testing.T) {
		dataDir := tokenDir(t)
		requests := 0
		api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			_, _ = io.WriteString(w, `[]`)
		}))
		defer api.Close()
		for _, args := range [][]string{
			{"list", "unexpected", "--url", api.URL, "--data-dir", dataDir},
			{"show", "--url", api.URL, "--data-dir", dataDir},
			{"approve", "--url", api.URL, "--data-dir", dataDir},
			{"deny", "a1", "extra", "--url", api.URL, "--data-dir", dataDir},
		} {
			var stdout, stderr strings.Builder
			if err := runApprovals(args, &stdout, &stderr); err == nil {
				t.Errorf("args %v unexpectedly succeeded", args)
			}
		}
		if requests != 0 {
			t.Fatalf("invalid arguments caused %d API requests", requests)
		}
	})

	t.Run("help requires no daemon", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if err := runApprovals([]string{"--help"}, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		for _, phrase := range []string{"latest 500", "older records", "pending ID", "Scheduled automation", "ownbot inspect runs resume"} {
			if !strings.Contains(stdout.String(), phrase) {
				t.Errorf("help missing %q: %s", phrase, stdout.String())
			}
		}
	})
}
