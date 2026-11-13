package webfront

import (
	"context"
	"errors"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"
)

const askDefaultConcurrency = 2

var (
	agentSlotHeartbeat = 60 * time.Second
	runEventFlushIdle  = 250 * time.Millisecond
	TurnStopGrace      = 5 * time.Second
)

var errTurnShutdown = errors.New("The server stopped before the turn finished.")

// agentSlots is acquireAgentSlot() in packages/llm/src/work/agent-slots.ts:
// a process-wide limit on running turns, first come first served.
type agentSlots struct {
	mu      sync.Mutex
	limit   int
	active  int
	waiting []chan struct{}
}

func defaultAgentSlotLimit() int {
	if n, ok := jsNumber(os.Getenv("OPENNEKO_AGENT_CONCURRENCY")); ok && n >= 1 && n == math.Trunc(n) && n <= math.MaxInt32 {
		return int(n)
	}
	if strings.Contains(os.Getenv("OPENNEKO_SANDBOX_OWNER"), "worker") {
		return agentDefaultGlobalCap
	}
	return askDefaultConcurrency
}

func (s *agentSlots) admitNextLocked() {
	if s.limit == 0 {
		s.limit = defaultAgentSlotLimit()
	}
	for s.active < s.limit && len(s.waiting) > 0 {
		s.active++
		close(s.waiting[0])
		s.waiting = s.waiting[1:]
	}
}

func (s *agentSlots) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			s.admitNextLocked()
			s.mu.Unlock()
		})
	}
}

// acquire holds one slot. A turn that must wait reports it once through
// onWait and refreshes its run row through heartbeat, so the stale-run sweep
// leaves it alone.
func (s *agentSlots) acquire(ctx context.Context, onWait func() error, heartbeat func()) (func(), error) {
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	s.mu.Lock()
	if s.limit == 0 {
		s.limit = defaultAgentSlotLimit()
	}
	if s.active < s.limit && len(s.waiting) == 0 {
		s.active++
		s.mu.Unlock()
		return s.releaseFunc(), nil
	}
	s.mu.Unlock()
	if err := onWait(); err != nil {
		return nil, err
	}
	admitted := make(chan struct{})
	s.mu.Lock()
	s.waiting = append(s.waiting, admitted)
	s.admitNextLocked()
	s.mu.Unlock()
	beat := time.NewTicker(agentSlotHeartbeat)
	defer beat.Stop()
	for {
		select {
		case <-admitted:
			return s.releaseFunc(), nil
		case <-beat.C:
			heartbeat()
		case <-ctx.Done():
			s.mu.Lock()
			for i, ch := range s.waiting {
				if ch == admitted {
					s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
					s.mu.Unlock()
					return nil, context.Cause(ctx)
				}
			}
			s.mu.Unlock()
			s.releaseFunc()()
			return nil, context.Cause(ctx)
		}
	}
}

func (a *API) touchAgentRun(ctx context.Context, runID string) {
	if _, err := a.DB.Exec(ctx, `update work_run set updated_at = now() where id::text = $1`, runID); err != nil {
		log.Printf("[work-run] heartbeat for %s failed: %v", runID, err)
		return
	}
	if _, err := a.DB.Exec(ctx, `update processing_job set updated_at = now() where id::text = $1`, runID); err != nil {
		log.Printf("[work-run] heartbeat for %s failed: %v", runID, err)
	}
}

type runSubscriber func(event any, id int64)

type liveRun struct {
	orgID, threadID string
	cancel          context.CancelCauseFunc

	mu   sync.Mutex
	subs map[uint64]runSubscriber
	next uint64
}

// runRegistry is lib/neko-run-registry.ts and agent-shutdown.ts: the turns
// that run in this process, their cancel and their live subscribers.
type runRegistry struct {
	mu   sync.Mutex
	runs map[string]*liveRun
	wg   sync.WaitGroup
}

