package web

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scottmbaker/topswatch/internal/testfixture"
)

var update = flag.Bool("update", false, "rewrite golden files")

const snapshotGolden = "testdata/snapshot_golden.png"

// TestRenderSnapshotGolden pins the rendered dashboard image. Any change
// to layout, colors, or metric selection must be accompanied by a
// deliberate `go test ./internal/web -update` and a review of the new PNG.
func TestRenderSnapshotGolden(t *testing.T) {
	snapNow = func() time.Time { return testfixture.Epoch.Add(120 * time.Second) }
	t.Cleanup(func() { snapNow = time.Now })

	got := renderSnapshot(testfixture.Devices(), testfixture.History(120))

	var buf bytes.Buffer
	if err := png.Encode(&buf, got); err != nil {
		t.Fatal(err)
	}

	if *update {
		if err := os.WriteFile(snapshotGolden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", snapshotGolden)
		return
	}

	wantBytes, err := os.ReadFile(snapshotGolden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	want, err := png.Decode(bytes.NewReader(wantBytes))
	if err != nil {
		t.Fatal(err)
	}
	if diff := imageDiff(got, want); diff != "" {
		out := filepath.Join(t.TempDir(), "snapshot_got.png")
		_ = os.WriteFile(out, buf.Bytes(), 0o644)
		t.Fatalf("snapshot differs from golden: %s\nactual written to %s", diff, out)
	}
}

func imageDiff(a, b image.Image) string {
	if a.Bounds() != b.Bounds() {
		return "bounds " + a.Bounds().String() + " vs " + b.Bounds().String()
	}
	bd := a.Bounds()
	n := 0
	first := ""
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				if first == "" {
					first = image.Pt(x, y).String()
				}
				n++
			}
		}
	}
	if n == 0 {
		return ""
	}
	return "pixels differ: " + itoa(n) + " (first at " + first + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

const enginesGolden = "testdata/snapshot_engines_golden.png"

// TestRenderSnapshotEnginesGolden pins the expanded GPU view
// (?gpu=engines): one bar per engine and the media frequency card.
func TestRenderSnapshotEnginesGolden(t *testing.T) {
	snapNow = func() time.Time { return testfixture.Epoch.Add(120 * time.Second) }
	t.Cleanup(func() { snapNow = time.Now })

	plain := renderSnapshot(testfixture.Devices(), testfixture.History(120))
	got := renderSnapshotOpts(testfixture.Devices(), testfixture.History(120), SnapshotOptions{GPUEngines: true})
	if want := plain.Bounds().Dy() + enginesPanelH(5); got.Bounds().Dy() != want {
		t.Fatalf("expanded height %d, want %d (plain %d + panel)", got.Bounds().Dy(), want, plain.Bounds().Dy())
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, got); err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(enginesGolden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", enginesGolden)
		return
	}
	wantBytes, err := os.ReadFile(enginesGolden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	want, err := png.Decode(bytes.NewReader(wantBytes))
	if err != nil {
		t.Fatal(err)
	}
	if diff := imageDiff(got, want); diff != "" {
		out := filepath.Join(t.TempDir(), "snapshot_engines_got.png")
		_ = os.WriteFile(out, buf.Bytes(), 0o644)
		t.Fatalf("engines snapshot differs from golden: %s\nactual written to %s", diff, out)
	}
}

// TestSnapshotHandlesEmptyHistory guards against panics on the first
// request after startup, before any sample exists.
func TestSnapshotHandlesEmptyHistory(t *testing.T) {
	img := renderSnapshot(testfixture.Devices(), nil)
	if img.Bounds().Dx() != snapW {
		t.Fatalf("width %d, want %d", img.Bounds().Dx(), snapW)
	}
	img = renderSnapshotOpts(testfixture.Devices(), nil, SnapshotOptions{GPUEngines: true})
	if img.Bounds().Dx() != snapW {
		t.Fatalf("expanded width %d, want %d", img.Bounds().Dx(), snapW)
	}
}
