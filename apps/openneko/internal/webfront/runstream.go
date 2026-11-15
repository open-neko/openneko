package webfront

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	runStreamSubscribed = func(runID string) {}
	runStreamRead       = func(runID string) {}
)

// runNotices is createNotifyClient("work_run_event") for one stream: a wake
// for each stored event of the run. It owns a connection outside the pool,
// so open streams cannot use up the pool.
type runNotices struct {
	wake   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
	conn   *pgx.Conn

	mu      sync.Mutex
	ids     []int64
	unnamed bool
}

// take returns the event ids that the notices since the last take named;
// unnamed is true when a notice named no id.
func (n *runNotices) take() (ids []int64, unnamed bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids, unnamed = n.ids, n.unnamed
	n.ids, n.unnamed = nil, false
	return ids, unnamed
}

func (a *API) listenRun(ctx context.Context, runID string) (*runNotices, error) {
	conn, err := pgx.ConnectConfig(ctx, a.DB.Config().ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `listen work_run_event`); err != nil {
		_ = conn.Close(context.Background())
		return nil, err
	}
	lctx, cancel := context.WithCancel(context.Background())
	n := &runNotices{wake: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{}), conn: conn}
	go func() {
		defer close(n.done)
		for {
			note, err := conn.WaitForNotification(lctx)
			if err != nil {
				return
			}
			if note.Channel != "work_run_event" {
				continue
			}
			rest, ok := strings.CutPrefix(note.Payload, runID)
			if !ok || (rest != "" && rest[0] != ':') {
				continue
			}
			id, err := strconv.ParseInt(strings.TrimPrefix(rest, ":"), 10, 64)
			n.mu.Lock()
			if err == nil {
				n.ids = append(n.ids, id)
			} else {
				n.unnamed = true
			}
			n.mu.Unlock()
			select {
			case n.wake <- struct{}{}:
			default:
			}
		}
	}()
	return n, nil
}

func (n *runNotices) close() {
	if n == nil {
		return
	}
	n.cancel()
	<-n.done
	_ = n.conn.Close(context.Background())
}

// liveQueue holds the events that the run's event store hands to this
// stream. The store must not wait for the stream, so push never blocks.
type liveQueue struct {
	mu     sync.Mutex
	events []liveEvent
	signal chan struct{}
}

type liveEvent struct {
	event any
	id    int64
}

func (q *liveQueue) push(event any, id int64) {
	q.mu.Lock()
	q.events = append(q.events, liveEvent{event, id})
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *liveQueue) take() []liveEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.events
	q.events = nil
	return out
}

