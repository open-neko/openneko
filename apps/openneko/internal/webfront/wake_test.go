package webfront

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestWake(t *testing.T) {
	ctx := context.Background()
	pool := testpg.Migrated(t)
	var schema string
	if err := pool.QueryRow(ctx, `select current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	var actions, sweeps atomic.Int32
	q := &jobs.Queue{Pool: pool, Schema: schema + "_river", BossSchema: schema + "_boss", Logf: t.Logf, MetricsOn: true,
		MetricSweep:      func(context.Context) error { sweeps.Add(1); return nil },
		CronSweep:        func(context.Context) error { sweeps.Add(1); return nil },
		DirectorySync:    func(context.Context) error { sweeps.Add(1); return nil },
		BotNameSweep:     func(context.Context) error { sweeps.Add(1); return nil },
		WorkflowAPISweep: func(context.Context) error { sweeps.Add(1); return nil },
		ActionExecute:    func(context.Context, string, string) error { actions.Add(1); return nil },
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "drop schema if exists "+q.Schema+" cascade") }) //nolint:errcheck
	if err := q.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Schema: q.Schema})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, jobs.ActionExecuteArgs{OrgID: "org-1", ActionRequestID: "ar-1"}, nil); err != nil {
		t.Fatal(err)
	}
	wake := &Wake{Token: "wake-secret", Queue: q}
	f := newFront(t, nil)
	f.Wake = wake
	call := func(method, target, auth string) (int, jobs.WakeResult) {
		t.Helper()
		r := httptest.NewRequest(method, target, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		var res jobs.WakeResult
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, res
	}

	for _, c := range []struct {
		method, target, auth string
		want                 int
	}{
		{"POST", WakePath, "", 401},
		{"POST", WakePath, "Bearer wrong", 401},
		{"POST", WakePath, "wake-secret", 401},
		{"GET", WakePath, "Bearer wake-secret", 405},
		{"POST", WakePath + "?max_seconds=0", "Bearer wake-secret", 400},
		{"POST", WakePath + "?max_seconds=301", "Bearer wake-secret", 400},
		{"POST", WakePath + "?max_seconds=x", "Bearer wake-secret", 400},
	} {
		if code, _ := call(c.method, c.target, c.auth); code != c.want {
			t.Errorf("%s %s with %q = %d, want %d", c.method, c.target, c.auth, code, c.want)
		}
	}
	if actions.Load() != 0 || sweeps.Load() != 0 {
		t.Fatal("a refused call ran a job")
	}

	code, res := call("POST", WakePath+"?max_seconds=30", "Bearer wake-secret")
	if code != 200 || res.Inserted != 7 || res.Ran != 8 || res.TimedOut {
		t.Fatalf("wake with jobs off = %d %+v, want 7 inserted and 8 ran", code, res)
	}
	if actions.Load() != 1 || sweeps.Load() != 5 {
		t.Fatalf("ran %d actions and %d sweeps, want 1 and 5", actions.Load(), sweeps.Load())
	}
	code, res = call("POST", WakePath, "Bearer wake-secret")
	if code != 200 || res.Inserted > 3 || res.Ran != res.Inserted {
		t.Fatalf("second wake = %d %+v, want nothing new outside a new minute", code, res)
	}

	wake.JobsOn = true
	if _, err := client.Insert(ctx, jobs.ActionExecuteArgs{OrgID: "org-1", ActionRequestID: "ar-2"}, nil); err != nil {
		t.Fatal(err)
	}
	code, res = call("POST", WakePath, "Bearer wake-secret")
	if code != 200 || res.Ran != 0 || actions.Load() != 1 {
		t.Fatalf("wake with jobs on = %d %+v and %d actions, want no job run", code, res, actions.Load())
	}

	wake.Token = ""
	if code, _ := call("POST", WakePath, "Bearer "); code != 401 {
		t.Fatalf("a web with no wake token answered %d, want 401", code)
	}
	f.Wake = nil
	if code, _ := call("POST", WakePath, "Bearer wake-secret"); code != 404 {
		t.Fatalf("a front with no wake handler answered %d, want 404", code)
	}
}
