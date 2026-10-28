// Package service installs OwnBot as a per-user login service. It never
// requests administrator access and never owns or removes the application's data.
package service

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const label = "com.ownbot.daemon"
const unitName = "ownbot.service"

type Spec struct {
	Executable string
	Args       []string
	DataDir    string
	HomeDir    string                                  // optional; injectable for tests
	ConfigHome string                                  // optional; injectable for tests
	GOOS       string                                  // optional; injectable for tests
	Run        func(name string, args ...string) error // optional; injectable for tests
}

type Status struct {
	Supported bool
	Installed bool
	Active    bool
	Path      string
	Message   string
}

func (s Spec) osName() string {
	if s.GOOS != "" {
		return s.GOOS
	}
	return runtime.GOOS
}

func (s Spec) home() (string, error) {
	if s.HomeDir != "" {
		return s.HomeDir, nil
	}
	return os.UserHomeDir()
}

func (s Spec) run(name string, args ...string) error {
	if s.Run != nil {
		return s.Run(name, args...)
	}
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), message, err)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (s Spec) servicePath() (string, error) {
	home, err := s.home()
	if err != nil {
		return "", fmt.Errorf("locate user home: %w", err)
	}
	switch s.osName() {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
	case "linux":
		configHome := s.ConfigHome
		if configHome == "" && s.HomeDir == "" {
			configHome = os.Getenv("XDG_CONFIG_HOME")
		}
		if configHome != "" && !filepath.IsAbs(configHome) {
			if s.ConfigHome != "" {
				return "", errors.New("XDG config directory must be an absolute path")
			}
			configHome = ""
		}
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		return filepath.Join(configHome, "systemd", "user", unitName), nil
	default:
		return "", fmt.Errorf("user services are not supported on %s; run `ownbot daemon start` for this session", s.osName())
	}
}

func Supported(goos string) bool { return goos == "darwin" || goos == "linux" }

// Preflight verifies that the current user's native service manager is
// available before callers stop an already-running daemon.
func Preflight(s Spec) error {
	if !Supported(s.osName()) {
		return unsupported(s.osName())
	}
	if s.osName() == "darwin" {
		domain, err := guiDomain()
		if err != nil {
			return err
		}
		if err := s.run("launchctl", "print", domain); err != nil {
			return fmt.Errorf("current user's LaunchAgent manager is unavailable: %w", err)
		}
		return nil
	}
	if err := s.run("systemctl", "--user", "show-environment"); err != nil {
		return fmt.Errorf("current user's systemd manager is unavailable: %w", err)
	}
	return nil
}

func Install(s Spec) error {
	if !Supported(s.osName()) {
		return unsupported(s.osName())
	}
	if strings.TrimSpace(s.Executable) == "" || !filepath.IsAbs(s.Executable) {
		return errors.New("service executable must be an absolute path")
	}
	if strings.TrimSpace(s.DataDir) == "" {
		return errors.New("service data directory is required")
	}
	for _, arg := range append([]string{s.Executable}, s.Args...) {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return errors.New("service arguments cannot contain control characters")
		}
	}
	path, err := s.servicePath()
	if err != nil {
		return err
	}
	content, err := s.render()
	if err != nil {
		return err
	}
	if err := Preflight(s); err != nil {
		return err
	}
	previous, hadPrevious, err := readServiceFile(path)
	if err != nil {
		return fmt.Errorf("inspect existing user service: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create user service directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("secure user service directory: %w", err)
	}
	if err := atomicWrite(path, content); err != nil {
		return fmt.Errorf("write secure user service: %w", err)
	}
	if s.osName() == "darwin" {
		if err := os.MkdirAll(s.DataDir, 0700); err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("create daemon data directory: %w", err))
		}
		if err := os.Chmod(s.DataDir, 0700); err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("secure daemon data directory: %w", err))
		}
		logFile, err := os.OpenFile(filepath.Join(s.DataDir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("create secure daemon log: %w", err))
		}
		if err := logFile.Chmod(0600); err != nil {
			_ = logFile.Close()
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("secure daemon log: %w", err))
		}
		_ = logFile.Close()
	}
	switch s.osName() {
	case "darwin":
		domain, err := guiDomain()
		if err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, err)
		}
		// Rebootstrap makes repeated installs update the existing user's agent.
		if err := s.run("launchctl", "bootout", domain, path); err != nil {
			if loaded := s.run("launchctl", "print", domain+"/"+label); loaded == nil {
				return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("unload existing user service: %w", err))
			}
		}
		if err := s.run("launchctl", "bootstrap", domain, path); err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("start user service: %w", err))
		}
	case "linux":
		if err := s.run("systemctl", "--user", "daemon-reload"); err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("reload user services: %w", err))
		}
		if err := s.run("systemctl", "--user", "enable", unitName); err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("enable user service: %w", err))
		}
		// restart also starts an inactive service and applies changed arguments
		// when an older unit was already active.
		if err := s.run("systemctl", "--user", "restart", unitName); err != nil {
			return rollbackInstall(s, path, previous, hadPrevious, fmt.Errorf("start user service: %w", err))
		}
	}
	return nil
}

func readServiceFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("refusing to replace non-regular service file %s", path)
	}
	content, err := os.ReadFile(path)
	return content, true, err
}

func rollbackInstall(s Spec, path string, previous []byte, hadPrevious bool, cause error) error {
	var err error
	if hadPrevious {
		err = atomicWrite(path, previous)
	} else {
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	}
	if err != nil {
		cause = errors.Join(cause, fmt.Errorf("restore previous service definition: %w", err))
	}
	if s.osName() == "linux" {
		if err := s.run("systemctl", "--user", "daemon-reload"); err != nil {
			cause = errors.Join(cause, fmt.Errorf("reload user services after rollback: %w", err))
		}
	}
	return cause
}

func atomicWrite(path string, content []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular service file %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ownbot-service-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func Uninstall(s Spec) error {
	if !Supported(s.osName()) {
		return unsupported(s.osName())
	}
	path, err := s.servicePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect user service: %w", err)
	}
	switch s.osName() {
	case "darwin":
		domain, err := guiDomain()
		if err != nil {
			return err
		}
		if err := s.run("launchctl", "bootout", domain, path); err != nil {
			if loaded := s.run("launchctl", "print", domain+"/"+label); loaded == nil {
				return fmt.Errorf("stop user service: %w", err)
			}
		}
	case "linux":
		if err := s.run("systemctl", "--user", "disable", "--now", unitName); err != nil {
			return fmt.Errorf("disable user service: %w", err)
		}
		if err := s.run("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("reload user services: %w", err)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove user service file: %w", err)
	}
	return nil
}

func Inspect(s Spec) (Status, error) {
	if !Supported(s.osName()) {
		return Status{Message: fmt.Sprintf("user services are not supported on %s", s.osName())}, nil
	}
	path, err := s.servicePath()
	if err != nil {
		return Status{}, err
	}
	status := Status{Supported: true, Path: path}
	if _, err := os.Stat(path); err == nil {
		status.Installed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, fmt.Errorf("inspect user service: %w", err)
	}
	if !status.Installed {
		return status, nil
	}
	switch s.osName() {
	case "darwin":
		domain, err := guiDomain()
		if err != nil {
			return Status{}, err
		}
		status.Active = s.run("launchctl", "print", domain+"/"+label) == nil
	case "linux":
		status.Active = s.run("systemctl", "--user", "is-active", "--quiet", unitName) == nil
	}
	return status, nil
}

func unsupported(goos string) error {
	return fmt.Errorf("user service installation is not supported on %s; run `ownbot daemon start` for this session", goos)
}

func guiDomain() (string, error) {
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return "", errors.New("could not determine current user for LaunchAgent")
	}
	return "gui/" + current.Uid, nil
}

func (s Spec) render() ([]byte, error) {
	switch s.osName() {
	case "darwin":
		args := append([]string{s.Executable}, s.Args...)
		var b strings.Builder
		b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict><key>Label</key><string>")
		b.WriteString(label)
		b.WriteString("</string><key>ProgramArguments</key><array>")
		for _, arg := range args {
			b.WriteString("<string>")
			_ = xml.EscapeText(&b, []byte(arg))
			b.WriteString("</string>")
		}
		b.WriteString("</array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>StandardOutPath</key><string>")
		_ = xml.EscapeText(&b, []byte(filepath.Join(s.DataDir, "daemon.log")))
		b.WriteString("</string><key>StandardErrorPath</key><string>")
		_ = xml.EscapeText(&b, []byte(filepath.Join(s.DataDir, "daemon.log")))
		b.WriteString("</string></dict></plist>\n")
		return []byte(b.String()), nil
	case "linux":
		args := append([]string{s.Executable}, s.Args...)
		parts := make([]string, len(args))
		for i, arg := range args {
			parts[i] = systemdQuote(arg)
		}
		return []byte("[Unit]\nDescription=OwnBot local daemon\nAfter=default.target\n\n[Service]\nType=simple\nExecStart=" + strings.Join(parts, " ") + "\nRestart=on-failure\nRestartSec=2\nUMask=0077\n\n[Install]\nWantedBy=default.target\n"), nil
	default:
		return nil, unsupported(s.osName())
	}
}

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "$", "$$")
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return "\"" + value + "\""
}
