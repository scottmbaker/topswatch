package web

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	sample, ok := s.coll.Latest()
	if !ok {
		http.Error(w, "no data yet", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sample) //nolint:errcheck
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	// ?range=5min|1h|24h selects the tier. Defaults to short.
	rangeName := r.URL.Query().Get("range")
	history := s.coll.HistoryRange(rangeName)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history) //nolint:errcheck
}

func (s *Server) handleRanges(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.coll.AvailableRanges()) //nolint:errcheck
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.coll.Devices()) //nolint:errcheck
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := s.coll.Subscribe()
	defer s.coll.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case sample, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(sample)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data) //nolint:errcheck
			flusher.Flush()
		}
	}
}
