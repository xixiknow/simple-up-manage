package upstream

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ErrorInfo struct {
	Code    string
	Type    string
	Message string
}

// ParseError accepts the top-level and nested envelopes used by our providers,
// including response.failed SSE payloads. It never classifies arbitrary HTML.
func ParseError(body []byte) ErrorInfo {
	var envelope struct {
		Code     json.RawMessage `json:"code"`
		Type     string          `json:"type"`
		Message  string          `json:"message"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ErrorInfo{}
	}
	for _, nested := range []json.RawMessage{envelope.Error, envelope.Response.Error} {
		if len(nested) > 0 && string(nested) != "null" {
			var fields struct {
				Code    json.RawMessage `json:"code"`
				Type    string          `json:"type"`
				Message string          `json:"message"`
			}
			if json.Unmarshal(nested, &fields) == nil {
				envelope.Code, envelope.Type, envelope.Message = fields.Code, fields.Type, fields.Message
				break
			}
		}
	}
	var code string
	_ = json.Unmarshal(envelope.Code, &code)
	return ErrorInfo{Code: code, Type: envelope.Type, Message: envelope.Message}
}

func (e ErrorInfo) Kind() string {
	for _, value := range []string{e.Code, e.Type} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "api_key_disabled", "group_deleted", "key_disabled", "account_deactivated":
			return "credential_disabled"
		case "invalid_api_key", "authentication_error", "invalid_token", "token_expired":
			return "authentication"
		case "insufficient_quota", "insufficient_user_quota", "pre_consume_token_quota_failed", "quota_exceeded", "insufficient_balance", "credit_balance_exhausted", "usage_limit_reached":
			return "quota"
		case "model_not_found", "model_not_supported", "unsupported_model", "unsupported_endpoint":
			return "capability"
		case "rate_limit_error", "rate_limit_exceeded", "gateway_queue_full":
			return "rate_limit"
		}
	}
	if strings.EqualFold(e.Type, "billing_error") {
		switch strings.ToLower(strings.TrimSpace(e.Message)) {
		case "insufficient balance", "insufficient account balance":
			return "quota"
		}
	}
	return ""
}

func (e ErrorInfo) Summary(fallback string) string {
	if e.Code == "" && e.Message == "" && strings.HasPrefix(e.Type, "response.") {
		return fallback
	}
	parts := []string{}
	for _, value := range []string{e.Code, e.Type, e.Message} {
		if value != "" && value != "error" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	text := strings.ReplaceAll(strings.ToValidUTF8(strings.Join(parts, ": "), ""), "\x00", "")
	runes := []rune(text)
	return string(runes[:min(len(runes), 500)])
}

func RetryAfter(value string, now time.Time) time.Duration {
	if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && n > 0 {
		return time.Duration(min(n, 86400)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return min(at.Sub(now), 24*time.Hour)
	}
	return 0
}
