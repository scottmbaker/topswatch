// Package client is a small HTTP client for a running topswatch daemon.
// The TUI and GUI viewers use it so they need no hardware access and no
// elevated permissions: they attach to the daemon over its existing JSON
// and SSE endpoints, on localhost by default or on a remote host.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/module"
)

// DefaultAddr is where a viewer looks when no --connect is given.
const DefaultAddr = "localhost:9876"

const defaultPort = "9876"

// ErrNoData is returned by Latest before the daemon has taken its first
// sample (HTTP 503 from /api/metrics/latest).
var ErrNoData = errors.New("daemon has no data yet")

// Client talks to one daemon.
type Client struct {
	base string
	http *http.Client
}

// New builds a client for addr, which may be "host", "host:port",
// "[v6addr]:port", or a full "http://..." URL. A missing port defaults
// to 9876.
func New(addr string) (*Client, error) {
	base, err := normalize(addr)
	if err != nil {
		return nil, err
	}
	return &Client{
		base: base,
		http: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// normalize turns the user-supplied address into a "http://host:port" base.
func normalize(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = DefaultAddr
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", fmt.Errorf("invalid address %q: %w", addr, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("invalid address %q: scheme must be http or https", addr)
		}
		if u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), defaultPort)
		}
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawQuery, u.Fragment = "", ""
		return u.String(), nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port: bare hostname, IPv4, or bracketed/unbracketed IPv6.
		host = strings.Trim(addr, "[]")
		port = defaultPort
	}
	if host == "" {
		return "", fmt.Errorf("invalid address %q: empty host", addr)
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// Base returns the normalized base URL, for display.
func (c *Client) Base() string { return c.base }

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode == http.StatusServiceUnavailable {
		return ErrNoData
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Devices returns the daemon's static device info.
func (c *Client) Devices(ctx context.Context) (map[string]module.DeviceInfo, error) {
	var out map[string]module.DeviceInfo
	err := c.getJSON(ctx, "/api/devices", &out)
	return out, err
}

// Latest returns the most recent full sample.
func (c *Client) Latest(ctx context.Context) (collector.Sample, error) {
	var out collector.Sample
	err := c.getJSON(ctx, "/api/metrics/latest", &out)
	return out, err
}

// Ranges lists the daemon's history tiers in display order.
func (c *Client) Ranges(ctx context.Context) ([]string, error) {
	var out []string
	err := c.getJSON(ctx, "/api/metrics/ranges", &out)
	return out, err
}

// History returns the downsampled history for a tier ("5min", "1h", "24h").
// An empty name selects the daemon's default (short) tier.
func (c *Client) History(ctx context.Context, tier string) ([]collector.ReducedSample, error) {
	path := "/api/metrics/history"
	if tier != "" {
		path += "?range=" + url.QueryEscape(tier)
	}
	var out []collector.ReducedSample
	err := c.getJSON(ctx, path, &out)
	return out, err
}

// Stream subscribes to the daemon's SSE feed and calls fn for every
// sample until ctx is cancelled or the connection drops. It returns nil on
// cancellation and the transport error otherwise. Callers that want
// reconnection wrap it in a loop.
func (c *Client) Stream(ctx context.Context, fn func(collector.Sample)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/metrics/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	// No overall timeout on a long-lived stream.
	hc := &http.Client{Transport: c.http.Transport}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream: HTTP %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var data bytes.Buffer
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			// Event boundary.
			if data.Len() > 0 {
				var s collector.Sample
				if err := json.Unmarshal(data.Bytes(), &s); err == nil {
					fn(s)
				}
				data.Reset()
			}
		case bytes.HasPrefix(line, []byte("data:")):
			payload := bytes.TrimPrefix(line, []byte("data:"))
			payload = bytes.TrimPrefix(payload, []byte(" "))
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(payload)
		default:
			// Comments and other fields are ignored.
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("stream closed by daemon")
}

// Snapshot fetches the daemon's server-rendered dashboard image
// (/snapshot.jpg). The GUI viewer displays it directly, so the daemon's
// renderer is the single source of truth for the dashboard look.
func (c *Client) Snapshot(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/snapshot.jpg", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/snapshot.jpg: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
