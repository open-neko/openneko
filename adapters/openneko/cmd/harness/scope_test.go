package main

import (
	"strings"
	"testing"
)

func TestAdmissionScopeRequiresUnambiguousHostIdentity(t *testing.T) {
	scope, err := admissionScope("org-42", "thread-17")
	if err != nil || scope != "org:org-42\nthread:thread-17" {
		t.Fatalf("scope=%q err=%v", scope, err)
	}
	for _, ids := range [][2]string{
		{"", "thread-17"}, {"org-42", ""},
		{" org-42", "thread-17"}, {"org-42", "thread-17 "},
		{"org-42\nthread:other", "thread-17"}, {"org-42", "thread-17\rorg:other"},
		{strings.Repeat("o", 129), "thread-17"},
	} {
		if scope, err := admissionScope(ids[0], ids[1]); err == nil || scope != "" {
			t.Fatalf("admitted invalid identity %q: scope=%q err=%v", ids, scope, err)
		}
	}
}
