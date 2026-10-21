package sysfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRereadSeesRewrittenValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "energy_uj")
	if err := os.WriteFile(p, []byte("100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := OpenUint(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	if v, _ := f.Uint(); v != 100 {
		t.Fatalf("got %d, want 100", v)
	}
	// Rewritten in place, as the collectors' tests do and as the kernel
	// regenerates an attribute on every read.
	if err := os.WriteFile(p, []byte("250\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Uint(); v != 250 {
		t.Fatalf("got %d after rewrite, want 250", v)
	}
	if err := os.WriteFile(p, []byte("7"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Uint(); v != 7 {
		t.Fatalf("got %d after shrink, want 7", v)
	}
}

func TestLargeFileGrowsBuffer(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stat")
	want := strings.Repeat("cpu0 1 2 3 4 5 6 7 8 9 10\n", 400) // ~10 KiB
	if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	for i := 0; i < 2; i++ {
		b, err := f.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != want {
			t.Fatalf("read %d bytes, want %d", len(b), len(want))
		}
	}
}

func TestExactlyBufferSized(t *testing.T) {
	p := filepath.Join(t.TempDir(), "page")
	want := strings.Repeat("x", initialBuf)
	if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	b, err := f.Bytes()
	if err != nil || string(b) != want {
		t.Fatalf("len %d err %v", len(b), err)
	}
}

func TestReadAt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "telem")
	if err := os.WriteFile(p, []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	var b [8]byte
	if err := f.ReadAt(b[:], 4); err != nil || string(b[:]) != "456789ab" {
		t.Fatalf("got %q err %v", b, err)
	}
	if err := f.ReadAt(b[:], 12); err == nil {
		t.Fatal("expected short read error")
	}
}

func TestParsers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v")
	if err := os.WriteFile(p, []byte(" -42 \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	if v, err := f.Int(); err != nil || v != -42 {
		t.Fatalf("Int: %d %v", v, err)
	}
	if v, err := f.Float(); err != nil || v != -42 {
		t.Fatalf("Float: %g %v", v, err)
	}
	if _, err := f.Uint(); err == nil {
		t.Fatal("Uint of a negative should fail")
	}
	if s, _ := ReadString(p); s != "-42" {
		t.Fatalf("ReadString: %q", s)
	}
	if _, err := OpenUint(p); err == nil {
		t.Fatal("OpenUint of a negative should fail")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected open error")
	}
}

func TestReadInto(t *testing.T) {
	p := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(p, []byte("1 (a b) S 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	b, err := ReadInto(p, buf)
	if err != nil || string(b) != "1 (a b) S 3\n" {
		t.Fatalf("got %q err %v", b, err)
	}
	if b, err := ReadInto(p, buf[:4]); err != nil || string(b) != "1 (a" {
		t.Fatalf("bounded: got %q err %v", b, err)
	}
	if _, err := ReadInto(filepath.Join(t.TempDir(), "x"), buf); err == nil {
		t.Fatal("expected error")
	}
}
