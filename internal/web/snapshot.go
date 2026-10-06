package web

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/metricdef"
	"github.com/scottmbaker/topswatch/internal/module"
)

// --- Theme (mirrors style.css) ---

var (
	cBg      = color.RGBA{0x0d, 0x11, 0x17, 0xff}
	cBgCard  = color.RGBA{0x16, 0x1b, 0x22, 0xff}
	cBorder  = color.RGBA{0x30, 0x36, 0x3d, 0xff}
	cText    = color.RGBA{0xe6, 0xed, 0xf3, 0xff}
	cTextDim = color.RGBA{0x8b, 0x94, 0x9e, 0xff}
	cAccent  = color.RGBA{0x58, 0xa6, 0xff, 0xff}
	// cBarFree is the "free" segment of the memory bar: lighter than the
	// card background so the empty part of the bar reads as a bar.
	cBarFree = color.RGBA{0x2d, 0x33, 0x3b, 0xff}
	// Per-metric series colors come from metricdef (see hexColor).
)

// hexColor parses a metricdef "#rrggbb" color into the renderer's RGBA.
// Falls back to the text color on malformed input so a bad table entry
// degrades visibly rather than panicking.
func hexColor(h string) color.RGBA {
	if len(h) != 7 || h[0] != '#' {
		return cText
	}
	var r, g, b uint8
	if _, err := fmt.Sscanf(h[1:], "%02x%02x%02x", &r, &g, &b); err != nil {
		return cText
	}
	return color.RGBA{r, g, b, 0xff}
}

// snapNow is the clock used for the header timestamp. Tests override it so
// the rendered image is deterministic.
var snapNow = time.Now

