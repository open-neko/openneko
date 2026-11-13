package webfront

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

const plantedStoreFault = "planted-store-fault password=hunter2-7781"

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	out := &lockedBuffer{}
	saved, flags := log.Writer(), log.Flags()
	log.SetOutput(out)
	t.Cleanup(func() { log.SetOutput(saved); log.SetFlags(flags) })
	return out
}

func failOpenAPIStore(t *testing.T, exec func(string) error) {
	t.Helper()
	if err := exec(`create function neko_test_openapi_fault() returns trigger language plpgsql as $$
		begin raise exception '` + plantedStoreFault + `'; end $$`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`create trigger neko_test_openapi_fault before insert on openapi_spec_asset
		for each row execute function neko_test_openapi_fault()`); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAPIStoreErrorStaysInTheLog(t *testing.T) {
	t.Run("route", func(t *testing.T) {
		f, db := settingsFront(t)
		failOpenAPIStore(t, func(q string) error { _, err := db.Exec(context.Background(), q); return err })
		logs := captureLog(t)
		body, h := specUpload(t, nil, "a.yaml", petSpec)
		w := call(f, "POST", "/api/v1/settings/openapi-specs", body, withCookie(h, sessionCookie("admin")))
		if w.Code != 500 || strings.TrimSpace(w.Body.String()) != `{"error":"internal server error"}` {
			t.Fatalf("route: %d %s", w.Code, w.Body)
		}
		if !strings.Contains(logs.String(), plantedStoreFault) {
			t.Fatalf("the log lacks the database error: %q", logs.String())
		}
	})

	t.Run("tool", func(t *testing.T) {
		f, db := memoryFront(t)
		seed(t, db, `insert into work_run (id, org_id, thread_id, backend, actor_user_id, actor_role) values
			($1, 'org', $2, 'test', 'admin', 'member')`, sourceAdminRun, threadMine)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(petSpec)) }))
		defer srv.Close()
		_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
		oldLookup, oldPublic, oldCAs := openAPILookup, openAPIPublic, openAPIRootCAs
		t.Cleanup(func() { openAPILookup, openAPIPublic, openAPIRootCAs = oldLookup, oldPublic, oldCAs })
		openAPIRootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		openAPILookup = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		openAPIPublic = func(netip.Addr) bool { return true }
		failOpenAPIStore(t, func(q string) error { _, err := db.Exec(context.Background(), q); return err })
		logs := captureLog(t)

		admin := f.API.runTools(turnBinding{OrgID: "org", RunID: sourceAdminRun})
		got, err := admin.importOpenAPISpec(context.Background(), openAPIImportIn{OrgID: "org", RunID: &admin.binding.RunID,
			URL: "https://example.com:" + port + "/pets.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if s := jsStringify(got, false); s != `{"error":"`+openAPIStoreFailed+`"}` {
			t.Fatalf("tool: %s", s)
		}
		if !strings.Contains(logs.String(), plantedStoreFault) {
			t.Fatalf("the log lacks the database error: %q", logs.String())
		}
	})
}
