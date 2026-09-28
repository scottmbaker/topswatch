package textout

import (
	"bytes"
	"flag"
	"os"
	"testing"

	"github.com/scottmbaker/topswatch/internal/testfixture"
)

var update = flag.Bool("update", false, "rewrite golden files")

const golden = "testdata/sample_golden.txt"

// TestPrintSampleGolden pins the `--text` output. Changes to labels,
// ordering, or formatting must be accompanied by a deliberate
// `go test ./internal/textout -update` and a review of the diff.
func TestPrintSampleGolden(t *testing.T) {
	var buf bytes.Buffer
	PrintSample(&buf, testfixture.Sample(42))

	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("text output differs from golden.\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[float64]string{
		0:                             "0 B",
		512:                           "512 B",
		2048:                          "2 KiB",
		1.5 * 1024 * 1024:             "1.5 MiB",
		3 * 1024 * 1024 * 1024:        "3.00 GiB",
		2 * 1024 * 1024 * 1024 * 1024: "2.00 TiB",
	}
	for in, want := range cases {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%v) = %q, want %q", in, got, want)
		}
	}
}
