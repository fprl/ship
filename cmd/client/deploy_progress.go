package client

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/fprl/ship/internal/deployevent"
)

const deployHeartbeatInterval = 15 * time.Second

type activeDeployProgress struct {
	phase      string
	detail     string
	startedAt  time.Time
	generation uint64
}

type shipProgress struct {
	mu            sync.Mutex
	out           io.Writer
	tty           bool
	logs          bool
	now           func() time.Time
	tickInterval  time.Duration
	last          time.Time
	active        *activeDeployProgress
	generation    uint64
	lastHeartbeat time.Time
	closed        bool
}

func newShipProgress(logs bool) *shipProgress {
	info, err := os.Stderr.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0 && isTerminalFD(os.Stderr.Fd())
	interval := deployHeartbeatInterval
	if tty {
		interval = time.Second
	}
	return newShipProgressRenderer(os.Stderr, tty, logs, time.Now, interval)
}

func newShipProgressRenderer(out io.Writer, tty, logs bool, now func() time.Time, tickInterval time.Duration) *shipProgress {
	current := now()
	return &shipProgress{out: out, tty: tty, logs: logs, now: now, tickInterval: tickInterval, last: current}
}

func (p *shipProgress) complete(name string, duration time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeCompletedLocked("✓", displayDeployText(name), duration)
	p.last = p.now()
}

func (p *shipProgress) timed(name string) {
	now := p.now()
	p.complete(name, now.Sub(p.last))
}

func (p *shipProgress) line(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearActiveLineLocked()
	fmt.Fprintln(p.out, displayDeployText(line))
	p.redrawActiveLocked()
	p.last = p.now()
}

func (p *shipProgress) event(event deployevent.Event) {
	switch event.Kind {
	case deployevent.KindStart:
		p.start(event)
	case deployevent.KindDone:
		p.finish(event, "✓")
	case deployevent.KindFail:
		p.finish(event, "✗")
	case deployevent.KindLog:
		p.log(event)
	}
}

func (p *shipProgress) start(event deployevent.Event) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.clearActiveLineLocked()
	p.generation++
	active := &activeDeployProgress{
		phase:      displayDeployText(event.Phase),
		detail:     displayDeployText(event.Detail),
		startedAt:  p.now(),
		generation: p.generation,
	}
	if active.detail == "" {
		active.detail = active.phase
	}
	p.active = active
	p.lastHeartbeat = active.startedAt
	if p.tty {
		p.redrawActiveLocked()
	} else {
		fmt.Fprintf(p.out, "… %s\n", active.detail)
	}
	interval := p.tickInterval
	generation := active.generation
	p.mu.Unlock()

	if interval > 0 {
		go p.heartbeat(generation, interval)
	}
}

func (p *shipProgress) heartbeat(generation uint64, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if !p.heartbeatTick(generation, interval) {
			return
		}
	}
}

func (p *shipProgress) heartbeatTick(generation uint64, interval time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.active == nil || p.active.generation != generation {
		return false
	}
	now := p.now()
	elapsed := now.Sub(p.active.startedAt)
	if p.tty {
		p.redrawActiveLocked()
	} else if now.Sub(p.lastHeartbeat) >= interval {
		fmt.Fprintf(p.out, "… %s %s\n", p.active.detail, formatPhaseDuration(elapsed))
		p.lastHeartbeat = now
	}
	return true
}

func (p *shipProgress) finish(event deployevent.Event, mark string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	detail := displayDeployText(event.Detail)
	duration := time.Duration(event.DurationMS) * time.Millisecond
	if p.active != nil {
		p.clearActiveLineLocked()
		if detail == "" {
			detail = p.active.detail
		}
		if duration <= 0 {
			duration = p.now().Sub(p.active.startedAt)
		}
	}
	if detail == "" {
		detail = displayDeployText(event.Phase)
	}
	p.generation++
	p.active = nil
	p.writeCompletedLocked(mark, detail, duration)
	p.last = p.now()
}

func (p *shipProgress) log(event deployevent.Event) {
	if !p.logs {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.clearActiveLineLocked()
	message := displayDeployLog(event.Message)
	if message != "" {
		fmt.Fprintf(p.out, "  [%s] %s\n", displayDeployText(event.Phase), message)
	}
	p.redrawActiveLocked()
}

func (p *shipProgress) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.clearActiveLineLocked()
	p.generation++
	p.active = nil
	p.closed = true
}

func (p *shipProgress) writeCompletedLocked(mark, detail string, duration time.Duration) {
	if duration > 0 {
		fmt.Fprintf(p.out, "%s %s %s\n", mark, detail, formatPhaseDuration(duration))
		return
	}
	fmt.Fprintf(p.out, "%s %s\n", mark, detail)
}

func (p *shipProgress) clearActiveLineLocked() {
	if p.tty && p.active != nil {
		fmt.Fprint(p.out, "\r\x1b[2K")
	}
}

func (p *shipProgress) redrawActiveLocked() {
	if !p.tty || p.active == nil {
		return
	}
	fmt.Fprintf(p.out, "\r\x1b[2K⠋ %s %s", p.active.detail, formatPhaseDuration(p.now().Sub(p.active.startedAt)))
}

func displayDeployText(value string) string {
	value = strings.TrimSpace(value)
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func displayDeployLog(value string) string {
	return displayDeployText(value)
}
