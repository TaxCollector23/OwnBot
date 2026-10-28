package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func testSpec(t *testing.T, goos string, runner func(string, ...string) error) Spec {
	t.Helper()
	home := t.TempDir()
	return Spec{GOOS: goos, HomeDir: home, Executable: filepath.Join(home, "Own Your Bot", "ownbotd"), Args: []string{"--data-dir", filepath.Join(home, "Library Data", "ownbot"), "--listen", "127.0.0.1:18765"}, DataDir: filepath.Join(home, "Library Data", "ownbot"), Run: runner}
}

func TestRenderLaunchAgentXMLAndEscapesPaths(t *testing.T) {
	spec := testSpec(t, "darwin", nil)
	content, err := spec.render()
	if err != nil {
		t.Fatal(err)
	}
	spec.Executable = filepath.Join(spec.HomeDir, "Own & Bot", "ownbotd")
	content, err = spec.render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ownbotd") || !strings.Contains(string(content), "Library Data") {
		t.Fatalf("plist lost path arguments: %s", content)
	}
	if !strings.Contains(string(content), "Own &amp; Bot") {
		t.Fatalf("plist did not XML escape content: %s", content)
	}
	if runtime.GOOS == "darwin" {
		plutil, err := exec.LookPath("plutil")
		if err != nil {
			t.Skip("plutil is unavailable")
		}
		path := filepath.Join(t.TempDir(), "service.plist")
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
			t.Fatalf("plutil rejected LaunchAgent: %s (%v)", output, err)
		}
	}
}

func TestRenderSystemdQuotesShellLikeCharacters(t *testing.T) {
	spec := testSpec(t, "linux", nil)
	spec.Executable = "/opt/Own Your Bot/ownbotd"
	spec.Args = []string{"--data-dir", "/tmp/a b/$HOME/100%", `x"y`}
	content, err := spec.render()
	if err != nil {
		t.Fatal(err)
	}
	unit := string(content)
	for _, want := range []string{`"/opt/Own Your Bot/ownbotd"`, `"/tmp/a b/$$HOME/100%%"`, `"x\"y"`} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q: %s", want, unit)
		}
	}
}

func TestInstallAndUninstallTouchOnlyServiceFiles(t *testing.T) {
	var calls [][]string
	run := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	spec := testSpec(t, "linux", run)
	if err := Install(spec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spec.HomeDir, ".config", "systemd", "user", unitName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unit mode = %o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("service dir mode = %v, err=%v", dirInfo, err)
	}
	if got, want := calls, [][]string{{"systemctl", "--user", "show-environment"}, {"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", unitName}, {"systemctl", "--user", "restart", unitName}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("install commands = %#v", got)
	}
	status, err := Inspect(spec)
	if err != nil || !status.Installed || !status.Active {
		t.Fatalf("service status = %#v, err=%v", status, err)
	}
	data := filepath.Join(spec.HomeDir, "Library Data", "ownbot")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(data, "api.token")
	if err := os.WriteFile(token, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("service file remains: %v", err)
	}
	if got, err := os.ReadFile(token); err != nil || string(got) != "private" {
		t.Fatalf("uninstall touched data: %q, %v", got, err)
	}
}

func TestInstallUsesLaunchctlUserDomainWithoutShell(t *testing.T) {
	var calls [][]string
	run := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	spec := testSpec(t, "darwin", run)
	if err := Install(spec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spec.HomeDir, "Library", "LaunchAgents", label+".plist")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0][0] != "launchctl" || calls[0][1] != "print" || calls[2][0] != "launchctl" || calls[2][1] != "bootstrap" {
		t.Fatalf("LaunchAgent commands = %#v", calls)
	}
	if strings.Contains(strings.Join(calls[0], " "), "sh -c") {
		t.Fatal("service commands must not use a shell")
	}
}

func TestInspectUnsupportedAndUninstallNeverTouchesData(t *testing.T) {
	spec := testSpec(t, "windows", nil)
	status, err := Inspect(spec)
	if err != nil || status.Supported {
		t.Fatalf("status = %#v, err=%v", status, err)
	}
	if err := Uninstall(spec); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported uninstall error = %v", err)
	}
}

func TestInstallRejectsRelativeExecutable(t *testing.T) {
	spec := testSpec(t, "linux", nil)
	spec.Executable = "ownbotd"
	if err := Install(spec); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("install error = %v", err)
	}
}

func TestLinuxServicePathFallsBackForRelativeXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	spec := Spec{GOOS: "linux"}
	path, err := spec.servicePath()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "systemd", "user", unitName)
	if path != want {
		t.Fatalf("service path=%q want fallback %q", path, want)
	}
	spec.ConfigHome = "relative-config"
	if _, err := spec.servicePath(); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("explicit relative config home error=%v", err)
	}
}

func TestPreflightReportsUnavailableUserManager(t *testing.T) {
	spec := testSpec(t, "linux", func(name string, args ...string) error {
		if !reflect.DeepEqual(append([]string{name}, args...), []string{"systemctl", "--user", "show-environment"}) {
			t.Fatalf("unexpected preflight command: %s %v", name, args)
		}
		return os.ErrPermission
	})
	if err := Preflight(spec); err == nil || !strings.Contains(err.Error(), "systemd manager is unavailable") {
		t.Fatalf("preflight error=%v", err)
	}
}

func TestInstallFailureRestoresPreviousServiceFile(t *testing.T) {
	spec := testSpec(t, "linux", func(name string, args ...string) error {
		if len(args) > 1 && args[1] == "restart" {
			return os.ErrPermission
		}
		return nil
	})
	path, err := spec.servicePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	previous := []byte("previous unit definition\n")
	if err := os.WriteFile(path, previous, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(spec); err == nil {
		t.Fatal("install unexpectedly succeeded")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(previous) {
		t.Fatalf("previous unit not restored: %q, err=%v", got, err)
	}
}
