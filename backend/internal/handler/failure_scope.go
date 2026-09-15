package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func parseRetryAfter(value string, now time.Time) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n > 0 {
		return time.Duration(min(n, 86400)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return min(at.Sub(now), 24*time.Hour)
	}
	return 0
}

func unsupportedCapability(body []byte) bool {
	var v struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	for _, code := range []string{v.Error.Code, v.Error.Type} {
		switch code {
		case "model_not_found", "model_not_supported", "unsupported_model", "unsupported_endpoint":
			return true
		}
	}
	return false
}

func authenticationFailure(body []byte) bool {
	var v struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	for _, code := range []string{v.Error.Code, v.Error.Type} {
		switch code {
		case "invalid_api_key", "authentication_error", "invalid_token", "token_expired":
			return true
		}
	}
	return false
}
