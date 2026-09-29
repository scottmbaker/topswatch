package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/testfixture"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":                        "http://localhost:9876",
		"localhost":               "http://localhost:9876",
		"nuc335.local":            "http://nuc335.local:9876",
		"nuc335.local:8080":       "http://nuc335.local:8080",
		"192.168.1.5":             "http://192.168.1.5:9876",
		"192.168.1.5:1234":        "http://192.168.1.5:1234",
		"[fe80::1%en0]:9876":      "http://[fe80::1%en0]:9876",
		"[fe80::1%en0]":           "http://[fe80::1%en0]:9876",
		"fe80::1%en0":             "http://[fe80::1%en0]:9876",
		"http://host.example":     "http://host.example:9876",
		"http://host.example:80/": "http://host.example:80",
		"https://host.example/x/": "https://host.example:9876/x",
	}
	for in, want := range cases {
		got, err := normalize(in)
		if err != nil {
			t.Errorf("normalize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"ftp://x", ":9876"} {
		if _, err := normalize(bad); err == nil {
			t.Errorf("normalize(%q): expected error", bad)
		}
	}
}

// fakeDaemon serves the subset of the real server's routes the client uses,
// with fixture data.
func fakeDaemon(t *testing.T, haveData bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(testfixture.Devices())
	})
	mux.HandleFunc("/api/metrics/latest", func(w http.ResponseWriter, r *http.Request) {
		if !haveData {
			http.Error(w, "no data yet", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(testfixture.Sample(7))
	})
	mux.HandleFunc("/api/metrics/ranges", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]string{"5min", "1h", "24h"})
	})
	mux.HandleFunc("/api/metrics/history", func(w http.ResponseWriter, r *http.Request) {
		mh := collector.NewMultiHistory()
		for i := 0; i < 30; i++ {
			mh.Ingest(testfixture.Sample(i))
		}
		_ = json.NewEncoder(w).Encode(mh.Range(r.URL.Query().Get("range")))
	})
	mux.HandleFunc("/snapshot.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8fakejpeg"))
	})
	mux.HandleFunc("/api/metrics/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			b, _ := json.Marshal(testfixture.Sample(i))
			_, _ = fmt.Fprintf(w, ": keepalive\ndata: %s\n\n", b)
			f.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEndpoints(t *testing.T) {
	srv := fakeDaemon(t, true)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	devs, err := c.Devices(ctx)
	if err != nil || devs["cpu"].Name == "" {
		t.Fatalf("Devices: %v %v", devs, err)
	}
	s, err := c.Latest(ctx)
	if err != nil || !s.Timestamp.Equal(testfixture.Epoch.Add(7*time.Second)) {
		t.Fatalf("Latest: %v %v", s.Timestamp, err)
	}
	if len(s.Metrics["gpu"]) == 0 {
		t.Fatal("Latest: no gpu metrics decoded")
	}
	rs, err := c.Ranges(ctx)
	if err != nil || len(rs) != 3 {
		t.Fatalf("Ranges: %v %v", rs, err)
	}
	h, err := c.History(ctx, "1h")
	if err != nil || len(h) != 2 {
		t.Fatalf("History(1h): %d entries, %v", len(h), err)
	}
	h, err = c.History(ctx, "")
	if err != nil || len(h) != 29 {
		t.Fatalf("History(default): %d entries, %v", len(h), err)
	}
	img, err := c.Snapshot(ctx)
	if err != nil || len(img) != 10 || img[0] != 0xff {
		t.Fatalf("Snapshot: %d bytes, %v", len(img), err)
	}
}

func TestLatestNoData(t *testing.T) {
	srv := fakeDaemon(t, false)
	c, _ := New(srv.URL)
	if _, err := c.Latest(context.Background()); err != ErrNoData {
		t.Fatalf("got %v, want ErrNoData", err)
	}
}

func TestStream(t *testing.T) {
	srv := fakeDaemon(t, true)
	c, _ := New(srv.URL)
	var got []time.Time
	err := c.Stream(context.Background(), func(s collector.Sample) {
		got = append(got, s.Timestamp)
	})
	if err == nil {
		t.Fatal("expected 'closed by daemon' error after server ended the stream")
	}
	if len(got) != 3 || !got[2].Equal(testfixture.Epoch.Add(2*time.Second)) {
		t.Fatalf("stream delivered %v", got)
	}
}

func TestStreamCancel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, _ := New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Stream(ctx, func(collector.Sample) {}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled stream returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after cancel")
	}
}
