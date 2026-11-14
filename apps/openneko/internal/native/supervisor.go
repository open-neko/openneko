package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type ProcState struct {
	PID      int       `json:"pid"`
	Started  time.Time `json:"started"`
	Restarts int       `json:"restarts"`
	LastExit string    `json:"last_exit,omitempty"`
}

type State struct {
	PID     int                   `json:"pid"`
	Started time.Time             `json:"started"`
	WebAddr string                `json:"web_addr"`
	Procs   map[string]*ProcState `json:"procs"`
}

func ReadState(l Layout) (State, error) {
	raw, err := os.ReadFile(l.State())
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("read %s: %w", l.State(), err)
	}
	return s, nil
}

type Supervisor struct {
	Layout  Layout
	Procs   []Proc
	WebAddr string
	Log     io.Writer
	Grace   time.Duration
	Backoff time.Duration

	mu    sync.Mutex
	state State
}

// Run starts every process and starts each again when it stops, until ctx
// ends. Then it sends each a stop signal and kills what is left after Grace.
func (s *Supervisor) Run(ctx context.Context) error {
	unlock, err := Lock(s.Layout)
	if err != nil {
		return err
	}
	defer unlock()
	if s.Grace == 0 {
		s.Grace = 15 * time.Second
	}
	if s.Backoff == 0 {
		s.Backoff = time.Second
	}
	s.state = State{PID: os.Getpid(), Started: time.Now().UTC(), WebAddr: s.WebAddr, Procs: map[string]*ProcState{}}
	for _, p := range s.Procs {
		s.state.Procs[p.Name] = &ProcState{}
	}
	if err := s.save(); err != nil {
		return err
	}
	defer os.Remove(s.Layout.State())
	var wg sync.WaitGroup
	for _, p := range s.Procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.keep(ctx, p)
		}()
	}
	wg.Wait()
	return nil
}

func (s *Supervisor) keep(ctx context.Context, p Proc) {
	delay := s.Backoff
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx, p)
		if ctx.Err() != nil {
			return
		}
		msg := "exit 0"
		if err != nil {
			msg = err.Error()
		}
		s.update(p.Name, func(ps *ProcState) { ps.PID, ps.LastExit = 0, msg; ps.Restarts++ })
		fmt.Fprintf(s.Log, "openneko: %s stopped (%s); starting it again in %s\n", p.Name, msg, delay)
		if time.Since(started) > time.Minute {
			delay = s.Backoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func (s *Supervisor) runOnce(ctx context.Context, p Proc) error {
	logFile, err := os.OpenFile(s.Layout.Log(p.Name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(p.Args[0], p.Args[1:]...)
	cmd.Env = p.Env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = childAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	s.update(p.Name, func(ps *ProcState) { ps.PID, ps.Started = cmd.Process.Pid, time.Now().UTC() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	_ = terminate(cmd.Process)
	select {
	case err := <-done:
		return err
	case <-time.After(s.Grace):
		_ = cmd.Process.Kill()
		return <-done
	}
}

func (s *Supervisor) update(name string, f func(*ProcState)) {
	s.mu.Lock()
	f(s.state.Procs[name])
	s.mu.Unlock()
	if err := s.save(); err != nil {
		fmt.Fprintf(s.Log, "openneko: write %s: %v\n", s.Layout.State(), err)
	}
}

func (s *Supervisor) save() error {
	s.mu.Lock()
	raw, err := json.MarshalIndent(s.state, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Layout.Run(), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Layout.State())
}

var ErrNotRunning = errors.New("the OpenNeko processes are not running")

// Running reports the supervisor PID when a supervisor holds the lock.
func Running(l Layout) (int, bool) {
	if !Locked(l) {
		return 0, false
	}
	st, err := ReadState(l)
	if err != nil {
		return 0, true
	}
	return st.PID, true
}

// Stop signals the supervisor and waits until it lets go of the lock.
func Stop(l Layout, wait time.Duration) error {
	pid, ok := Running(l)
	if !ok {
		return ErrNotRunning
	}
	if pid == 0 {
		return fmt.Errorf("a supervisor holds %s but %s is unreadable", l.Lock(), filepath.Base(l.State()))
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := terminate(p); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if !Locked(l) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the supervisor (pid %d) did not stop in %s", pid, wait)
}
