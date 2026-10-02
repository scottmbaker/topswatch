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
	"image/color"
	"image/jpeg"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottmbaker/topswatch/internal/client"
	"github.com/scottmbaker/topswatch/internal/energy"
)

// Options configures a GUI session.
type Options struct {
	Addr    string
	Refresh time.Duration
	// Energy adds the watt-hour panel (Start/Stop, Idle baseline, Clear)
	// under the dashboard. Off by default.
	Energy bool
	// Baseline is how long an idle-baseline capture lasts (default 10s).
	Baseline time.Duration
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

	var panel *energyPanel
	if opts.Energy {
		panel = newEnergyPanel(opts.Baseline)
		// Beside the dashboard on a wide window (a landscape handheld),
		// beneath it on a tall one.
		split := container.New(adaptiveSplit{}, img, panel.box)
		w.SetContent(container.NewBorder(nil, status, nil, nil, split))
		w.Resize(fyne.NewSize(1240, 700))
	} else {
		w.SetContent(container.NewBorder(nil, status, nil, nil, img))
		w.Resize(fyne.NewSize(820, 800))
	}

	w.Canvas().SetOnTypedKey(func(k *fyne.KeyEvent) {
		switch k.Name {
		case fyne.KeyEscape:
			w.Close()
		}
	})
	// Same keys as the TUI, so the panel can be driven without a pointer.
	w.Canvas().SetOnTypedRune(func(r rune) {
		switch r {
		case 'q':
			w.Close()
		case 's':
			if panel != nil {
				panel.session.Toggle()
				panel.render()
			}
		case 'b':
			if panel != nil {
				panel.session.StartBaseline()
				panel.render()
			}
		case 'c':
			if panel != nil {
				panel.session.Reset()
				panel.render()
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	w.SetOnClosed(cancel)
	go refreshLoop(ctx, c, opts.Refresh, img, status)
	if panel != nil {
		go panel.loop(ctx, c, opts.Refresh)
	}

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

// energyPanel is the optional stopwatch under the dashboard. It polls the
// daemon's latest sample for the cumulative energy counters; the session
// logic is shared with the TUI (internal/energy).
type energyPanel struct {
	box     *fyne.Container
	session *energy.Session
	start   *widget.Button
	table   *widget.Label
	state   *widget.Label
}

func newEnergyPanel(baseline time.Duration) *energyPanel {
	p := &energyPanel{session: energy.NewSession(baseline)}
	p.table = widget.NewLabel("")
	p.table.TextStyle = fyne.TextStyle{Monospace: true}
	p.state = widget.NewLabel("Energy: waiting for counters from the daemon…")
	p.state.Wrapping = fyne.TextWrapWord

	// Button callbacks run on the UI goroutine, as does render (via
	// fyne.Do from the poll loop), so the session needs no locking.
	// Each button carries its keyboard shortcut; the keys match the TUI.
	p.start = widget.NewButton("Start (s)", func() { p.session.Toggle(); p.render() })
	base := widget.NewButton("Idle baseline (b)", func() { p.session.StartBaseline(); p.render() })
	clear := widget.NewButton("Clear (c)", func() { p.session.Reset(); p.render() })

	// Reserve the table's full size up front (8 rows plus header, widest
	// with the baseline columns) so the layout does not jump when the first
	// measurement appears.
	widest := strings.Repeat("M", 70)
	tableSize := fyne.MeasureText(widest, theme.TextSize(), p.table.TextStyle)
	reserve := canvas.NewRectangle(color.Transparent)
	reserve.SetMinSize(fyne.NewSize(tableSize.Width+2*theme.Padding(), tableSize.Height*10))

	p.box = container.NewVBox(
		container.NewHBox(p.start, base, clear),
		p.state,
		container.NewStack(reserve, p.table),
	)
	return p
}

// adaptiveSplit lays out two objects, the dashboard image and the energy
// panel: side by side when the space is clearly wider than tall, stacked
// otherwise. The panel takes its minimum size and the image the rest.
type adaptiveSplit struct{}

func (adaptiveSplit) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) != 2 {
		return
	}
	img, panel := objs[0], objs[1]
	pm := panel.MinSize()
	if size.Width >= size.Height*1.25 && size.Width > pm.Width*1.6 {
		w := pm.Width
		img.Move(fyne.NewPos(0, 0))
		img.Resize(fyne.NewSize(size.Width-w, size.Height))
		panel.Move(fyne.NewPos(size.Width-w, 0))
		panel.Resize(fyne.NewSize(w, size.Height))
		return
	}
	h := pm.Height
	if h > size.Height {
		h = size.Height
	}
	img.Move(fyne.NewPos(0, 0))
	img.Resize(fyne.NewSize(size.Width, size.Height-h))
	panel.Move(fyne.NewPos(0, size.Height-h))
	panel.Resize(fyne.NewSize(size.Width, h))
}

func (adaptiveSplit) MinSize(objs []fyne.CanvasObject) fyne.Size {
	if len(objs) != 2 {
		return fyne.NewSize(0, 0)
	}
	im, pm := objs[0].MinSize(), objs[1].MinSize()
	// Small enough for either arrangement; the image scales to what is left.
	w, h := im.Width, im.Height
	if pm.Width > w {
		w = pm.Width
	}
	if pm.Height > h {
		h = pm.Height
	}
	return fyne.NewSize(w, h)
}

func (p *energyPanel) loop(ctx context.Context, c *client.Client, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if s, err := c.Latest(ctx); err == nil {
			r := energy.FromSample(s)
			fyne.Do(func() { p.session.Observe(r); p.render() })
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *energyPanel) render() {
	s := p.session
	if !s.Supported() {
		p.state.SetText("Energy: no counters from the daemon (it needs the power collector, running as root)")
		return
	}
	rep, has := s.Report()
	var status string
	switch s.State() {
	case energy.Running:
		p.start.SetText("Stop (s)")
		status = "Measuring " + energy.FormatDuration(rep.Elapsed)
	case energy.Stopped:
		p.start.SetText("Start (s)")
		status = "Stopped at " + energy.FormatDuration(rep.Elapsed)
	default:
		p.start.SetText("Start (s)")
		status = "Ready"
	}
	capturing, remaining, base := s.Baseline()
	switch {
	case capturing:
		status += fmt.Sprintf("  ·  capturing idle baseline, %.0fs left", remaining.Seconds())
	case base != nil:
		status += fmt.Sprintf("  ·  idle baseline captured (%.0fs)", base.Seconds)
	default:
		status += "  ·  no idle baseline"
	}
	if has {
		if notes := rep.Footnotes(); len(notes) > 0 {
			status += "  ·  " + notes[0]
		}
		p.table.SetText(rep.Table())
	} else {
		p.table.SetText("")
	}
	p.state.SetText(status)
}
