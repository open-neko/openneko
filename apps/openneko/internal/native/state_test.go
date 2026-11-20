package native

import (
	"sync"
	"testing"
	"time"
)

func TestASlowSaveDoesNotOverwriteALaterOne(t *testing.T) {
	l := preparedLayout(t)
	s := &Supervisor{Layout: l, Log: &syncBuffer{}, state: State{Procs: map[string]*ProcState{"serve": {}, "web": {}}}}
	slow := make(chan struct{})
	var once sync.Once
	renameState = func(from, to string) error {
		first := false
		once.Do(func() { first = true })
		if first {
			close(slow)
			time.Sleep(300 * time.Millisecond)
		}
		return replaceFile(from, to)
	}
	t.Cleanup(func() { renameState = replaceFile })

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.update("serve", func(ps *ProcState) { ps.PID = 11 })
	}()
	<-slow
	s.update("web", func(ps *ProcState) { ps.PID = 22 })
	<-done
	st, err := ReadState(l)
	if err != nil {
		t.Fatal(err)
	}
	if st.Procs["serve"].PID != 11 || st.Procs["web"].PID != 22 {
		t.Fatalf("the state file gives serve %d and web %d, want 11 and 22", st.Procs["serve"].PID, st.Procs["web"].PID)
	}
}
