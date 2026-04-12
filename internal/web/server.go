package web

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/scottmbaker/topswatch/internal/collector"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	coll     *collector.Collector
	promReg  *prometheus.Registry
	addr     string
}

func NewServer(coll *collector.Collector, addr string, port int) *Server {
	s := &Server{
		coll: coll,
		addr: fmt.Sprintf("%s:%d", addr, port),
	}
	s.setupPrometheus()
	return s
}

func (s *Server) setupPrometheus() {
	s.promReg = prometheus.NewRegistry()
	s.promReg.MustRegister(&promCollector{coll: s.coll})
}

func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()

	// Static files (web UI)
	staticContent, err := fs.Sub(staticFS, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(staticContent)))

	// API endpoints
	mux.HandleFunc("/api/metrics/latest", s.handleLatest)
	mux.HandleFunc("/api/metrics/history", s.handleHistory)
	mux.HandleFunc("/api/metrics/stream", s.handleSSE)
	mux.HandleFunc("/api/devices", s.handleDevices)
	mux.HandleFunc("/api/metrics/ranges", s.handleRanges)
	mux.HandleFunc("/snapshot.jpg", s.handleSnapshot)

	// Prometheus
	mux.Handle("/metrics", promhttp.HandlerFor(s.promReg, promhttp.HandlerOpts{}))

	log.Printf("[web] listening on %s", s.addr)
	return http.ListenAndServe(s.addr, mux)
}
