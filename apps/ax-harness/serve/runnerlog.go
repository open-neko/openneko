package serve

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
)

const (
	maxRunnerLogLine = 4 << 10
	maxRunnerLog     = 256 << 10
)

// runnerLog labels each line that an agent runner writes to stderr, so a
// runner cannot write a line that reads as serve's own. It drops control
// characters and stops after maxRunnerLog bytes.
type runnerLog struct {
	mu      sync.Mutex
	out     io.Writer
	label   string
	partial []byte
	written int
	cut     bool
	threads bool
}

// runnerMallocEnv keeps glibc from giving each runner thread its own 64 MB
// arena; with cgo on a host with many CPUs, those arenas fill the runner's
// address-space cap before its thread cap.
const runnerMallocEnv = "MALLOC_ARENA_MAX=2"

// threadFailures are the Go runtime's lines when it cannot start a thread.
var threadFailures = []string{"fatal error: thread exhaustion", "runtime: failed to create new OS thread", "fatal error: newosproc", "runtime/cgo: pthread_create failed"}

// outOfThreads tells whether the runner wrote that it could not start a
// thread.
func (l *runnerLog) outOfThreads() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.threads
}

func newRunnerLog(out io.Writer, label string) *runnerLog {
	return &runnerLog{out: out, label: label}
}

func (l *runnerLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	for len(p) > 0 && !l.cut {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			room := maxRunnerLogLine - len(l.partial)
			if len(p) <= room {
				l.partial = append(l.partial, p...)
				break
			}
			l.partial = append(l.partial, p[:room]...)
			p = p[room:]
			l.emit()
			continue
		}
		l.partial = append(l.partial, p[:min(i, max(0, maxRunnerLogLine-len(l.partial)))]...)
		p = p[i+1:]
		l.emit()
	}
	return n, nil
}

// Close writes a last line that did not end in a newline.
func (l *runnerLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.partial) > 0 && !l.cut {
		l.emit()
	}
	return nil
}

func (l *runnerLog) emit() {
	line := strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == unicode.ReplacementChar || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, string(l.partial))
	l.partial = l.partial[:0]
	for _, f := range threadFailures {
		if strings.Contains(line, f) {
			l.threads = true
		}
	}
	if l.written+len(line) > maxRunnerLog {
		l.cut = true
		fmt.Fprintf(l.out, "%s: stderr cut at %d bytes\n", l.label, maxRunnerLog)
		return
	}
	l.written += len(line)
	fmt.Fprintf(l.out, "%s: %s\n", l.label, line)
}
