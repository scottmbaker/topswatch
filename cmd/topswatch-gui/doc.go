//go:build !gui

// topswatch-gui is only built with `-tags gui` (see main.go). Without the
// tag this stub keeps `go build ./...` and `go vet ./...` working while
// avoiding Fyne's cgo and OpenGL/X11 requirements, and tells anyone who
// runs the untagged binary how to get the real one.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "topswatch-gui was built without the gui build tag; rebuild with: go build -tags gui ./cmd/topswatch-gui (or make gui)")
	os.Exit(1)
}
