package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestKilocodeOrgIDRequiresExplicitOrgBilling(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"org_id": "org-123",
		},
	}
	if got := kilocodeOrgID(auth); got != "" {
		t.Fatalf("expected empty org id without use_org_billing, got %q", got)
	}

	auth.Metadata["use_org_billing"] = true
	if got := kilocodeOrgID(auth); got != "org-123" {
		t.Fatalf("expected org id when use_org_billing=true, got %q", got)
	}
}

func TestKilocodeUseOrgBillingAttribute(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"use_org_billing": "true",
			"org_id":          "org-456",
		},
	}
	if !kilocodeUseOrgBilling(auth) {
		t.Fatal("expected use_org_billing attribute to enable org billing")
	}
	if got := kilocodeOrgID(auth); got != "org-456" {
		t.Fatalf("expected org id from attributes, got %q", got)
	}
}

func TestShouldSkipKilocodeSSEMetaLine(t *testing.T) {
	cases := []struct {
		line string
		skip bool
	}{
		{line: "", skip: true},
		{line: ": OPENROUTER PROCESSING", skip: true},
		{line: "event: ping", skip: true},
		{line: "id: 1", skip: true},
		{line: "retry: 1000", skip: true},
		{line: `data: {"choices":[{"delta":{"content":"hi"}}]}`, skip: false},
	}
	for _, tc := range cases {
		if got := shouldSkipKilocodeSSEMetaLine([]byte(tc.line)); got != tc.skip {
			t.Fatalf("shouldSkipKilocodeSSEMetaLine(%q) = %v, want %v", tc.line, got, tc.skip)
		}
	}
}
