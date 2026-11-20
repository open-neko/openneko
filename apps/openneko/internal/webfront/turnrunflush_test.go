package webfront

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

type fakeRunClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeRunTimer
}

type fakeRunTimer struct {
	c       *fakeRunClock
	at      time.Duration
	f       func()
	stopped bool
}

func (t *fakeRunTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (c *fakeRunClock) afterFunc(d time.Duration, f func()) runEventTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeRunTimer{c: c, at: c.now + d, f: f}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeRunClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	var due []*fakeRunTimer
	kept := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case t.at <= c.now:
			t.stopped = true
			due = append(due, t)
		default:
			kept = append(kept, t)
		}
	}
	c.timers = kept
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

func TestRunEventStoreFlushesDuringASteadyStream(t *testing.T) {
	f, _, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	cancelRun := a.runs.register(runAdmin, "org", threadMine, func(error) {})
	t.Cleanup(func() { a.runs.unregister(runAdmin, cancelRun) })
	clock := &fakeRunClock{}
	var arrivals []time.Duration
	var text strings.Builder
	unsubscribe, ok := a.subscribeRun(runAdmin, func(event any, _ int64) {
		if jsGet(event, "type") != "message" {
			return
		}
		arrivals = append(arrivals, clock.now)
		text.WriteString(jsGet(event, "content").(string))
	})
	if !ok {
		t.Fatal("subscribe")
	}
	defer unsubscribe()

	s := a.newRunEventStore("org", threadMine, runAdmin)
	s.afterFunc = clock.afterFunc
	var want strings.Builder
	for i := 0; i < 100; i++ {
		word := "word" + string(rune('a'+i%26)) + " "
		want.WriteString(word)
		if err := s.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", word)); err != nil {
			t.Fatal(err)
		}
		clock.advance(20 * time.Millisecond)
	}
	before := len(arrivals)
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	if before != 7 || arrivals[0] != 100*time.Millisecond || arrivals[1] != 400*time.Millisecond {
		t.Fatalf("the text grew %d times in 2s of steady deltas, first at %v", before, arrivals)
	}
	if text.String() != want.String() {
		t.Fatalf("text %q", text.String())
	}
}