func (r *runRegistry) register(runID, orgID, threadID string, cancel context.CancelCauseFunc) *liveRun {
	run := &liveRun{orgID: orgID, threadID: threadID, cancel: cancel, subs: map[uint64]runSubscriber{}}
	r.mu.Lock()
	if r.runs == nil {
		r.runs = map[string]*liveRun{}
	}
	r.runs[runID] = run
	r.wg.Add(1)
	r.mu.Unlock()
	return run
}

func (r *runRegistry) unregister(runID string, run *liveRun) {
	r.mu.Lock()
	if r.runs[runID] == run {
		delete(r.runs, runID)
	}
	r.mu.Unlock()
	r.wg.Done()
}

func (r *runRegistry) get(runID string) *liveRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[runID]
}

func (a *API) abortRun(runID string) bool {
	run := a.runs.get(runID)
	if run == nil {
		return false
	}
	run.cancel(nil)
	return true
}

// subscribeRun adds a live subscriber; ok is false when the run is not in
// this process. A subscriber runs inside the run's event store and must not
// block.
func (a *API) subscribeRun(runID string, sub runSubscriber) (unsubscribe func(), ok bool) {
	run := a.runs.get(runID)
	if run == nil {
		return nil, false
	}
	run.mu.Lock()
	id := run.next
	run.next++
	run.subs[id] = sub
	run.mu.Unlock()
	return func() {
		run.mu.Lock()
		delete(run.subs, id)
		run.mu.Unlock()
	}, true
}

func (a *API) notifyRun(runID string, event any, id int64) {
	run := a.runs.get(runID)
	if run == nil {
		return
	}
	run.mu.Lock()
	subs := make([]runSubscriber, 0, len(run.subs))
	for _, sub := range run.subs {
		subs = append(subs, sub)
	}
	run.mu.Unlock()
	for _, sub := range subs {
		func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("[neko-run-registry] subscriber threw for %s: %v", runID, p)
				}
			}()
			sub(event, id)
		}()
	}
}

// StopTurns stops every turn in this process and waits until each one has
// written its run status, or ctx ends. It returns how many it stopped.
func (a *API) StopTurns(ctx context.Context) int {
	a.runs.mu.Lock()
	n := len(a.runs.runs)
	for _, run := range a.runs.runs {
		run.cancel(errTurnShutdown)
	}
	a.runs.mu.Unlock()
	done := make(chan struct{})
	go func() {
		a.runs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return n
}

// runEventStore is createCoalescingEmit() in apps/web/src/lib/coalescing-emit.ts
// over appendWorkRunEvent(): assistant and user text joins into one row per
// idle window, every other event flushes the text first, and each stored
// event goes to the live subscribers in order.
type runEventStore struct {
	api                          *API
	orgID, threadID, runID, kind string
	idle                         time.Duration
	skills                       skillTrace

	mu      sync.Mutex
	role    string
	buf     strings.Builder
	pending bool
	timer   *time.Timer
	gen     int
	err     error
}

func (a *API) newRunEventStore(orgID, threadID, runID string) *runEventStore {
	return &runEventStore{api: a, orgID: orgID, threadID: threadID, runID: runID, kind: "work", idle: runEventFlushIdle}
}

func (s *runEventStore) emit(ctx context.Context, event any) error {
	ctx = context.WithoutCancel(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if jsGet(event, "type") != "message" {
		if err := s.flushLocked(ctx); err != nil {
			return err
		}
		s.skills.observe(traceBinding{RunID: s.runID, OrgID: s.orgID, Kind: s.kind}, event)
		return s.persistLocked(ctx, event)
	}
	role, _ := jsGet(event, "role").(string)
	if role != "assistant" && role != "user" {
		if err := s.flushLocked(ctx); err != nil {
			return err
		}
		return s.persistLocked(ctx, event)
	}
	if s.pending && s.role != role {
		if err := s.flushLocked(ctx); err != nil {
			return err
		}
	}
	content, _ := jsGet(event, "content").(string)
	if !s.pending {
		s.pending, s.role = true, role
		s.buf.Reset()
	}
	s.buf.WriteString(content)
	s.stopTimerLocked()
	s.gen++
	gen := s.gen
	s.timer = time.AfterFunc(s.idle, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen == gen {
			_ = s.flushLocked(ctx)
		}
	})
	return nil
}

func (s *runEventStore) stopTimerLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *runEventStore) flushLocked(ctx context.Context) error {
	s.stopTimerLocked()
	s.gen++
	if !s.pending {
		return nil
	}
	s.pending = false
	content := s.buf.String()
	s.buf.Reset()
	if content == "" {
		return nil
	}
	return s.persistLocked(ctx, jsObjOf("type", "message", "role", s.role, "content", content))
}

