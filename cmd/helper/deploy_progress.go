package helper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fprl/ship/internal/deployevent"
	"github.com/fprl/ship/internal/utils"
)

type deployProgressEmitter struct {
	enabled bool
	logs    bool
	out     io.Writer
	mu      sync.Mutex
}

func newDeployProgressEmitter(enabled, logs bool, out io.Writer) *deployProgressEmitter {
	return &deployProgressEmitter{enabled: enabled, logs: logs, out: out}
}

func (e *deployProgressEmitter) start(phase, detail string) func(error) {
	if e == nil {
		return func(error) {}
	}
	started := time.Now()
	e.write(deployevent.Event{Kind: deployevent.KindStart, Phase: phase, Detail: detail})
	return func(err error) {
		kind := deployevent.KindDone
		if err != nil {
			kind = deployevent.KindFail
		}
		e.write(deployevent.Event{Kind: kind, Phase: phase, Detail: detail, DurationMS: time.Since(started).Milliseconds()})
	}
}

func (e *deployProgressEmitter) log(phase, message string, scrubValues []string) {
	if e == nil || !e.enabled || !e.logs {
		return
	}
	message = utils.RedactCommandOutput(scrubText(message, scrubValues))
	e.write(deployevent.Event{Kind: deployevent.KindLog, Phase: phase, Message: message})
}

func (e *deployProgressEmitter) write(event deployevent.Event) {
	if e == nil || !e.enabled {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = deployevent.Write(e.out, event)
}

type deployLogWriter struct {
	emitter     *deployProgressEmitter
	phase       string
	scrubValues []string
	partial     string
}

func (w *deployLogWriter) Write(data []byte) (int, error) {
	w.partial += strings.ReplaceAll(string(data), "\r", "\n")
	for {
		line, rest, ok := strings.Cut(w.partial, "\n")
		if !ok {
			break
		}
		w.emitter.log(w.phase, line, w.scrubValues)
		w.partial = rest
	}
	return len(data), nil
}

func (w *deployLogWriter) flush() {
	if w.partial == "" {
		return
	}
	w.emitter.log(w.phase, w.partial, w.scrubValues)
	w.partial = ""
}

func runDeployCommand(emitter *deployProgressEmitter, phase string, scrubValues []string, timeout time.Duration, name string, args []string, cwd string) ([]byte, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
	}
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.Command(name, args...)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	stdoutLog := &deployLogWriter{emitter: emitter, phase: phase, scrubValues: scrubValues}
	stderrLog := &deployLogWriter{emitter: emitter, phase: phase, scrubValues: scrubValues}
	if emitter != nil && emitter.logs {
		cmd.Stdout = io.MultiWriter(&stdout, stdoutLog)
		cmd.Stderr = io.MultiWriter(&stderr, stderrLog)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

	runErr := cmd.Run()
	stdoutLog.flush()
	stderrLog.flush()
	if runErr == nil {
		return stdout.Bytes(), nil
	}
	cmdErr := &utils.CommandError{
		Name:     name,
		Args:     append([]string(nil), args...),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Err:      runErr,
		TimedOut: ctx != nil && ctx.Err() == context.DeadlineExceeded,
		Timeout:  timeout,
	}
	if emitter == nil || !emitter.logs {
		if tail := tailLines(scrubText(cmdErr.CombinedOutput(), scrubValues), 40); tail != "" {
			fmt.Fprintln(os.Stderr, tail)
		}
	}
	return nil, cmdErr
}
