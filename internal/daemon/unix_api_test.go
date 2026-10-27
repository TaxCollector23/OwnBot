//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ownbot/internal/server"
)

func TestUnixHTTPTransportUsesLocalhostAndBearerAuth(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ob-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	stateDir := filepath.Join(dir, "state")
	app, err := server.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	tokenBytes, err := os.ReadFile(filepath.Join(stateDir, "api.token"))
	if err != nil {
		_ = app.Close()
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	socket := filepath.Join(dir, "ownbot.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{DataDir: stateDir, SocketPath: socket, ShutdownTimeoutSeconds: 5}, app.Handler(), app, NewLogger(io.Discard))
	}()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	var response *http.Response
	for i := 0; i < 100; i++ {
		response, err = client.Get("http://localhost/api/status")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("Unix API did not listen: %v", err)
	}
	_ = drainAndClose(response.Body)
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("localhost API status: %d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, "http://localhost/api/agents", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response, err = client.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = drainAndClose(response.Body)
	if response.StatusCode != http.StatusUnauthorized {
		cancel()
		t.Fatalf("sensitive read without bearer: %d", response.StatusCode)
	}
	request, err = http.NewRequest(http.MethodGet, "http://localhost/api/agents", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = client.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = drainAndClose(response.Body)
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("authenticated read: %d", response.StatusCode)
	}
	transport.CloseIdleConnections()
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("Unix daemon shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Unix daemon did not shut down")
	}
	if _, err = os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("owned socket remains after shutdown: %v", err)
	}
}

func drainAndClose(rc io.ReadCloser) error {
	_, err := io.Copy(io.Discard, rc)
	closeErr := rc.Close()
	if err != nil {
		return err
	}
	return closeErr
}
