package webfront

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-neko/neko/apps/openneko/internal/llm"
	"github.com/open-neko/openneko/apps/ax-harness/keydoor"
	"github.com/open-neko/openneko/apps/ax-harness/serve"
)

func listenDoor(t *testing.T, door *llm.KeyDoor) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "door")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	path := filepath.Join(dir, keydoor.SocketName)
	if _, err := llm.ListenDoor(ctx, path, door); err != nil {
		t.Fatal(err)
	}
	keydoor.SetSocket(path)
	t.Cleanup(func() { keydoor.SetSocket("") })
	return dir
}

func mismatchRows(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `select concat_ws(' ', payload->>'purpose', payload->>'doorCalls', payload->>'reportedCalls') from audit_chain
		where org_id = 'org' and entity_kind = 'key_door' and event = 'agent:model_calls_mismatch' order by seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	return got
}

func TestKeyDoorServes200HonestCallsAtOnce(t *testing.T) {
	f, db, _ := hostFront(t)
	door := f.API.newKeyDoor()
	listenDoor(t, door)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(up.Close)
	client, err := keydoor.Client()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failed := make(chan string, 200)
	for i := range 200 {
		pass, err := door.Issue("org", fmt.Sprintf("turn honest-%d", i), map[string]llm.DoorRoute{"primary": {Upstream: up.URL, Key: "k"}}, time.Minute, 1)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+pass.Token)
			res, err := client.Do(req)
			if err != nil {
				failed <- err.Error()
				return
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode != 200 {
				failed <- fmt.Sprintf("status %d", res.StatusCode)
			}
			pass.Settle(context.Background(), 1)
		}()
	}
	wg.Wait()
	close(failed)
	var errs []string
	for e := range failed {
		errs = append(errs, e)
	}
	if len(errs) > 0 {
		t.Errorf("%d of 200 honest calls failed: %s", len(errs), errs[0])
	}
	if rows := mismatchRows(t, db); len(rows) > 0 {
		t.Errorf("honest calls wrote %d mismatch rows: %v", len(rows), rows[0])
	}
}

// cutServe spends calls[i] door calls in turn i, reports started[i]
// model.request.started events, and drops the connection with no result.
func cutServe(t *testing.T, dir string, calls, started []int) string {
	t.Helper()
	socket := filepath.Join(dir, "serve.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for turn := 0; ; turn++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadBytes('\n')
			var start serve.Frame
			var spec struct {
				RunID string `json:"run_id"`
			}
			_ = json.Unmarshal(line, &start)
			_ = json.Unmarshal(start.Spec, &spec)
			client, _ := keydoor.Client()
			for range calls[turn] {
				req, _ := http.NewRequest(http.MethodPost, keydoor.URL("primary")+"/chat/completions", strings.NewReader(`{}`))
				req.Header.Set("Authorization", "Bearer "+start.ModelKeys["MODEL_API_KEY"])
				if res, err := client.Do(req); err == nil {
					_, _ = io.Copy(io.Discard, res.Body)
					res.Body.Close()
				}
			}
			for i := range started[turn] {
				frame, _ := json.Marshal(map[string]any{"type": "event", "event": map[string]any{"version": 1, "run_id": spec.RunID,
					"sequence": i + 1, "type": "model.request.started", "call_id": i + 1}})
				_, _ = conn.Write(append(frame, '\n'))
			}
			conn.Close()
		}
	}()
	return socket
}

func TestKeyDoorSettlesATurnThatEndsWithNoResult(t *testing.T) {
	f, db, _ := hostFront(t)
	clearAxEnv(t, nil)
	door := f.API.newKeyDoor()
	dir := listenDoor(t, door)
	up, _ := keyUpstream(t, func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{}`) })
	socket := cutServe(t, dir, []int{3, 3, 2}, []int{0, 3, 3})
	cfg := &llm.AxBackendConfig{Route: llm.AxModelRoute{Provider: "openai-compatible", Model: "m", URL: up.URL + "/v1"}}
	for _, tag := range []string{"silent", "honest", "cut"} {
		res, err := runAx(context.Background(), cfg, axTurnOptions{OrgID: "org", Tag: tag, Socket: socket, Door: door,
			Keys: map[string]string{"MODEL_API_KEY": "k"}})
		if err != nil || res.Status == "completed" {
			t.Fatalf("%s turn: %+v %v", tag, res, err)
		}
	}
	got := mismatchRows(t, db)
	if want := "turn silent 3 0"; strings.Join(got, "\n") != want {
		t.Errorf("audit chain after turns with no result:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}

func TestKeyDoorLimitFollowsTheConcurrentJobsSetting(t *testing.T) {
	a := &API{}
	for _, c := range []struct{ turns, want int }{{0, 64}, {3, 64}, {20, 20*llm.MaxGrantOpen + 32}, {200, 200*llm.MaxGrantOpen + 32}} {
		a.slots.setLimit(c.turns)
		if got := a.newKeyDoor().MaxOpen(); got != c.want {
			t.Errorf("%d agent turns at once: the door opens %d calls at once, want %d", c.turns, got, c.want)
		}
	}
}
