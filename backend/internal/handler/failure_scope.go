package handler

import (
	"simple-up-manage/internal/upstream"
	"time"
)

func parseRetryAfter(value string, now time.Time) time.Duration {
	return upstream.RetryAfter(value, now)
}

func unsupportedCapability(body []byte) bool {
	return upstream.ParseError(body).Kind() == "capability"
}

func authenticationFailure(body []byte) bool {
	kind := upstream.ParseError(body).Kind()
	return kind == "authentication" || kind == "credential_disabled"
}
