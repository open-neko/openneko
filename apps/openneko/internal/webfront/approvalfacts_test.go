package webfront

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func TestApprovalFactsComeFromThePayload(t *testing.T) {
	payload, _ := jsParse([]byte(`{"operation":"send_email","body":{"to":["Ann <ann@acme.com>","audit@outside.example"],
		"subject":"Invoices","html":"` + strings.Repeat("x", 500) + `"},"intent":"routine digest","target":"acme.com"}`))
	f := buildApprovalFacts("resend.email", payload, []string{"acme.com"}, nil)
	if f.Headline != "Send an email: ann@acme.com, audit@outside.example" {
		t.Errorf("headline %q", f.Headline)
	}
	if !slices.Equal(f.Flags, []string{"Goes to an outside domain: outside.example"}) {
		t.Errorf("flags %v", f.Flags)
	}
	if !slices.Contains(f.Lines, "To: audit@outside.example (outside domain outside.example)") || !slices.Contains(f.Lines, "html: 500 characters") {
		t.Errorf("lines %v", f.Lines)
	}

	hook, _ := jsParse([]byte(`{"url":"https://collect.example/in?d=1","method":"POST"}`))
	f = buildApprovalFacts("send_webhook", hook, nil, func(string) bool { return false })
	if f.Headline != "Send a webhook: collect.example" || !slices.Contains(f.Flags, "Reaches the host collect.example") ||
		!slices.Contains(f.Flags, "No earlier action reached collect.example") {
		t.Errorf("webhook %+v", f)
	}
	role, _ := jsParse([]byte(`{"email":"m@acme.com","role":"admin"}`))
	if f = buildApprovalFacts("user_admin", role, []string{"acme.com"}, nil); !slices.Equal(f.Flags, []string{"Gives the admin role"}) {
		t.Errorf("role %+v", f)
	}
}

func TestApprovalFactsReadTheMailItself(t *testing.T) {
	msg := "From: ops@acme.com\r\nTo: Ann <ann@acme.com>, \"Board\" <board@acme.com>\r\nCc: =?utf-8?q?J=C3=B6rg?= <jorg@outside.example>\r\n" +
		"Bcc: copy@leak.example\r\nSubject: =?utf-8?q?Q3_numbers?=\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("Revenue line\r\n", 60)
	raw := base64.RawURLEncoding.EncodeToString([]byte(msg))
	payload, _ := jsParse([]byte(`{"operation":"send_message","path":{"userId":"me"},"body":{"message":{"raw":"` + raw + `"}},"target":"ann@acme.com"}`))
	f := buildApprovalFacts("google_workspace.gmail", payload, []string{"acme.com"}, nil)
	if f.Headline != "Send or draft an email: ann@acme.com, board@acme.com, jorg@outside.example, copy@leak.example" {
		t.Errorf("headline %q", f.Headline)
	}
	for _, want := range []string{"To: ann@acme.com", "To: board@acme.com", "Cc: jorg@outside.example (outside domain outside.example)",
		"Bcc: copy@leak.example (outside domain leak.example)", "Subject: Q3 numbers"} {
		if !slices.Contains(f.Lines, want) {
			t.Errorf("no line %q in %v", want, f.Lines)
		}
	}
	if !slices.Contains(f.Flags, "Goes to an outside domain: leak.example") || !slices.Contains(f.Flags, "Goes to an outside domain: outside.example") {
		t.Errorf("flags %v", f.Flags)
	}
	for _, l := range append(f.Lines, f.Flags...) {
		if strings.Contains(l, "characters") {
			t.Errorf("the message shows as a length: %v", f.Lines)
		}
	}

	bad, _ := jsParse([]byte(`{"body":{"message":{"raw":"not base64 !!"}}}`))
	if f := buildApprovalFacts("google_workspace.gmail", bad, nil, nil); !slices.Contains(f.Flags, "OpenNeko cannot read the recipients of this email") {
		t.Errorf("unreadable raw %+v", f)
	}
	list, _ := jsParse([]byte(`{"to":"ann@acme.com, x@outside.example"}`))
	if f := buildApprovalFacts("resend.email", list, []string{"acme.com"}, nil); !slices.Equal(f.Flags, []string{"Goes to an outside domain: outside.example"}) {
		t.Errorf("comma list %+v", f)
	}
	deep, _ := jsParse([]byte(`{"a":{"b":{"c":{"d":{"e":{"f":{"notify":"spy@far.example","hook":"https://deep.example/x"}}}}}}}`))
	f = buildApprovalFacts("shop.notify", deep, []string{"acme.com"}, nil)
	if !slices.Contains(f.Flags, "Goes to an outside domain: far.example") || !slices.Contains(f.Flags, "Reaches the host deep.example") {
		t.Errorf("deep leaves %+v", f)
	}
}

func TestOpenAPICardNamesADifferentAPIHost(t *testing.T) {
	surface := jsStringify(openAPIImportSurface(jsObjOf("url", "https://docs.example/spec.json", "host", "docs.example",
		"baseUrl", "https://api.collect.example/v1"), "s1"), false)
	if !strings.Contains(surface, "API calls and the API credential go to api.collect.example. That host differs from the fetch host docs.example.") {
		t.Fatalf("surface %s", surface)
	}
	same := jsStringify(openAPIImportSurface(jsObjOf("url", "https://docs.example/spec.json", "host", "docs.example",
		"baseUrl", "https://docs.example/v1"), "s1"), false)
	if strings.Contains(same, "differs") {
		t.Fatalf("same host flagged: %s", same)
	}
}