// runEventStream is GET /api/v1/work/threads/{threadId}/runs/{runId}/events:
// the run's stored events after the client's last id, then each new event
// as it is stored, until the run ends.
func (a *API) runEventStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	threadID, runID := r.PathValue("threadId"), r.PathValue("runId")
	afterID := 0.0
	if n, ok := jsNumber(r.Header.Get("Last-Event-Id")); ok && n != 0 {
		afterID = n
	} else {
		q := r.URL.Query()
		raw := "0"
		if q.Has("afterId") {
			raw = q.Get("afterId")
		} else if q.Has("afterSeq") {
			raw = q.Get("afterSeq")
		}
		if n, ok := jsNumber(raw); ok {
			afterID = n
		}
	}
	act, err := a.currentActor(ctx, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	notFound := func() {
		w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}
	if ok, err := a.runInThread(ctx, act, threadID, runID); err != nil || !ok {
		if err != nil {
			a.fail(w, r, err)
		} else {
			notFound()
		}
		return
	}
	var backend string
	if err := a.DB.QueryRow(ctx, `select backend from work_run where org_id = $1 and id = $2`, act.OrgID, runID).Scan(&backend); err != nil {
		a.fail(w, r, err)
		return
	}
	var legacy *legacyAnswerFilter
	if legacyRunEventClient(r) {
		if legacy, err = a.legacyAnswerFilterAt(ctx, act.OrgID, runID, int64(afterID)); err != nil {
			a.fail(w, r, err)
			return
		}
	}

	live := &liveQueue{signal: make(chan struct{}, 1)}
	if unsubscribe, ok := a.subscribeRun(runID, live.push); ok {
		defer unsubscribe()
	}
	notices, err := a.listenRun(ctx, runID)
	if err != nil {
		log.Printf("[work-events] LISTEN setup failed; falling back to interval: %v", err)
	}
	defer notices.close()
	var wake <-chan struct{}
	if notices != nil {
		wake = notices.wake
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	closed := false
	write := func(s string) {
		if closed {
			return
		}
		if _, err := w.Write([]byte(s)); err != nil {
			closed = true
			return
		}
		if err := rc.Flush(); err != nil {
			closed = true
		}
	}
	sent := map[int64]bool{}
	send := func(payload string, id int64, keep func(*legacyAnswerFilter) bool) {
		if float64(id) <= afterID || sent[id] {
			return
		}
		sent[id] = true
		if legacy != nil && !keep(legacy) {
			return
		}
		write("id: " + strconv.FormatInt(id, 10) + "\ndata: " + payload + "\n\n")
	}
	doneSent := false
	sendLive := func() {
		for _, e := range live.take() {
			if jsGet(e.event, "type") == "done" {
				doneSent = true
			}
			send(jsStringify(e.event, false), e.id, func(f *legacyAnswerFilter) bool { return f.keepEvent(e.event) })
		}
	}
	// A wake for events that the live channel already sent needs no read,
	// until the run's done event: then the status decides the end.
	wakeNeedsRead := func() bool {
		ids, unnamed := notices.take()
		if unnamed || doneSent {
			return true
		}
		for _, id := range ids {
			if !sent[id] && float64(id) > afterID {
				return true
			}
		}
		return false
	}

	runStreamSubscribed(runID)
	write(": hello\n\n")
	write("data: " + jsStringify(jsObjOf("type", "hello", "runId", runID, "threadId", threadID, "backend", backend), false) + "\n\n")

	start, keepalive := time.Now(), time.Now()
	cursor := int64(0)
	if afterID > 0 {
		cursor = int64(afterID)
	}
	readStored := func() error {
		runStreamRead(runID)
		rows, err := a.DB.Query(ctx, `select id, payload::text, joined_ids, joined_ends from work_run_event
			where org_id = $1 and run_id = $2 and id > $3 order by id`, act.OrgID, runID, cursor)
		if err != nil {
			return err
		}
		type stored struct {
			id      int64
			payload string
			parts   joinedParts
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stored, error) {
			var s stored
			return s, row.Scan(&s.id, &s.payload, &s.parts.ids, &s.parts.ends)
		})
		if err != nil {
			return err
		}
		for _, s := range list {
			cursor = s.id
			if sent[s.id] || float64(s.id) <= afterID {
				continue
			}
			raw := []byte(s.payload)
			if len(s.parts.ids) > 0 {
				var ok bool
				if raw, ok, err = s.parts.rest(raw, func(id int64) bool { return sent[id] || float64(id) <= afterID }); err != nil {
					return err
				} else if !ok {
					sent[s.id] = true
					continue
				}
			}
			payload, err := jsJSON(raw)
			if err != nil {
				return err
			}
			send(string(payload), s.id, func(f *legacyAnswerFilter) bool { return f.keepPayload(payload) })
		}
		return nil
	}
	timer := time.NewTimer(runStreamPoll)
	defer timer.Stop()
	for !closed && ctx.Err() == nil {
		sendLive()
		if err := readStored(); err != nil {
			if ctx.Err() == nil {
				log.Printf("[work-events] read failed for %s: %v", runID, err)
			}
			return
		}
		var status string
		if err := a.DB.QueryRow(ctx, `select status from work_run where org_id = $1 and id = $2`, act.OrgID, runID).Scan(&status); err != nil {
			if ctx.Err() == nil {
				log.Printf("[work-events] status read failed for %s: %v", runID, err)
			}
			return
		}
		if status == "completed" || status == "failed" || status == "cancelled" || status == "needs_input" {
			sendLive()
			if err := readStored(); err != nil && ctx.Err() == nil {
				log.Printf("[work-events] read failed for %s: %v", runID, err)
			}
			return
		}
		if time.Since(start) > runStreamLifetime {
			return
		}
		if time.Since(keepalive) > runStreamKeepalive {
			write(": keepalive\n\n")
			keepalive = time.Now()
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(runStreamPoll)
	wait:
		for {
			select {
			case <-live.signal:
				sendLive()
				if closed {
					return
				}
			case <-wake:
				if wakeNeedsRead() {
					break wait
				}
			case <-timer.C:
				break wait
			case <-ctx.Done():
				return
			}
		}
	}
}
