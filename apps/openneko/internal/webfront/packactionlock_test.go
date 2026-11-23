package webfront

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestLockPackActionBodyMatchesNodeFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "packs", "testdata", "pack_action_preflight_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		LockBodyCases []struct {
			Name            string         `json:"name"`
			Kind            string         `json:"kind"`
			HasBody         bool           `json:"hasBody"`
			Body            any            `json:"body"`
			Locked          any            `json:"locked"`
			Config          map[string]any `json:"config"`
			ExpectedHasBody bool           `json:"expectedHasBody"`
			ExpectedBody    any            `json:"expectedBody"`
			ExpectedError   *string        `json:"expectedError"`
		} `json:"lockBodyCases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.LockBodyCases) == 0 {
		t.Fatal("fixture has no lock-body cases")
	}
	for _, tc := range fx.LockBodyCases {
		payload := map[string]any{"operation": "send"}
		if tc.HasBody {
			payload["body"] = tc.Body
		}
		payloadObj, _ := jsValueOf(payload).(*jsObj)
		got, hasBody, err := lockPackActionJSBody(tc.Kind, payloadObj, jsValueOf(tc.Locked), tc.Config)
		if tc.ExpectedError != nil {
			if err == nil || err.Error() != *tc.ExpectedError {
				t.Errorf("%s: got %v, want %q", tc.Name, err, *tc.ExpectedError)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.Name, err)
			continue
		}
		if hasBody != tc.ExpectedHasBody {
			t.Errorf("%s: hasBody=%v, want %v", tc.Name, hasBody, tc.ExpectedHasBody)
		}
		if gotPlain := plainJSON(got); !reflect.DeepEqual(gotPlain, tc.ExpectedBody) {
			t.Errorf("%s: body=%#v, want %#v", tc.Name, gotPlain, tc.ExpectedBody)
		}
	}
}

func TestBuiltInActionKindsWinOverPackKinds(t *testing.T) {
	db := testpg.Migrated(t)
	seedOrg(t, db)
	a := &API{Gate: &Gate{}, DB: db}
	seed(t, db, `insert into pack_action_definition (org_id, kind, description, definition, definition_hash) values
		('org', 'send_webhook', 'Shadow', '{}', 'h'), ('org', 'user_admin', 'Shadow', '{}', 'h'), ('org', 'shop.refund', 'Refund', '{}', 'h')`)
	seed(t, db, `insert into action_request (id, org_id, scope, kind, status, payload) values
		('cccccccc-0000-4000-8000-000000000001', 'org', 'external', 'send_webhook', 'approved', '{}'),
		('cccccccc-0000-4000-8000-000000000002', 'org', 'external', 'user_admin', 'approved', '{}'),
		('cccccccc-0000-4000-8000-000000000003', 'org', 'external', 'shop.refund', 'approved', '{}')`)
	for id, want := range map[string]string{
		"cccccccc-0000-4000-8000-000000000001": "executeSendWebhook",
		"cccccccc-0000-4000-8000-000000000002": "executeUserAdmin",
		"cccccccc-0000-4000-8000-000000000003": "executeDeclarativePackAction",
	} {
		run, err := a.goActionAdapter(t.Context(), "org", id)
		if err != nil {
			t.Fatal(err)
		}
		if name := runtime.FuncForPC(reflect.ValueOf(run).Pointer()).Name(); !strings.Contains(name, want) {
			t.Errorf("%s: adapter %s, want %s", id, name, want)
		}
	}
}
