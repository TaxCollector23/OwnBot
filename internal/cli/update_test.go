package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateDaemonHealthRequiresExactReleaseVersion(t *testing.T) {
	version := "1.2.3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"service":"ownbot","version":%q}`, version)
	}))
	defer server.Close()
	if !daemonReportsVersion(server.URL, version) {
		t.Fatal("matching updated daemon version was rejected")
	}
	if daemonReportsVersion(server.URL, "1.2.4") {
		t.Fatal("old daemon version passed the update health check")
	}
}
