// Package measure implements `topswatch --measure -- command`: run a
// command and report the energy used while it ran, in the manner of
// time(1) or `perf stat`. It is a client of a running daemon and reads
// the daemon's cumulative energy counters before and after.
//
//nolint:errcheck // report output to an io.Writer; write errors are not actionable
package measure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/scottmbaker/topswatch/internal/client"
	"github.com/scottmbaker/topswatch/internal/collector"
	"github.com/scottmbaker/topswatch/internal/energy"
)

// Options configures a measurement.
type Options struct {
	Addr     string        // daemon address (see client.New)
	Baseline time.Duration // idle window measured before the command; 0 = none
	JSON     bool          // machine-readable report
	Command  []string      // command and arguments
	// For measures for a fixed time instead of running a command (the
	// workload is started elsewhere, e.g. a demo already on screen).
	For time.Duration
	// Report is where the result goes (stderr by default, like time(1), so
	// the command's own stdout stays clean).
	Report io.Writer
}

// Result is the JSON form of a measurement.
type Result struct {
	Command        []string      `json:"command"`
	ExitCode       int           `json:"exit_code"`
	CommandSeconds float64       `json:"command_seconds"`
	Daemon         string        `json:"daemon"`
	Report         energy.Report `json:"report"`
	Notes          []string      `json:"notes"`
}

// firstSampleTimeout bounds how long we wait for the daemon to deliver a
// sample; with the default 1s interval this is generous.
const firstSampleTimeout = 15 * time.Second

// Run executes the command and writes the report. It returns the
// command's exit code.
func Run(opts Options) (int, error) {
	if len(opts.Command) == 0 && opts.For <= 0 {
		return 2, errors.New("nothing to measure; usage: topswatch --measure [--baseline 10s] [--json] -- command [args...]   or   topswatch --measure --for 5m")
	}
	if opts.Report == nil {
		opts.Report = os.Stderr
	}
	c, err := client.New(opts.Addr)
	if err != nil {
		return 2, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stream readings. The channel is unbuffered-ish on purpose: next()
	// always wants the first sample that arrives *after* it is called, so
	// stale ones are drained first.
	readings := make(chan energy.Reading, 64)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- c.Stream(ctx, func(s collector.Sample) {
			select {
			case readings <- energy.FromSample(s):
			default:
			}
		})
	}()
	next := func() (energy.Reading, error) {
		for {
			select {
			case <-readings:
				continue
			default:
			}
			break
		}
		select {
		case r := <-readings:
			if !r.Available() {
				return r, fmt.Errorf("daemon at %s reports no energy counters (it needs the power collector, i.e. a current topswatch daemon running as root)", c.Base())
			}
			return r, nil
		case err := <-streamErr:
			if err == nil {
				err = errors.New("stream ended")
			}
			return energy.Reading{}, fmt.Errorf("cannot read from daemon at %s: %w", c.Base(), err)
		case <-time.After(firstSampleTimeout):
			return energy.Reading{}, fmt.Errorf("no sample from daemon at %s within %s", c.Base(), firstSampleTimeout)
		}
	}

	// --- optional idle baseline ---
	var base *energy.Report
	if opts.Baseline > 0 {
		if !opts.JSON {
			fmt.Fprintf(opts.Report, "topswatch: measuring idle baseline for %s (keep the device idle)...\n", opts.Baseline)
		}
		b0, err := next()
		if err != nil {
			return 2, err
		}
		deadline := time.Now().Add(opts.Baseline)
		var b1 energy.Reading
		for {
			if b1, err = next(); err != nil {
				return 2, err
			}
			if !time.Now().Before(deadline) {
				break
			}
		}
		rep := energy.Diff(b0, b1)
		base = &rep
	}

	// --- bracket the command ---
	start, err := next()
	if err != nil {
		return 2, err
	}

	exit := 0
	var cmdDur time.Duration
	what := strings.Join(opts.Command, " ")
	if len(opts.Command) > 0 {
		cmd := exec.Command(opts.Command[0], opts.Command[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		// Ctrl-C goes to the child (same process group); we stay alive to
		// report what was used up to that point.
		signal.Ignore(syscall.SIGINT)
		defer signal.Reset(syscall.SIGINT)

		t0 := time.Now()
		runErr := cmd.Run()
		cmdDur = time.Since(t0)
		if runErr != nil {
			var ee *exec.ExitError
			if errors.As(runErr, &ee) {
				exit = ee.ExitCode()
			} else {
				return 127, fmt.Errorf("cannot run %q: %w", opts.Command[0], runErr)
			}
		}
	} else {
		what = fmt.Sprintf("%s window", energy.FormatDuration(opts.For))
		if !opts.JSON {
			fmt.Fprintf(opts.Report, "topswatch: measuring for %s...\n", opts.For)
		}
		t0 := time.Now()
		time.Sleep(opts.For)
		cmdDur = time.Since(t0)
	}

	end, err := next()
	if err != nil {
		return exit, err
	}

	rep := energy.Diff(start, end)
	if base != nil {
		rep = rep.WithBaseline(*base)
	}
	notes := rep.Footnotes()
	if pad := rep.Elapsed - cmdDur; pad > 0 {
		notes = append(notes, fmt.Sprintf(
			"The window is %s longer than the command because readings align to daemon samples.",
			energy.FormatDuration(pad)))
	}

	if opts.JSON {
		enc := json.NewEncoder(opts.Report)
		enc.SetIndent("", "  ")
		return exit, enc.Encode(Result{
			Command: opts.Command, ExitCode: exit, CommandSeconds: cmdDur.Seconds(),
			Daemon: c.Base(), Report: rep, Notes: notes,
		})
	}

	if len(opts.Command) > 0 {
		fmt.Fprintf(opts.Report, "\ntopswatch: %s  (command ran %s, exit %d, window %s)\n\n",
			what, energy.FormatDuration(cmdDur), exit, energy.FormatDuration(rep.Elapsed))
	} else {
		fmt.Fprintf(opts.Report, "\ntopswatch: %s  (window %s)\n\n", what, energy.FormatDuration(rep.Elapsed))
	}
	fmt.Fprint(opts.Report, rep.Table())
	fmt.Fprintln(opts.Report)
	for _, n := range notes {
		fmt.Fprintln(opts.Report, "  "+n)
	}
	return exit, nil
}
