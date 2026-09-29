//go:build gui

// topswatch-gui is a lightweight desktop viewer for a running topswatch
// daemon. Build with: go build -tags gui ./cmd/topswatch-gui
package main

import (
	"flag"
	"log"
	"time"

	"github.com/scottmbaker/topswatch/internal/gui"
)

func main() {
	connect := flag.String("connect", "", "daemon address (host, host:port, [v6]:port, or URL; default localhost:9876)")
	refresh := flag.Duration("refresh", time.Second, "how often to fetch a new dashboard image")
	flag.Parse()

	if err := gui.Run(gui.Options{Addr: *connect, Refresh: *refresh}); err != nil {
		log.Fatalf("[gui] %v", err)
	}
}