func plantRunSecret(t *testing.T, a *API, key string) {
	t.Helper()
	a.SecretsDir = t.TempDir()
	if err := secrets.Write(secrets.Store{"pack.shop": {"API_KEY": key}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
}

func TestRunEventStoreHoldsASecretTailAcrossOtherEvents(t *testing.T) {
	const key = "planted-key-0123456789ab"
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	plantRunSecret(t, a, key)
	s := a.newRunEventStore("org", threadMine, runAdmin)
	s.afterFunc = (&fakeRunClock{}).afterFunc
	note := func(content string) *jsObj {
		return jsObjOf("type", "progress", "id", "ax-summary-1", "content", content, "source", "provider_summary")
	}
	answer := func(content string) *jsObj {
		return jsObjOf("type", "message", "role", "assistant", "content", content)
	}
	for _, e := range []*jsObj{
		note("I see " + key[:6]),
		jsObjOf("type", "usage", "usage", jsObjOf()),
		note(key[6:] + " in the config."),
		answer("The key is " + key[:9]),
		jsObjOf("type", "status", "message", "Checking"),
		answer(key[9:14]),
		jsObjOf("type", "usage", "usage", jsObjOf()),
		answer(key[14:20]),
		jsObjOf("type", "card_pending"),
		answer(key[20:] + " here."),
	} {
		if err := s.emit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	rows := rowText(t, db, `select string_agg(coalesce(payload->>'content', ''), '|' order by id) from work_run_event where run_id = $1`, runAdmin)
	for _, part := range strings.Split(rows, "|") {
		for i := 0; i+4 <= len(key); i++ {
			if strings.Contains(part, key[i:i+4]) {
				t.Fatalf("a row holds %q of the key: %q", key[i:i+4], rows)
			}
		}
	}
	notes := rowText(t, db, `select string_agg(payload->>'content', '' order by id) from work_run_event where run_id = $1 and kind = 'progress'`, runAdmin)
	text := rowText(t, db, `select string_agg(payload->>'content', '' order by id) from work_run_event where run_id = $1 and kind = 'message'`, runAdmin)
	if notes != "I see [REDACTED] in the config." || text != "The key is [REDACTED] here." {
		t.Fatalf("notes %q, text %q", notes, text)
	}
}

func TestRunEventStoreDropsTheHeldTailOnAReset(t *testing.T) {
	const key = "planted-key-0123456789ab"
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	plantRunSecret(t, a, key)
	s := a.newRunEventStore("org", threadMine, runAdmin)
	s.afterFunc = (&fakeRunClock{}).afterFunc
	for _, e := range []*jsObj{
		jsObjOf("type", "message", "role", "assistant", "content", "Old answer plan"),
		jsObjOf("type", "status", "message", "Again"),
		jsObjOf("type", "answer_reset"),
		jsObjOf("type", "message", "role", "assistant", "content", "New answer."),
	} {
		if err := s.emit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	got := rowText(t, db, `select string_agg(kind || ' ' || coalesce(payload->>'content', payload->>'message', ''), '|' order by id) from work_run_event where run_id = $1`, runAdmin)
	if got != "message Old answer |status Again|answer_reset |message New answer." {
		t.Fatalf("stored %q", got)
	}
}

func TestRunEventStoreKeepsTextWhenATimedStoreFails(t *testing.T) {
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	clock := &fakeRunClock{}
	s := a.newRunEventStore("org", threadMine, "not-a-uuid")
	s.afterFunc = clock.afterFunc
	if err := s.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "Revenue rose ")); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	s.runID = runAdmin
	if err := s.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", "by 2%.")); err != nil {
		t.Fatal(err)
	}
	if err := s.finalize(ctx); err == nil {
		t.Fatal("the failed store was not reported")
	}
	if got := rowText(t, db, `select string_agg(payload->>'content', '|' order by id) from work_run_event where run_id = $1`, runAdmin); got != "Revenue rose by 2%." {
		t.Fatalf("stored %q", got)
	}
}

func TestRunEventStoreScrubsASecretSplitAcrossFlushes(t *testing.T) {
	const key = "planted-key-0123456789ab"
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	a.SecretsDir = t.TempDir()
	if err := secrets.Write(secrets.Store{"pack.shop": {"API_KEY": key}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	old, oldEvery := runEventFlushAfter, runEventFlushEvery
	t.Cleanup(func() { runEventFlushAfter, runEventFlushEvery = old, oldEvery })
	runEventFlushAfter, runEventFlushEvery = 10*time.Millisecond, 10*time.Millisecond
	count := func() int {
		n := 0
		if err := db.QueryRow(ctx, `select count(*) from work_run_event where run_id = $1`, runAdmin).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	s := a.newRunEventStore("org", threadMine, runAdmin)
	head := "Here is the configuration that the shop pack uses, and the key is " + key[:9]
	if err := s.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", head)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the timed flush stored nothing")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, part := range []string{key[9:20], key[20:] + " in the file."} {
		if err := s.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", part)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := count(); n < 2 {
		t.Fatalf("the text was stored in %d rows before the end", n)
	}
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	stored := rowText(t, db, `select string_agg(payload->>'content', '|' order by id) from work_run_event where run_id = $1`, runAdmin)
	if strings.Contains(stored, key[:9]) || strings.Contains(stored, key[9:20]) || strings.Contains(stored, key[20:]) ||
		stored != "Here is the configuration that the shop pack uses, and the key is [REDACTED] in the file." {
		t.Fatalf("stored %q", stored)
	}
}

func TestSecretSafeCutMatchesAWholeScrub(t *testing.T) {
	values := []string{"sk-0123456789", "sk-0123456789-long", "pässwörd-é-9", "abababab"}
	pattern, usable := secretPattern(values)
	scrub := secretScrubber(values)
	for _, c := range []struct {
		text string
		want int
	}{
		{"Revenue rose by 4%.", 19},
		{"The key is sk-01", 11},
		{"The key is sk-0123456789 and", 28},
		{"The key is sk-0123456789-lo", 11},
		{"Code pässw", 5},
		{"ab", 0},
	} {
		if got := secretSafeCut(c.text, pattern, usable); got != c.want {
			t.Errorf("%q: cut %d, want %d", c.text, got, c.want)
		}
	}
	if got := secretSafeCut("short text", nil, nil); got != 10 {
		t.Fatalf("no secrets: %d", got)
	}
	pieces := []string{"The key ", "sk-0123456789", "-long", " and ", "pässwörd-é-9", " é ", "abababababab", "x", "sk-012", "3456789", "."}
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 2000; round++ {
		var whole strings.Builder
		for n := 3 + rng.Intn(8); n > 0; n-- {
			whole.WriteString(pieces[rng.Intn(len(pieces))])
		}
		text := whole.String()
		var out, buf strings.Builder
		for i := 0; i < len(text); {
			step := 1 + rng.Intn(7)
			if i+step > len(text) {
				step = len(text) - i
			}
			buf.WriteString(text[i : i+step])
			i += step
			if rng.Intn(2) == 0 {
				held := buf.String()
				cut := secretSafeCut(held, pattern, usable)
				out.WriteString(scrub(held[:cut]))
				buf.Reset()
				buf.WriteString(held[cut:])
			}
		}
		out.WriteString(scrub(buf.String()))
		if out.String() != scrub(text) {
			t.Fatalf("text %q: chunked %q, whole %q", text, out.String(), scrub(text))
		}
	}
}

func TestRunEventStoreJoinsAndScrubsThinkingDeltas(t *testing.T) {
	const key = "planted-key-0123456789ab"
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	a.SecretsDir = t.TempDir()
	if err := secrets.Write(secrets.Store{"pack.shop": {"API_KEY": key}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	old := runEventFlushAfter
	t.Cleanup(func() { runEventFlushAfter = old })
	runEventFlushAfter = time.Hour
	s := a.newRunEventStore("org", threadMine, runAdmin)
	note := func(id, content string) *jsObj {
		return jsObjOf("type", "progress", "id", id, "content", content, "source", "provider_summary", "provider", "anthropic")
	}
	for _, e := range []*jsObj{
		note("ax-summary-1", "The shop key is "+key[:12]),
		note("ax-summary-1", key[12:]+", so I "),
		note("ax-summary-1", "leave it out."),
		jsObjOf("type", "status", "message", "Read the orders"),
		note("ax-summary-2", "Second note."),
		note("ax-summary-3", "Third note."),
		jsObjOf("type", "message", "role", "assistant", "content", "Done."),
	} {
		if err := s.emit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	stored := rowText(t, db, `select string_agg(kind || ' ' || coalesce(payload->>'id', '') || ' ' || coalesce(payload->>'content', payload->>'message') || ' ' || coalesce(payload->>'provider', ''), '|' order by id) from work_run_event where run_id = $1`, runAdmin)
	want := "progress ax-summary-1 The shop key is [REDACTED], so I leave it out. anthropic|status  Read the orders |progress ax-summary-2 Second note. anthropic|progress ax-summary-3 Third note. anthropic|message  Done. "
	if stored != want {
		t.Fatalf("stored %q", stored)
	}
}
