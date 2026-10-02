// Package tui is a terminal dashboard that attaches to a running topswatch
// daemon over HTTP. It needs no hardware access, so it works over a plain
// SSH session on the device or from another machine with --connect.
package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/scottmbaker/topswatch/internal/client"
	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/energy"
	"github.com/scottmbaker/topswatch/internal/metricdef"
	"github.com/scottmbaker/topswatch/internal/module"
)

// Options configures a TUI session.
type Options struct {
	// Addr is the daemon address (see client.New).
	Addr string
	// Refresh is the polling interval. Zero means follow the daemon's SSE
	// stream, i.e. redraw once per daemon sample.
	Refresh time.Duration
	// Energy enables the watt-hour panel and its keys (s start/stop, b
	// idle baseline, c clear). Off by default to keep the display quiet.
	Energy bool
	// Baseline is how long an idle-baseline capture lasts (default 10s).
	Baseline time.Duration
	// Record is the length of a timed measurement started with t
	// (default 5m).
	Record time.Duration
}

// Run starts the TUI and blocks until the user quits.
func Run(opts Options) error {
	c, err := client.New(opts.Addr)
	if err != nil {
		return err
	}
	m := newModel(c, opts)
	// Data arrives at most once per second, so a 60 fps render ticker is
	// pure overhead on a tool whose job is to stay out of the way.
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithFPS(10))
	_, err = p.Run()
	m.cancel()
	return err
}

// --- messages ---

type devicesMsg map[string]module.DeviceInfo
type historyMsg struct {
	tier string
	hist []collector.ReducedSample
}
type sampleMsg collector.Sample
type rangesMsg []string
type errMsg struct{ err error }
type pollTickMsg struct{}
type historyTickMsg struct{}

// --- model ---

type model struct {
	c      *client.Client
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc

	width, height int
	devices       map[string]module.DeviceInfo
	ranges        []string
	tierIdx       int
	store         *store
	warnings      []collector.Warning
	lastErr       string
	paused        bool
	lastSample    time.Time
	charts        *chartCache
	session       *energy.Session // nil unless Options.Energy
	mem           metricdef.MemoryBar
	hasMem        bool

	// stream mode
	samples chan collector.Sample
}

func newModel(c *client.Client, opts Options) *model {
	ctx, cancel := context.WithCancel(context.Background())
	var session *energy.Session
	if opts.Energy {
		session = energy.NewSession(opts.Baseline)
	}
	if opts.Record <= 0 {
		opts.Record = 5 * time.Minute
	}
	return &model{
		session: session,
		c:       c,
		opts:    opts,
		ctx:     ctx,
		cancel:  cancel,
		store:   newStore(),
		ranges:  []string{collector.TierShort},
		charts:  newChartCache(),
	}
}

func (m *model) tier() string { return m.ranges[m.tierIdx%len(m.ranges)] }

func (m *model) streaming() bool { return m.opts.Refresh <= 0 }

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.fetchDevices(), m.fetchRanges(), m.fetchHistory(m.tier())}
	if m.streaming() {
		m.samples = make(chan collector.Sample, 4)
		go m.streamLoop()
		cmds = append(cmds, m.waitSample())
	} else {
		cmds = append(cmds, m.fetchLatest())
	}
	return tea.Batch(cmds...)
}

// --- commands ---

func (m *model) fetchDevices() tea.Cmd {
	return func() tea.Msg {
		d, err := m.c.Devices(m.ctx)
		if err != nil {
			return errMsg{err}
		}
		return devicesMsg(d)
	}
}

func (m *model) fetchRanges() tea.Cmd {
	return func() tea.Msg {
		r, err := m.c.Ranges(m.ctx)
		if err != nil || len(r) == 0 {
			return nil
		}
		return rangesMsg(r)
	}
}

func (m *model) fetchHistory(tier string) tea.Cmd {
	return func() tea.Msg {
		h, err := m.c.History(m.ctx, tier)
		if err != nil {
			return errMsg{err}
		}
		return historyMsg{tier, h}
	}
}

func (m *model) fetchLatest() tea.Cmd {
	return func() tea.Msg {
		s, err := m.c.Latest(m.ctx)
		if err != nil {
			return errMsg{err}
		}
		return sampleMsg(s)
	}
}

func pollTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{} })
}

func historyTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return historyTickMsg{} })
}

func (m *model) waitSample() tea.Cmd {
	return func() tea.Msg {
		select {
		case s, ok := <-m.samples:
			if !ok {
				return nil
			}
			return sampleMsg(s)
		case <-m.ctx.Done():
			return nil
		}
	}
}

