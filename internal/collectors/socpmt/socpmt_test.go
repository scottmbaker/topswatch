package socpmt

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func fakeRegion(t *testing.T, base, name, guid string, gtOffset int, raw uint64) {
	t.Helper()
	d := filepath.Join(base, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "guid"), []byte(guid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 0x1000)
	if gtOffset > 0 {
		binary.LittleEndian.PutUint64(buf[gtOffset:], raw)
	}
	if err := os.WriteFile(filepath.Join(d, "telem"), buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenPicksKnownGUID(t *testing.T) {
	base := t.TempDir()
	fakeRegion(t, base, "telem1", "0x3086100", 0, 0)           // unknown region, skipped
	fakeRegion(t, base, "telem2", "0x3086000", 0x668, 3*16384) // PTL, 3 J
	d, err := openUnder(base)
	if err != nil {
		t.Fatal(err)
	}
	if d.Generation() != "Panther Lake" {
		t.Fatalf("generation = %q", d.Generation())
	}
	if j, _ := d.GTEnergyJoules(); j != 3 {
		t.Fatalf("GT energy = %v J, want 3", j)
	}
}

func TestOpenRejectsUnknownOrMissing(t *testing.T) {
	base := t.TempDir()
	if _, err := openUnder(base); err == nil {
		t.Fatal("empty dir should fail")
	}
	fakeRegion(t, base, "telem1", "0xdeadbeef", 0, 0)
	if _, err := openUnder(base); err == nil {
		t.Fatal("unknown GUID should fail")
	}
}
