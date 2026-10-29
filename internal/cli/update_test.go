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

func TestSelectReleaseMatchesCurrentChannelAndSkipsDrafts(t *testing.T) {
	releases := []releaseInfo{
		{TagName: "v5.0.0", Draft: true},
		{TagName: "v4.0.0", Prerelease: false},
		{TagName: "v4.1.0-alpha.2", Prerelease: true},
	}
	stable, err := selectRelease(releases, false)
	if err != nil || stable.TagName != "v4.0.0" {
		t.Fatalf("stable release=%+v err=%v", stable, err)
	}
	prerelease, err := selectRelease(releases, true)
	if err != nil || prerelease.TagName != "v4.1.0-alpha.2" {
		t.Fatalf("prerelease=%+v err=%v", prerelease, err)
	}
	if _, err := selectRelease(releases[:2], true); err == nil {
		t.Fatal("missing prerelease did not fail clearly")
	}
}
