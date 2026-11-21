package webfront

import (
	"slices"
	"strings"
	"testing"
)

func TestApprovalFactsComeFromThePayload(t *testing.T) {
	payload, _ := jsParse([]byte(`{"operation":"send_email","body":{"to":["Ann <ann@acme.com>","audit@outside.example"],
		"subject":"Invoices","html":"` + strings.Repeat("x", 500) + `"},"intent":"routine digest"}`))
	target := "acme.com, outside.example"
	f := buildApprovalFacts("resend.email", &target, payload, []string{"acme.com"}, nil)
	if f.Headline != "Send an email: acme.com, outside.example" {
		t.Errorf("headline %q", f.Headline)
	}
	if !slices.Equal(f.Flags, []string{"Goes to an outside domain: outside.example"}) {
		t.Errorf("flags %v", f.Flags)
	}
	if !slices.Contains(f.Lines, "to: audit@outside.example") || !slices.Contains(f.Lines, "html: 500 characters") {
		t.Errorf("lines %v", f.Lines)
	}

	hook, _ := jsParse([]byte(`{"url":"https://collect.example/in?d=1","method":"POST"}`))
	f = buildApprovalFacts("send_webhook", nil, hook, nil, func(string) bool { return false })
	if f.Headline != "Send a webhook" || !slices.Contains(f.Flags, "Reaches the host collect.example") ||
		!slices.Contains(f.Flags, "No earlier action reached collect.example") {
		t.Errorf("webhook %+v", f)
	}
	role, _ := jsParse([]byte(`{"email":"m@acme.com","role":"admin"}`))
	if f = buildApprovalFacts("user_admin", nil, role, []string{"acme.com"}, nil); !slices.Equal(f.Flags, []string{"Gives the admin role"}) {
		t.Errorf("role %+v", f)
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
