package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type closeCounter struct{ n atomic.Int32 }

func (c *closeCounter) Close() error { c.n.Add(1); return nil }
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	a := l.Addr().String()
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	return a
}
func TestRunRejectsNonLoopbackAndClosesResource(t *testing.T) {
	resource := &closeCounter{}
	err := Run(context.Background(), Config{DataDir: t.TempDir(), ListenAddr: "0.0.0.0:8765", ShutdownTimeoutSeconds: 1}, http.NotFoundHandler(), resource, NewLogger(io.Discard))
	if err == nil {
		t.Fatal("non-loopback listener accepted")
	}
	if resource.n.Load() != 1 {
		t.Fatalf("resource closes=%d", resource.n.Load())
	}
}
func TestRunGracefullyDrainsHTTPAndClosesResource(t *testing.T) {
	addr := freeLoopbackAddr(t)
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	})
	resource := &closeCounter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{DataDir: t.TempDir(), ListenAddr: addr, ShutdownTimeoutSeconds: 3}, handler, resource, NewLogger(io.Discard))
	}()
	client := &http.Client{Timeout: 5 * time.Second}
	ready := false
	for i := 0; i < 100; i++ {
		response, err := client.Get("http://" + addr + "/ready")
		if err == nil {
			_ = response.Body.Close()
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatal("server did not become ready")
	}
	responseDone := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + addr + "/block")
		if response != nil {
			_ = response.Body.Close()
		}
		responseDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned before active request drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-responseDone; err != nil {
		t.Fatalf("active request: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown timed out")
	}
	if resource.n.Load() != 1 {
		t.Fatalf("resource closes=%d", resource.n.Load())
	}
}
func TestRunReturnsServeErrorAndClosesResource(t *testing.T) {
	resource := &closeCounter{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, Config{DataDir: t.TempDir(), SocketPath: "relative.sock", ShutdownTimeoutSeconds: 1}, http.NotFoundHandler(), resource, NewLogger(io.Discard))
	if err == nil {
		t.Fatal("relative socket path accepted")
	}
	if resource.n.Load() != 1 {
		t.Fatalf("resource closes=%d", resource.n.Load())
	}
}
