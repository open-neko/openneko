package serve

import (
	"io"
	"testing"
)

func TestRunnerLogSeesEachThreadFailure(t *testing.T) {
	for _, line := range []string{
		"fatal error: thread exhaustion",
		"runtime: failed to create new OS thread (have 12 already; errno=11)",
		"fatal error: newosproc",
		"runtime/cgo: pthread_create failed: Resource temporarily unavailable",
	} {
		l := newRunnerLog(io.Discard, "agent runner 1")
		_, _ = l.Write([]byte(line + "\nSIGABRT: abort\n"))
		if !l.outOfThreads() {
			t.Errorf("%q does not read as a thread failure", line)
		}
	}
	l := newRunnerLog(io.Discard, "agent runner 1")
	_, _ = l.Write([]byte("panic: runtime error: index out of range\n"))
	if l.outOfThreads() {
		t.Error("a panic reads as a thread failure")
	}
}
