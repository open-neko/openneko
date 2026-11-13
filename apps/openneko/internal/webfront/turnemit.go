package webfront

import (
	"context"
	"errors"
	"sync"
	"time"
)

const axEmitQueueSize = 1024

var axEmitDrainTimeout = 30 * time.Second

var errEmitDrain = errors.New("the run's events could not be stored in time")

type emitQueue struct {
	ctx     context.Context
	emit    func(context.Context, any) error
	events  chan *jsObj
	pending sync.WaitGroup
	mu      sync.Mutex
	err     error
	closed  bool
}

func newEmitQueue(ctx context.Context, emit func(context.Context, any) error) *emitQueue {
	q := &emitQueue{ctx: ctx, emit: emit, events: make(chan *jsObj, axEmitQueueSize)}
	go q.write()
	return q
}

func (q *emitQueue) write() {
	for event := range q.events {
		if q.failed() == nil {
			if err := q.emit(q.ctx, event); err != nil {
				q.mu.Lock()
				if q.err == nil {
					q.err = err
				}
				q.mu.Unlock()
			}
		}
		q.pending.Done()
	}
}

func (q *emitQueue) failed() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}

func (q *emitQueue) push(event *jsObj) {
	if q.failed() != nil {
		return
	}
	q.pending.Add(1)
	q.events <- event
}

func (q *emitQueue) drain() error {
	done := make(chan struct{})
	go func() {
		q.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
		return q.failed()
	case <-time.After(axEmitDrainTimeout):
		q.mu.Lock()
		if q.err == nil {
			q.err = errEmitDrain
		}
		q.mu.Unlock()
		return errEmitDrain
	}
}

func (q *emitQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.events)
	}
}