func (s *runEventStore) persistLocked(ctx context.Context, event any) error {
	id, err := s.api.appendRunEvent(ctx, s.orgID, s.threadID, s.runID, event)
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		return err
	}
	s.api.notifyRun(s.runID, event, id)
	return nil
}

// finalize stores the text still held and returns the first store error.
func (s *runEventStore) finalize(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.flushLocked(context.WithoutCancel(ctx))
	return s.err
}

func (a *API) insertRunEvent(ctx context.Context, orgID, threadID, runID string, event any) (int64, error) {
	var kind any
	if typ, ok := jsGet(event, "type").(string); ok {
		kind = typ
	}
	var id int64
	err := a.DB.QueryRow(ctx, `insert into work_run_event (org_id, thread_id, run_id, kind, payload) values ($1, $2, $3, $4, $5::jsonb) returning id`,
		orgID, threadID, runID, kind, jsStringify(event, false)).Scan(&id)
	return id, err
}

// appendRunEvent is appendWorkRunEvent() in packages/llm/src/work/store.ts:
// a turn's usage adds its spend row, and a tool call that reads a skill adds
// the skill usage row.
func (a *API) appendRunEvent(ctx context.Context, orgID, threadID, runID string, event any) (int64, error) {
	id, err := a.insertRunEvent(ctx, orgID, threadID, runID, event)
	if err != nil || id <= 0 {
		return id, err
	}
	switch jsGet(event, "type") {
	case "usage":
		if jsGet(event, "source") == "outer" {
			if err := a.recordTurnSpend(ctx, orgID, runID, event, time.Now()); err != nil {
				return id, err
			}
		}
	case "tool_start":
		a.recordSkillUsage(ctx, orgID, threadID, runID, event, id)
	}
	return id, nil
}

// runSpendGuard is createRunSpendGuard() in packages/llm/src/spend/run-guard.ts:
// once the run's spend passes its reserved cap, the next event stops the
// run. A tool call carries the running turn's cost so far.
type runSpendGuard struct {
	api   *API
	runID string
	inner func(ctx context.Context, event any) error
	cap   float64
	stop  context.CancelCauseFunc

	mu        sync.Mutex
	completed float64
	exceeded  *spendCapExceeded
}

func (a *API) newRunSpendGuard(parent context.Context, runID string, emit func(context.Context, any) error) (context.Context, func(context.Context, any) error, error) {
	var reserved int64
	err := a.DB.QueryRow(parent, `select reserved_micros from spend_reservation where work_run_id::text = $1`, runID).Scan(&reserved)
	if err := noRows(err); err != nil {
		return nil, nil, err
	}
	if err != nil {
		return parent, emit, nil
	}
	ctx, stop := context.WithCancelCause(parent)
	g := &runSpendGuard{api: a, runID: runID, inner: emit, cap: float64(reserved), stop: stop}
	return ctx, g.emit, nil
}

