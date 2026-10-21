package procwalk

import (
	"os"
	"path/filepath"
	"testing"
)

const clientsSample = `             command  tgid dev master a   uid      magic                                                             name                   id
      systemd-logind   707   0   y    y     0          0                                                          <unset>                    8
         gnome-shell  2232   0   n    y  1001          1                                                          <unset>                   11
         Web Content 31337   0   n    y  1001          2                                                          <unset>                   12
             python3 68354 128   n    n  1000          0                                                          <unset>                   55
`

func TestParseDRMClients(t *testing.T) {
	got := parseDRMClients([]byte(clientsSample))
	want := []int{707, 2232, 31337, 68354}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if parseDRMClients([]byte("")) != nil || parseDRMClients([]byte("garbage\n1 2 3\n")) != nil {
		t.Fatal("expected nothing from input without a header")
	}
}

func TestDRMClientPIDs(t *testing.T) {
	root := t.TempDir()
	// Primary minor 0, its render-node alias 128 (ignored), and a second
	// card with a client of its own.
	for _, minor := range []string{"0", "128", "1"} {
		if err := os.MkdirAll(filepath.Join(root, minor), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "0", "clients"), []byte(clientsSample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "128", "clients"), []byte(clientsSample), 0o644); err != nil {
		t.Fatal(err)
	}
	card1 := "command  tgid dev master a   uid      magic name id\n   foo  4242   1   n    y  1001  0 <unset> 3\n"
	if err := os.WriteFile(filepath.Join(root, "1", "clients"), []byte(card1), 0o644); err != nil {
		t.Fatal(err)
	}
	pids, ok := drmClientPIDs(root)
	if !ok {
		t.Fatal("expected a readable list")
	}
	if len(pids) != 5 || !pids[707] || !pids[4242] || !pids[31337] {
		t.Fatalf("pids = %v", pids)
	}
	if _, ok := drmClientPIDs(filepath.Join(root, "missing")); ok {
		t.Fatal("missing debugfs must report no list")
	}
	// A device directory without a clients file (not root) is not a list.
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := drmClientPIDs(empty); ok {
		t.Fatal("unreadable clients file must report no list")
	}
}
