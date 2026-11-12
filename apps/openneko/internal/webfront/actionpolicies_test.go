package webfront

import (
	"strings"
	"testing"
)

const rulePolicyID = "b0000000-0000-4000-8000-000000000001"

func TestSettingsST2AdminOnly(t *testing.T) {
	f, _ := settingsFront(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/settings/graphjin-config", ""},
		{"PATCH", "/api/v1/settings/graphjin-config", `{"sourceConfigEnabled":false}`},
		{"GET", "/api/v1/policies", ""},
		{"GET", "/api/v1/policies/" + rulePolicyID, ""},
		{"PATCH", "/api/v1/policies/" + rulePolicyID, `{"approverGroupId":null}`},
	} {
		for _, who := range []map[string]string{sessionCookie("member")} {
			if w := call(f, c.method, c.path, c.body, who); w.Code != 403 || !strings.Contains(w.Body.String(), `"admin only"`) {
				t.Errorf("%s %s as %v: %d %q", c.method, c.path, who, w.Code, w.Body.String())
			}
		}
	}
}

func TestGraphjinConfigSettings(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	path := "/api/v1/settings/graphjin-config"
	if w := call(f, "GET", path, "", admin); w.Body.String() != `{"settings":{"sourceConfigEnabled":true},"source":"default"}` {
		t.Errorf("default: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PATCH", path, `null`, admin); w.Code != 400 {
		t.Errorf("null: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PATCH", path, `{`, admin); w.Code != 400 {
		t.Errorf("bad JSON: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PATCH", path, `{"sourceConfigEnabled":false}`, admin); w.Body.String() != `{"settings":{"sourceConfigEnabled":false},"source":"org"}` {
		t.Errorf("off: %d %s", w.Code, w.Body)
	}
	row := `select enabled || ',' || config::text || ',' || provider || ',' || secrets::text from llm_provider_config where org_id = 'org' and scope = 'graphjin-config'`
	if got := queryText(t, db, row); got != `false,{"sourceConfigEnabled": false},graphjin-config,{}` {
		t.Errorf("row: %s", got)
	}
	if w := call(f, "PATCH", path, `{"other":1}`, admin); w.Body.String() != `{"settings":{"sourceConfigEnabled":false},"source":"org"}` {
		t.Errorf("unchanged: %d %s", w.Code, w.Body)
	}
	call(f, "PATCH", path, `{"sourceConfigEnabled":true}`, admin)
	if got := queryText(t, db, `select count(*) || ',' || bool_and(enabled) from llm_provider_config where org_id = 'org' and scope = 'graphjin-config'`); got != "1,true" {
		t.Errorf("rows: %s", got)
	}
	seed(t, db, `update llm_provider_config set enabled = true, config = 'null' where org_id = 'org' and scope = 'graphjin-config'`)
	if w := call(f, "GET", path, "", admin); w.Body.String() != `{"settings":{"sourceConfigEnabled":true},"source":"org"}` {
		t.Errorf("null config: %s", w.Body)
	}
}

func TestActionPolicies(t *testing.T) {
	f, db := settingsFront(t)
	admin := sessionCookie("admin")
	seed(t, db, `insert into action_policy (id, org_id, name, mode, priority, applies_to_kinds, allowed_targets, limits, created_at, updated_at)
		values ($1, 'org', 'B rule', 'approval', 10, '{email.send}', '{"patterns":["a@x.com"]}', '{"10":1,"b":2}', '2026-11-01T00:00:00Z', '2026-11-02T00:00:00Z'),
		('b0000000-0000-4000-8000-000000000002', 'org', 'A rule', 'auto', 20, '{}', null, '{}', '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z')`, rulePolicyID)
	w := call(f, "GET", "/api/v1/policies", "", admin)
	want := `{"policies":[{"id":"` + rulePolicyID + `","name":"B rule","description":"","appliesToKinds":["email.send"],"appliesToScopes":[],` +
		`"mode":"approval","riskThresholdAutoApprove":null,"allowedTargets":{"patterns":["a@x.com"]},"deniedTargets":null,` +
		`"limits":{"10":1,"b":2},"approverGroupId":null,"priority":10,"enabled":true,` +
		`"createdAt":"2026-11-01T00:00:00.000Z","updatedAt":"2026-11-02T00:00:00.000Z"},{"id":"b0000000-0000-4000-8000-000000000002"`
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), want) {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}
	w = call(f, "GET", "/api/v1/policies/"+rulePolicyID, "", admin)
	if !strings.Contains(w.Body.String(), `"enabled":true,"createdByThreadId":null,"createdByRunId":null,"createdAt"`) {
		t.Errorf("detail: %d %s", w.Code, w.Body)
	}
	for _, id := range []string{"nope", "b0000000-0000-4000-8000-000000000009"} {
		if w := call(f, "GET", "/api/v1/policies/"+id, "", admin); w.Code != 404 || w.Body.String() != `{"error":"not found"}` {
			t.Errorf("missing %s: %d %s", id, w.Code, w.Body)
		}
	}

	group := queryText(t, db, `select id::text from user_group where org_id = 'org' and slug = 'administrators'`)
	path := "/api/v1/policies/" + rulePolicyID
	for _, c := range []struct {
		body string
		code int
		out  string
	}{
		{``, 400, `{"error":"send approverGroupId or allowedTargets"}`},
		{`{"x":1}`, 400, `{"error":"send approverGroupId or allowedTargets"}`},
		{`{"approverGroupId":5}`, 400, `{"error":"approverGroupId must be a group id or null"}`},
		{`{"approverGroupId":""}`, 400, `{"error":"approverGroupId must be a group id or null"}`},
		{`{"approverGroupId":"b0000000-0000-4000-8000-000000000009"}`, 404, `{"error":"group not found"}`},
		{`{"approverGroupId":"x"}`, 404, `{"error":"group not found"}`},
		{`{"approverGroupId":"` + group + `"}`, 200, `{"policy":{"id":"` + rulePolicyID + `","approverGroupId":"` + group + `","allowedTargets":{"patterns":["a@x.com"]}}}`},
		{`{"allowedTargets":{"patterns":5,"onMiss":"deny"}}`, 400, `{"error":"patterns must be a list of strings"}`},
		{`{"allowedTargets":"x"}`, 400, `{"error":"patterns must be a list of strings"}`},
		{`{"allowedTargets":{"patterns":["a b"],"onMiss":"deny"}}`, 400, `{"error":"\"a b\" is not a valid pattern"}`},
		{`{"allowedTargets":{"patterns":"a@x.com"}}`, 400, `{"error":"onMiss must be \"next\" or \"deny\""}`},
		{`{"allowedTargets":{"patterns":" b@x.com\n,a@x.com,b@x.com","onMiss":"next"}}`, 200,
			`{"policy":{"id":"` + rulePolicyID + `","approverGroupId":"` + group + `","allowedTargets":{"on_miss":"next","patterns":["b@x.com","a@x.com"]}}}`},
		{`{"approverGroupId":null,"allowedTargets":{"patterns":[" "],"onMiss":"deny"}}`, 200, `{"policy":{"id":"` + rulePolicyID + `","approverGroupId":null,"allowedTargets":null}}`},
		{`{"allowedTargets":{"patterns":["a@x.com"],"onMiss":"deny"}}`, 200, `{"policy":{"id":"` + rulePolicyID + `","approverGroupId":null,"allowedTargets":{"patterns":["a@x.com"]}}}`},
		{`{"allowedTargets":null}`, 200, `{"policy":{"id":"` + rulePolicyID + `","approverGroupId":null,"allowedTargets":null}}`},
	} {
		if w := call(f, "PATCH", path, c.body, admin); w.Code != c.code || w.Body.String() != c.out {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	if got := queryText(t, db, `select (allowed_targets is null) || ',' || (updated_at > '2026-11-02') from action_policy where id = $1`, rulePolicyID); got != "true,true" {
		t.Errorf("row: %s", got)
	}
	if w := call(f, "PATCH", "/api/v1/policies/b0000000-0000-4000-8000-000000000009", `{"approverGroupId":null}`, admin); w.Code != 404 {
		t.Errorf("missing policy: %d %s", w.Code, w.Body)
	}
	many := make([]string, 201)
	for i := range many {
		many[i] = `"p` + strings.Repeat("x", i) + `"`
	}
	if w := call(f, "PATCH", path, `{"allowedTargets":{"patterns":[`+strings.Join(many, ",")+`],"onMiss":"deny"}}`, admin); w.Body.String() != `{"error":"use at most 200 patterns"}` {
		t.Errorf("many: %d %s", w.Code, w.Body)
	}
}

func TestPersona(t *testing.T) {
	f, db := settingsFront(t)
	admin, member := sessionCookie("admin"), sessionCookie("member")
	path := "/api/v1/settings/persona"
	if w := call(f, "GET", path, "", member); w.Code != 200 || w.Body.String() != `{"profile":null,"promptPreview":""}` {
		t.Errorf("empty: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct {
		who  map[string]string
		body string
		out  string
	}{
		{member, `{"userId":""}`, `{"error":"member member may not write persona (org default)"}`},
		{member, `{"userId":"admin"}`, `{"error":"member member may not write persona admin"}`},
	} {
		if w := call(f, "PUT", path, c.body, c.who); w.Code != 403 || w.Body.String() != c.out {
			t.Errorf("%s: %d %s", c.body, w.Code, w.Body)
		}
	}
	if w := call(f, "PUT", path, `null`, member); w.Code != 500 {
		t.Errorf("null: %d %s", w.Code, w.Body)
	}
	w := call(f, "PUT", path, `{"userId":"","displayName":"Org","roleTemplate":"COO","focusAreas":["cash"," ",3],"answers":{"b":1,"2":true,"a":[1]},"briefMd":"  Brief  "}`, admin)
	if !strings.Contains(w.Body.String(), `"orgId":"org","userId":"","displayName":"Org","roleTemplate":"COO","focusAreas":["cash"],"answers":{"2":true,"a":[1],"b":1},"briefMd":"  Brief  "},`+
		`"promptPreview":"<operator-profile>\nYou are working for Org.\nTheir role: COO.\nThey care most about:\n- cash\n\nBrief\n</operator-profile>"}`) {
		t.Errorf("org default: %d %s", w.Code, w.Body)
	}
	if w := call(f, "GET", path, "", member); !strings.Contains(w.Body.String(), `"userId":"","displayName":"Org"`) {
		t.Errorf("fallback: %s", w.Body)
	}
	if w := call(f, "PUT", path, `{"roleTemplate":"CFO"}`, member); !strings.Contains(w.Body.String(), `"userId":"member","displayName":null,"roleTemplate":"CFO","focusAreas":[],"answers":{},"briefMd":""},"promptPreview":"<operator-profile>\nTheir role: CFO.\n</operator-profile>"}`) {
		t.Errorf("own: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PUT", path, `{"displayName":"Mo","answers":"x"}`, member); !strings.Contains(w.Body.String(), `"displayName":"Mo","roleTemplate":"CFO"`) {
		t.Errorf("merge: %d %s", w.Code, w.Body)
	}
	if w := call(f, "PUT", path, `{"userId":"member","displayName":null,"answers":[1]}`, admin); !strings.Contains(w.Body.String(), `"displayName":null,"roleTemplate":"CFO","focusAreas":[],"answers":[1]`) {
		t.Errorf("admin edit: %d %s", w.Code, w.Body)
	}
	if w := call(f, "GET", path, "", member); !strings.Contains(w.Body.String(), `"userId":"member"`) {
		t.Errorf("own first: %s", w.Body)
	}
	if got := queryText(t, db, `select count(*) from operator_profile where org_id = 'org'`); got != "2" {
		t.Errorf("rows: %s", got)
	}
}
