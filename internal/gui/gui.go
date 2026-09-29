//go:build gui

// Package gui is a small desktop viewer for a running topswatch daemon.
// It shows the daemon's own server-rendered dashboard (/snapshot.jpg),
// refreshed on a timer, so it carries no rendering logic of its own and
// always matches the web snapshot. It is built only with `-tags gui`
// because Fyne needs cgo and OpenGL/X11 headers; the daemon binary stays
// static and dependency-free.
package gui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/scottmbaker/topswatch/internal/client"
)

// Options configures a GUI session.
type Options struct {
	Addr    string
	Refresh time.Duration
}

// Run opens the window and blocks until it is closed.
func Run(opts Options) error {
	c, err := client.New(opts.Addr)
	if err != nil {
		return err
	}
	if opts.Refresh <= 0 {
		opts.Refresh = time.Second
	}

	a := app.New()
	w := a.NewWindow("TopsWatch")

	img := canvas.NewImageFromImage(image.NewRGBA(image.Rect(0, 0, 800, 600)))
	img.FillMode = canvas.ImageFillContain
	img.ScaleMode = canvas.ImageScaleFastest
	img.SetMinSize(fyne.NewSize(400, 300))

	status := widget.NewLabel("connecting to " + c.Base() + " …")
	status.Truncation = fyne.TextTruncateEllipsis

	w.SetContent(container.NewBorder(nil, status, nil, nil, img))
	w.Resize(fyne.NewSize(820, 800))

	w.Canvas().SetOnTypedKey(func(k *fyne.KeyEvent) {
		switch k.Name {
		case fyne.KeyQ, fyne.KeyEscape:
			w.Close()
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	w.SetOnClosed(cancel)
	go refreshLoop(ctx, c, opts.Refresh, img, status)

	w.ShowAndRun()
	return nil
}

// refreshLoop fetches the snapshot every interval. UI mutations go
// through fyne.Do, which Fyne requires from non-main goroutines.
func refreshLoop(ctx context.Context, c *client.Client, every time.Duration, img *canvas.Image, status *widget.Label) {
	t := time.NewTicker(every)
	defer t.Stop()
	fetch := func() {
		data, err := c.Snapshot(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fyne.Do(func() { status.SetText(fmt.Sprintf("%s — error: %v", c.Base(), err)) })
			return
		}
		decoded, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			fyne.Do(func() { status.SetText(fmt.Sprintf("%s — bad image: %v", c.Base(), err)) })
			return
		}
		fyne.Do(func() {
			img.Image = decoded
			img.Refresh()
			status.SetText(fmt.Sprintf("%s — updated %s — refresh %s — q to quit",
				c.Base(), time.Now().Format("15:04:05"), every))
		})
	}
	fetch()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fetch()
		}
	}
}
