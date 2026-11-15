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

func TestRunEventStoreFlushesDuringASteadyStream(t *testing.T) {
	f, _, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	cancelRun := a.runs.register(runAdmin, "org", threadMine, func(error) {})
	t.Cleanup(func() { a.runs.unregister(runAdmin, cancelRun) })
	var mu sync.Mutex
	var arrivals []time.Time
	var text strings.Builder
	unsubscribe, ok := a.subscribeRun(runAdmin, func(event any, _ int64) {
		if jsGet(event, "type") != "message" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		arrivals = append(arrivals, time.Now())
		text.WriteString(jsGet(event, "content").(string))
	})
	if !ok {
		t.Fatal("subscribe")
	}
	defer unsubscribe()

	s := a.newRunEventStore("org", threadMine, runAdmin)
	start := time.Now()
	var want strings.Builder
	for i := 0; i < 100; i++ {
		word := "word" + string(rune('a'+i%26)) + " "
		want.WriteString(word)
		if err := s.emit(ctx, jsObjOf("type", "message", "role", "assistant", "content", word)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	end := time.Now()
	mu.Lock()
	before := len(arrivals)
	var first time.Duration
	if before > 0 {
		first = arrivals[0].Sub(start)
	}
	mu.Unlock()
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	if before < 10 {
		t.Fatalf("the text grew %d times in %s of steady deltas", before, end.Sub(start).Round(time.Millisecond))
	}
	if first > 500*time.Millisecond {
		t.Fatalf("the first text arrived after %s", first)
	}
	if text.String() != want.String() {
		t.Fatalf("text %q", text.String())
	}
}

func TestRunEventStoreScrubsASecretSplitAcrossFlushes(t *testing.T) {
	const key = "sk-planted-0123456789abcdef"
	f, db, _ := chatTurnFront(t)
	a := f.API
	ctx := context.Background()
	a.SecretsDir = t.TempDir()
	if err := secrets.Write(secrets.Store{"pack.shop": {"API_KEY": key}}, a.SecretsDir); err != nil {
		t.Fatal(err)
	}
	old := runEventFlushAfter
	t.Cleanup(func() { runEventFlushAfter = old })
	runEventFlushAfter = 10 * time.Millisecond
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
	if err := s.finalize(ctx); err != nil {
		t.Fatal(err)
	}
	stored := rowText(t, db, `select string_agg(payload->>'content', '|' order by id) from work_run_event where run_id = $1`, runAdmin)
	if strings.Contains(stored, key[:9]) || strings.Contains(stored, key[9:20]) || strings.Contains(stored, key[20:]) ||
		strings.ReplaceAll(stored, "|", "") != "Here is the configuration that the shop pack uses, and the key is [REDACTED] in the file." ||
		strings.Count(stored, "|") < 1 {
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
