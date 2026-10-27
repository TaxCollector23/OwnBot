package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"ownbot/internal/server"
	"path/filepath"
	"strings"
)

func main() {
	dir := os.Getenv("OWNBOT_DATA_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		dir = filepath.Join(home, ".ownbot")
	}
	addr := os.Getenv("OWNBOT_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatalf("invalid OWNBOT_ADDR %q: %v", addr, err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		log.Fatal("OWNBOT_ADDR must be a loopback IP address")
	}
	app, err := server.New(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	log.Printf("ownbotd listening on http://%s (state: %s)", addr, dir)
	log.Fatal(http.ListenAndServe(addr, app.Handler()))
}