// --- HTTP handler ---

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	hist := s.coll.History()
	devices := s.coll.Devices()

	img := renderSnapshot(devices, hist)

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	if err := jpeg.Encode(w, img, &jpeg.Options{Quality: 85}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// --- Renderer ---

const (
	snapW       = 800
	snapPad     = 14
	headerH     = 28
	sectionHdrH = 18
	cardH       = 60
	chartH      = 100
	infoH       = 110
	moduleGap   = 8
	memBarH     = 18 // stacked system-memory bar under the CPU cards
)

// moduleHeight is a module section's height; the CPU section carries the
// memory bar when the daemon reports system memory.
func moduleHeight(mod string, hasMem bool) int {
	h := sectionHdrH + cardH + 6 + chartH + moduleGap
	if mod == "cpu" && hasMem {
		h += memBarH
	}
	return h
}

func renderSnapshot(devices map[string]module.DeviceInfo, hist []collector.Sample) image.Image {
	// Determine which modules to render (only those present in devices).
	modules := make([]string, 0, 3)
	for _, name := range metricdef.Order {
		if _, ok := devices[name]; ok {
			modules = append(modules, name)
		}
	}

	// Latest sample feeds the memory bar.
	var mem metricdef.MemoryBar
	hasMem := false
	if len(hist) > 0 {
		last := hist[len(hist)-1]
		mem, hasMem = metricdef.Memory(last.Metrics["cpu"], last.Metrics["gpu"])
	}

	// Compute height.
	h := snapPad + headerH + infoH + snapPad
	for _, name := range modules {
		h += moduleHeight(name, hasMem)
	}

	img := image.NewRGBA(image.Rect(0, 0, snapW, h))
	c := &snapCanvas{img: img, w: snapW, h: h}
	c.fill(0, 0, snapW, h, cBg)

	// --- Header ---
	c.bigText(snapPad, snapPad, "TopsWatch", cAccent, 2)
	subtitle := snapNow().Format("2006-01-02 15:04:05")
	if len(hist) > 0 {
		samples := len(hist)
		subtitle += fmt.Sprintf("   |   %d samples", samples)
	}
	c.textRight(snapW-snapPad, snapPad+13, subtitle, cTextDim)
	c.line(snapPad, snapPad+headerH-4, snapW-snapPad, snapPad+headerH-4, cBorder)

	// --- Module sections ---
	y := snapPad + headerH
	for _, name := range modules {
		drawModuleSection(c, name, y, hist, mem, hasMem)
		y += moduleHeight(name, hasMem)
	}

	// --- Info panel ---
	drawInfoPanel(c, snapPad, y, snapW-2*snapPad, infoH-snapPad, devices)

	return img
}

func drawModuleSection(c *snapCanvas, mod string, y int, hist []collector.Sample, mem metricdef.MemoryBar, hasMem bool) {
	defs := metricdef.Cards[mod]
	if len(defs) == 0 {
		return
	}

	// Section header
	c.text(snapPad, y+13, strings.ToUpper(mod), cTextDim)
	c.line(snapPad, y+sectionHdrH-2, snapW-snapPad, y+sectionHdrH-2, cBorder)

	// Cards row
	cardsY := y + sectionHdrH
	cardsW := snapW - 2*snapPad
	gap := 6
	n := len(defs)
	w := (cardsW - gap*(n-1)) / n
	for i, def := range defs {
		cx := snapPad + i*(w+gap)
		drawCard(c, cx, cardsY, w, cardH, def, mod, hist)
	}

	// Memory bar (CPU section only)
	chartY := cardsY + cardH + 6
	if mod == "cpu" && hasMem {
		drawMemoryBar(c, snapPad, chartY, snapW-2*snapPad, memBarH-6, mem)
		chartY += memBarH
	}

	// Chart
	drawChart(c, snapPad, chartY, snapW-2*snapPad, chartH, mod, hist)
}

// drawMemoryBar draws system RAM as a stacked bar: GPU buffers (part of
// "used" on an integrated GPU), the rest of used, and free, with the
// numbers to the right.
func drawMemoryBar(c *snapCanvas, x, y, w, h int, mb metricdef.MemoryBar) {
	label := "MEM"
	text := fmt.Sprintf("%.1f GB used", mb.Used)
	if mb.GPU > 0 {
		text += fmt.Sprintf(" (GPU %.1f)", mb.GPU)
	}
	text += fmt.Sprintf("  %.1f free of %.0f GB", mb.Total-mb.Used, mb.Total)
	labelW := len(label)*7 + 8
	textW := len(text)*7 + 8
	barX := x + labelW
	barW := w - labelW - textW
	if barW < 40 {
		return
	}
	c.text(x, y+h-3, label, cTextDim)
	c.fill(barX, y, barW, h, cBarFree)
	c.rectBorder(barX, y, barW, h, cTextDim)
	frac := func(gb float64) int { return int(gb / mb.Total * float64(barW-2)) }
	g := frac(mb.GPU)
	u := frac(mb.Used) - g
	if u < 0 {
		u = 0
	}
	c.fill(barX+1, y+1, g, h-2, hexColor(metricdef.ColorPurple))
	c.fill(barX+1+g, y+1, u, h-2, cAccent)
	// A 1px seam between GPU and the rest of "used" so two filled
	// segments stay distinguishable at a glance.
	if g > 0 && u > 0 {
		c.fill(barX+1+g, y+1, 1, h-2, cBg)
	}
	c.text(barX+barW+8, y+h-3, text, cText)
}

func drawCard(c *snapCanvas, x, y, w, h int, def metricdef.Def, mod string, hist []collector.Sample) {
	// Background
	c.fill(x, y, w, h, cBgCard)
	c.rectBorder(x, y, w, h, cBorder)

	// Label
	c.text(x+8, y+12, def.Short, cTextDim)

	// Value
	last, ok := lastMetricValue(hist, mod, def)
	if def.Transform != nil && ok {
		last = def.Transform(last)
	}
	val := "--"
	if ok {
		val = formatVal(last, def.Precision)
	}
	c.bigText(x+8, y+18, val, hexColor(def.Color), 2)
	// Unit
	unitX := x + 8 + len(val)*7*2 + 4
	c.text(unitX, y+34, def.Unit, cTextDim)

	// Sparkline
	sparkY := y + h - 16
	sparkH := 12
	sparkX := x + 8
	sparkW := w - 16
	drawSparkline(c, sparkX, sparkY, sparkW, sparkH, mod, def, hist, 60)
}

func drawSparkline(c *snapCanvas, x, y, w, h int, mod string, def metricdef.Def, hist []collector.Sample, n int) {
	pts := metricSeries(hist, mod, def)
	if len(pts) > n {
		pts = pts[len(pts)-n:]
	}
	if len(pts) < 2 {
		return
	}
	mn, mx := minMax(pts)
	if def.Max > 0 {
		mn = 0
		if mx < def.Max {
			mx = def.Max
		}
	}
	if mx == mn {
		mx = mn + 1
	}
	prevX, prevY := -1, -1
	for i, v := range pts {
		px := x + i*w/(len(pts)-1)
		py := y + h - int((v-mn)/(mx-mn)*float64(h))
		if prevX >= 0 {
			c.line(prevX, prevY, px, py, hexColor(def.Color))
		}
		prevX, prevY = px, py
	}
}

func drawChart(c *snapCanvas, x, y, w, h int, mod string, hist []collector.Sample) {
	// Panel
	c.fill(x, y, w, h, cBgCard)
	c.rectBorder(x, y, w, h, cBorder)

	pad := 8
	cx := x + pad
	cy := y + pad + 12
	cw := w - 2*pad
	ch := h - 2*pad - 12

	// Title
	c.text(x+pad, y+pad+10, strings.ToUpper(mod)+" METRICS - 5 MIN", cTextDim)

	// Gridlines
	for g := 1; g < 4; g++ {
		gy := cy + g*ch/4
		c.lineH(cx, gy, cx+cw, cBorder)
	}

	// Each metric in module
	defs := metricdef.Cards[mod]
	for _, def := range defs {
		// Card-only metrics: their scales have nothing to do with the
		// util/freq/power/temp lines.
		if !def.Chart {
			continue
		}
		pts := metricSeries(hist, mod, def)
		if len(pts) > 300 {
			pts = pts[len(pts)-300:]
		}
		if len(pts) < 2 {
			continue
		}
		mn, mx := minMax(pts)
		if def.Max > 0 {
			mn = 0
			if mx < def.Max {
				mx = def.Max
			}
		}
		if mx == mn {
			mx = mn + 1
		}
		prevX, prevY := -1, -1
		for i, v := range pts {
			px := cx + i*cw/(len(pts)-1)
			py := cy + ch - int((v-mn)/(mx-mn)*float64(ch))
			if prevX >= 0 {
				c.line(prevX, prevY, px, py, hexColor(def.Color))
			}
			prevX, prevY = px, py
		}
	}
}

func drawInfoPanel(c *snapCanvas, x, y, w, h int, devices map[string]module.DeviceInfo) {
	c.fill(x, y, w, h, cBgCard)
	c.rectBorder(x, y, w, h, cBorder)

	cols := 3
	colW := (w - 16) / cols
	for i, name := range metricdef.Order {
		dev, ok := devices[name]
		if !ok {
			continue
		}
		cx := x + 8 + i*colW
		cy := y + 8
		c.text(cx, cy+10, strings.ToUpper(name), cAccent)
		c.lineH(cx, cy+14, cx+colW-12, cBorder)
		row := cy + 28
		switch name {
		case "cpu":
			drawInfoRow(c, cx, row, colW-12, "Model", truncate(dev.Name, 24))
			row += 12
			drawInfoRow(c, cx, row, colW-12, "Cores",
				fmt.Sprintf("%sP/%sT", dev.Extra["cores"], dev.Extra["threads"]))
			row += 12
			drawInfoRow(c, cx, row, colW-12, "Arch", dev.Extra["arch"])
		case "npu":
			drawInfoRow(c, cx, row, colW-12, "Device", truncate(dev.Name, 24))
			row += 12
			drawInfoRow(c, cx, row, colW-12, "PCI", dev.PCIDevice)
			row += 12
			drawInfoRow(c, cx, row, colW-12, "Driver", truncate(dev.DriverVersion, 22))
			row += 12
			drawInfoRow(c, cx, row, colW-12, "Firmware", truncate(dev.FirmwareVersion, 22))
		case "gpu":
			drawInfoRow(c, cx, row, colW-12, "Device", truncate(dev.Name, 24))
			row += 12
			drawInfoRow(c, cx, row, colW-12, "PCI", dev.PCIDevice)
			row += 12
			drawInfoRow(c, cx, row, colW-12, "Driver", dev.Extra["driver"])
		}
	}
}

func drawInfoRow(c *snapCanvas, x, y, w int, label, value string) {
	c.text(x, y, label, cTextDim)
	c.textRight(x+w, y, value, cText)
}

// --- Helpers ---

// sampleMetricValue returns the raw value for (mod, def) within one sample.
func sampleMetricValue(s collector.Sample, mod string, def metricdef.Def) (float64, bool) {
	return metricdef.Raw(s.Metrics[mod], def)
}

// lastMetricValue returns the most recent value matching (mod, def).
func lastMetricValue(hist []collector.Sample, mod string, def metricdef.Def) (float64, bool) {
	for i := len(hist) - 1; i >= 0; i-- {
		if v, ok := sampleMetricValue(hist[i], mod, def); ok {
			return v, true
		}
	}
	return 0, false
}

// metricSeries returns the chronological sequence of values for
// (mod, def) across the history.
func metricSeries(hist []collector.Sample, mod string, def metricdef.Def) []float64 {
	out := make([]float64, 0, len(hist))
	for _, s := range hist {
		v, ok := sampleMetricValue(s, mod, def)
		if !ok {
			continue
		}
		if def.Transform != nil {
			v = def.Transform(v)
		}
		out = append(out, v)
	}
	return out
}

func minMax(vs []float64) (float64, float64) {
	mn, mx := vs[0], vs[0]
	for _, v := range vs {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}

func formatVal(v float64, prec int) string {
	return fmt.Sprintf("%.*f", prec, v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// --- Drawing primitives ---

type snapCanvas struct {
	img  *image.RGBA
	w, h int
}

func (c *snapCanvas) fill(x, y, w, h int, col color.Color) {
	r := image.Rect(x, y, x+w, y+h).Intersect(c.img.Bounds())
	draw.Draw(c.img, r, &image.Uniform{C: col}, image.Point{}, draw.Src)
}

func (c *snapCanvas) rectBorder(x, y, w, h int, col color.Color) {
	c.fill(x, y, w, 1, col)
	c.fill(x, y+h-1, w, 1, col)
	c.fill(x, y, 1, h, col)
	c.fill(x+w-1, y, 1, h, col)
}

func (c *snapCanvas) line(x0, y0, x1, y1 int, col color.Color) {
	dx := abs(x1 - x0)
	sx := 1
	if x0 > x1 {
		sx = -1
	}
	dy := -abs(y1 - y0)
	sy := 1
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		if x0 >= 0 && x0 < c.w && y0 >= 0 && y0 < c.h {
			c.img.Set(x0, y0, col)
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func (c *snapCanvas) lineH(x0, y, x1 int, col color.Color) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	c.fill(x0, y, x1-x0, 1, col)
}

func (c *snapCanvas) text(x, y int, s string, col color.Color) {
	d := &font.Drawer{
		Dst:  c.img,
		Src:  &image.Uniform{C: col},
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func (c *snapCanvas) textRight(xRight, y int, s string, col color.Color) {
	w := len(s) * 7
	c.text(xRight-w, y, s, col)
}

// bigText draws s into a temporary image at 1x then nearest-neighbor scales
// it onto the canvas at the given integer scale factor.
func (c *snapCanvas) bigText(x, y int, s string, col color.Color, scale int) {
	if scale < 1 {
		scale = 1
	}
	w := len(s) * 7
	h := 13
	if w == 0 {
		return
	}
	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	d := &font.Drawer{
		Dst:  tmp,
		Src:  &image.Uniform{C: col},
		Face: basicfont.Face7x13,
		Dot:  fixed.P(0, 11),
	}
	d.DrawString(s)
	for sy := 0; sy < h; sy++ {
		for sx := 0; sx < w; sx++ {
			pix := tmp.RGBAAt(sx, sy)
			if pix.A == 0 {
				continue
			}
			dx0 := x + sx*scale
			dy0 := y + sy*scale
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					px, py := dx0+dx, dy0+dy
					if px >= 0 && px < c.w && py >= 0 && py < c.h {
						c.img.Set(px, py, pix)
					}
				}
			}
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
