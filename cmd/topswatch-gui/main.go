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
	energyPanel := flag.Bool("energy", false, "show the watt-hour panel (Start/Stop, Idle baseline, Clear; keys s, b, c)")
	baseline := flag.Duration("baseline", 0, "length of an idle-baseline capture in the energy panel (default 10s)")
	flag.Parse()

	if err := gui.Run(gui.Options{Addr: *connect, Refresh: *refresh, Energy: *energyPanel, Baseline: *baseline}); err != nil {
		log.Fatalf("[gui] %v", err)
	}
}
