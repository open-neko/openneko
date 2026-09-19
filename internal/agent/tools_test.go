package agent

import "testing"

func TestProposalTrustBoundary(t *testing.T) {
	for _, input := range []string{
		`{"action":"a","arguments":{},"summary":"Ask","status":"approved"}`,
		`{"action":"a","arguments":null,"summary":"Ask"}`,
		`{"action":"a","arguments":[],"summary":"Ask"}`,
		`{"action":"","arguments":{},"summary":"Ask"}`,
	} {
		if _, err := ParseProposal([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	for _, input := range []string{
		`{"id":"r","status":"executed"}`,
		`{"id":"r","status":"approved"}`,
		`{"status":"pending_approval"}`,
		`{"status":"denied"}`,
		`{"id":"r","status":"pending_approval","executed":true}`,
	} {
		if _, err := ParseProposalReceipt([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	if _, err := ParseProposalReceipt([]byte(`{"status":"denied","reason":"Not authorized"}`)); err != nil {
		t.Fatal(err)
	}
}