// streamLoop keeps the SSE subscription alive, reconnecting with backoff.
func (m *model) streamLoop() {
	backoff := time.Second
	for m.ctx.Err() == nil {
		err := m.c.Stream(m.ctx, func(s collector.Sample) {
			select {
			case m.samples <- s:
			default:
				// UI is behind; drop rather than block the reader.
			}
		})
		if m.ctx.Err() != nil {
			return
		}
		if err != nil {
			select {
			case m.samples <- collector.Sample{Warnings: []collector.Warning{{
				Module: "tui", Kind: "connection", Severity: "warning",
				Message: fmt.Sprintf("stream: %v (reconnecting)", err),
			}}}:
			default:
			}
		}
		select {
		case <-time.After(backoff):
		case <-m.ctx.Done():
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// --- update ---

// historyRefresh is how often non-live tiers (1h, 24h) are re-fetched.
// Their buckets are 10s and 5min wide, so anything faster is wasted.
const historyRefresh = 10 * time.Second

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "r":
			m.tierIdx = (m.tierIdx + 1) % len(m.ranges)
			m.store.reset()
			cmds := []tea.Cmd{m.fetchHistory(m.tier())}
			if m.tier() != collector.TierShort {
				cmds = append(cmds, historyTick(historyRefresh))
			}
			return m, tea.Batch(cmds...)
		case "p":
			m.paused = !m.paused
			return m, nil
		}
		if m.session != nil {
			switch msg.String() {
			case "s":
				m.session.Toggle()
			case "b":
				m.session.StartBaseline()
			case "c":
				m.session.Reset()
			case "t":
				m.session.StartFor(m.opts.Record)
			}
		}
		return m, nil

	case devicesMsg:
		m.devices = msg
		return m, nil

	case rangesMsg:
		cur := m.tier()
		m.ranges = msg
		m.tierIdx = 0
		for i, r := range m.ranges {
			if r == cur {
				m.tierIdx = i
			}
		}
		return m, nil

	case historyMsg:
		if msg.tier != m.tier() {
			return m, nil // stale: user already cycled
		}
		m.store.loadReduced(msg.hist)
		m.lastErr = ""
		return m, nil

	case historyTickMsg:
		if m.tier() == collector.TierShort {
			return m, nil
		}
		return m, tea.Batch(m.fetchHistory(m.tier()), historyTick(historyRefresh))

	case sampleMsg:
		s := collector.Sample(msg)
		next := m.pollNext()
		if m.streaming() {
			next = m.waitSample()
		}
		if s.Timestamp.IsZero() {
			// Synthetic connection warning from streamLoop.
			m.warnings = s.Warnings
			return m, next
		}
		m.lastErr = ""
		m.warnings = s.Warnings
		m.lastSample = s.Timestamp
		if m.session != nil {
			// Energy keeps counting while the charts are paused or showing
			// a longer history range.
			m.session.Observe(energy.FromSample(s))
		}
		if m.devices == nil && len(s.Devices) > 0 {
			// The initial /api/devices fetch failed (daemon was down or
			// unreachable); every sample carries the same info.
			m.devices = s.Devices
		}
		if !m.paused {
			m.mem, m.hasMem = metricdef.Memory(s.Metrics["cpu"], s.Metrics["gpu"])
		}
		if !m.paused && m.tier() == collector.TierShort {
			m.store.push(s.Timestamp, s.Metrics)
		}
		return m, next

	case pollTickMsg:
		return m, m.fetchLatest()

	case errMsg:
		if msg.err == client.ErrNoData {
			m.lastErr = "daemon has no data yet"
		} else {
			m.lastErr = msg.err.Error()
		}
		if !m.streaming() {
			return m, pollTick(m.opts.Refresh)
		}
		return m, nil

	}
	return m, nil
}

// pollNext is issued after every successful poll so the interval is
// measured from the end of one request to the start of the next.
func (m *model) pollNext() tea.Cmd {
	if m.streaming() {
		return nil
	}
	return pollTick(m.opts.Refresh)
}

// --- view ---

func (m *model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	mode := "stream"
	if !m.streaming() {
		mode = "poll " + m.opts.Refresh.String()
	}
	f := frame{
		charts:    m.charts,
		addr:      m.c.Base(),
		now:       time.Now(),
		tier:      m.tier(),
		mode:      mode,
		paused:    m.paused,
		err:       m.lastErr,
		devices:   m.devices,
		warnings:  m.warnings,
		store:     m.store,
		energy:    newEnergyView(m.session),
		recordFor: m.opts.Record,
		mem:       m.mem,
		hasMem:    m.hasMem,
	}
	extra := f.energy.height()
	if m.hasMem {
		extra++ // the memory bar line under the CPU cards
	}
	return render(f, computeLayout(m.width, m.height, len(presentModules(m.devices)), extra))
}
