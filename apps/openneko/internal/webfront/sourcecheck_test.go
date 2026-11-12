package webfront

import (
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestSourceCheckRoute(t *testing.T) {
	f, _ := settingsFront(t)
	admin := sessionCookie("admin")
	const path = "/api/v1/settings/source-check"
	if w := call(f, "POST", path, `{"type":"postgres"}`, sessionCookie("member")); w.Code != 403 || strings.TrimSpace(w.Body.String()) != `{"error":"admin only"}` {
		t.Fatalf("member: %d %s", w.Code, w.Body)
	}
	if w := call(f, "POST", path, `null`, admin); w.Code != 500 {
		t.Fatalf("null: %d %s", w.Code, w.Body)
	}
	w := call(f, "POST", path, `not json`, admin)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), `{"check":{"ok":false,"supported":true,"type":"postgres","host":"","port":5432,"database":"","user":"","findings":[{"step":"resolve","status":"fail","message":"Enter the host, database, login first.","field":"host"}`) {
		t.Fatalf("not json: %s", w.Body)
	}
	w = call(f, "POST", path, `{"type":["mysql"],"host":"127.0.0.1","port":"9","dbname":"d","user":"u"}`, admin)
	if !strings.Contains(w.Body.String(), `"type":"mysql","host":"127.0.0.1","port":9,`) || !strings.Contains(w.Body.String(), `"step":"port","status":"fail"`) {
		t.Fatalf("answers: %s", w.Body)
	}
	w = call(f, "POST", path, `{"type":"oracle"}`, admin)
	if !strings.HasPrefix(w.Body.String(), `{"check":{"ok":false,"supported":false,"type":"oracle","host":"","port":0,`) {
		t.Fatalf("oracle: %s", w.Body)
	}

	raw := os.Getenv("OPENNEKO_TEST_DATABASE_URL")
	u, _ := url.Parse(raw)
	host, port, _ := net.SplitHostPort(u.Host)
	pw, _ := u.User.Password()
	body := `{"host":"` + host + `","port":` + port + `,"dbname":"postgres","user":"` + u.User.Username() + `","secretRef":"db.pw"}`
	w = call(f, "POST", path, body, admin)
	if !strings.Contains(w.Body.String(), `No stored password is named \"db.pw\". Save the password in the card first.`) {
		t.Fatalf("no secret: %s", w.Body)
	}
	const planted = "planted-source-check-4f2a"
	call(f, "PUT", "/api/v1/settings/source-secrets", `{"name":"db.pw","value":"`+planted+`"}`, admin)
	w = call(f, "POST", path, body, admin)
	if strings.Contains(w.Body.String(), planted) || !strings.Contains(w.Body.String(), `"step":"login","status":"fail","message":"The database refused the login`) {
		t.Fatalf("planted: %s", w.Body)
	}
	call(f, "PUT", "/api/v1/settings/source-secrets", `{"name":"db.pw","value":"`+pw+`"}`, admin)
	w = call(f, "POST", path, body, admin)
	if !strings.Contains(w.Body.String(), `"step":"login","status":"ok"`) || !strings.Contains(w.Body.String(), `"serverVersion":"PostgreSQL `) {
		t.Fatalf("stored password: %s", w.Body)
	}
	call(f, "PUT", "/api/v1/settings/source-secrets", `{"name":"db.pw","value":"`+planted+`"}`, admin)
	if w := call(f, "POST", path, `{"host":"`+host+`","port":`+port+`,"dbname":"postgres","user":"`+planted+`","secretRef":["db.pw"]}`, admin); !strings.Contains(w.Body.String(), `"message":"The database refused the login [REDACTED] with this password.`) {
		t.Fatalf("redact: %s", w.Body)
	}
}