func (g *runSpendGuard) emit(ctx context.Context, event any) error {
	g.mu.Lock()
	current := g.completed
	switch jsGet(event, "type") {
	case "usage":
		if jsGet(event, "source") == "outer" {
			micros, _ := turnUsagePrice(jsGet(event, "usage"), g.cap)
			g.completed += micros
			current = g.completed
		}
	case "tool_start":
		if snap := jsGet(event, "usageSnapshot"); jsTruthy(snap) {
			micros, _ := turnUsagePrice(snap, 0)
			current += micros
		}
	}
	g.mu.Unlock()
	if err := g.inner(ctx, event); err != nil {
		return err
	}
	g.mu.Lock()
	if g.exceeded != nil || current <= g.cap {
		g.mu.Unlock()
		return nil
	}
	g.exceeded = &spendCapExceeded{capUSD: microsToUSD(g.cap), spentUSD: microsToUSD(current)}
	stop := g.exceeded
	g.mu.Unlock()
	msg := stop.Error()
	if err := g.inner(ctx, jsObjOf("type", "error", "message", msg)); err != nil {
		return err
	}
	if err := g.api.finishWorkRun(context.WithoutCancel(ctx), g.runID, "failed", &msg); err != nil {
		return err
	}
	g.stop(stop)
	return nil
}

// startChatRun is the part of POST work/threads/{threadId}/runs after the
// run row and the user message exist: it registers the run, stores its
// events, guards its spend and runs the turn in the background. The channel
// closes when the run is over and its events are stored.
func (a *API) startChatRun(ctx context.Context, o chatTurnOptions) (<-chan struct{}, error) {
	base, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	store := a.newRunEventStore(o.OrgID, o.ThreadID, o.RunID)
	runCtx, guarded, err := a.newRunSpendGuard(base, o.RunID, store.emit)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	run := a.runs.register(o.RunID, o.OrgID, o.ThreadID, cancel)
	untrace := a.EvalTrace.register(o.RunID)
	o.Emit = guarded
	core := o.runCore
	if core == nil {
		core = a.runAgentCore
	}
	o.runCore = func(ctx context.Context, in agentCoreInput) (axRunResult, error) {
		release, err := a.slots.acquire(ctx,
			func() error { return in.Emit(ctx, statusEvent("Waiting for another question to finish…")) },
			func() { a.touchAgentRun(context.WithoutCancel(ctx), in.RunID) })
		if err != nil {
			return axRunResult{}, err
		}
		defer release()
		return core(ctx, in)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer a.runs.unregister(o.RunID, run)
		defer untrace()
		defer cancel(nil)
		bg := context.WithoutCancel(runCtx)
		if _, err := a.runChatTurn(runCtx, o); err != nil {
			log.Printf("[work-run/inproc] run %s threw: %v", o.RunID, err)
			a.failThrownRun(bg, o, store, err)
		}
		if err := store.finalize(bg); err != nil {
			log.Printf("[work-run/inproc] finalize failed for %s: %v", o.RunID, err)
		}
	}()
	return done, nil
}

func (a *API) failThrownRun(ctx context.Context, o chatTurnOptions, store *runEventStore, cause error) {
	var status string
	if err := noRows(a.DB.QueryRow(ctx, `select status from work_run where org_id = $1 and id::text = $2`, o.OrgID, o.RunID).Scan(&status)); err != nil {
		log.Printf("[work-run/inproc] cleanup failed for %s: %v", o.RunID, err)
		return
	}
	switch status {
	case "completed", "failed", "cancelled", "needs_input":
		return
	}
	msg := cause.Error()
	if err := store.emit(ctx, jsObjOf("type", "error", "message", msg)); err != nil {
		log.Printf("[work-run/inproc] cleanup failed for %s: %v", o.RunID, err)
		return
	}
	if err := store.emit(ctx, doneEvent("failed")); err != nil {
		log.Printf("[work-run/inproc] cleanup failed for %s: %v", o.RunID, err)
		return
	}
	if err := a.finishWorkRun(ctx, o.RunID, "failed", &msg); err != nil {
		log.Printf("[work-run/inproc] cleanup failed for %s: %v", o.RunID, err)
	}
}
