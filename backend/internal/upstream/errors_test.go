package upstream

import (
	"strings"
	"testing"
)

func TestProviderErrorEnvelopes(t *testing.T) {
	for _, tc := range []struct{ body, kind string }{
		{`{"code":"GROUP_DELETED","message":"group removed"}`, "credential_disabled"},
		{`{"error":{"code":"API_KEY_DISABLED"}}`, "credential_disabled"},
		{`{"code":"INSUFFICIENT_BALANCE"}`, "quota"},
		{`{"error":{"type":"billing_error","message":"insufficient balance"}}`, "quota"},
		{`{"error":{"type":"billing_error","message":"invalid billing format"}}`, ""},
		{`{"error":{"code":"model_not_found"}}`, "capability"},
		{`{"error":{"code":"gateway_queue_full"}}`, "rate_limit"},
		{`{"type":"response.failed","response":{"error":{"code":"invalid_api_key","message":"expired"}}}`, "authentication"},
		{`{"error":{"code":403,"message":"forbidden"}}`, ""},
		{`<html>GROUP_DELETED</html>`, ""},
	} {
		t.Run(tc.body, func(t *testing.T) {
			if got := ParseError([]byte(tc.body)).Kind(); got != tc.kind {
				t.Fatalf("kind=%s want=%s", got, tc.kind)
			}
		})
	}
	info := ParseError([]byte(`{"error":{"code":"GROUP_DELETED","message":"group removed"}}`))
	if !strings.Contains(info.Summary("Forbidden"), "GROUP_DELETED") {
		t.Fatal("lost diagnostic code")
	}
}
