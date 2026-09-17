package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultTimeout = 10 * time.Second

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: DefaultTimeout},
	}
}

// WithTimeout returns an independent client configuration with the same transport.
func (c *Client) WithTimeout(timeout time.Duration) *Client {
	cloned := *c.http
	cloned.Timeout = timeout
	return &Client{http: &cloned}
}

func NewStreamingClient() *Client {
	// Slightly above the gateway's 300s overall deadline so the request
	// context (sync 300s / stream first-token 30s) wins.
	return &Client{
		http: &http.Client{Timeout: 305 * time.Second},
	}
}

func JoinEndpoint(baseURL, path string) string {
	baseURL = strings.TrimSpace(baseURL)
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" {
		baseURL = strings.TrimRight(baseURL, "/")
		baseURL = strings.TrimSuffix(baseURL, "/v1")
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return baseURL + path
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/v1")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

type Result struct {
	Headers http.Header
	Status  int
	Body    []byte
}

func (c *Client) GetJSON(ctx context.Context, baseURL, path, apiKey string) (*Result, error) {
	return c.GetJSONWithHeaders(ctx, baseURL, path, apiKey, nil)
}

// GetJSONWithHeaders is GetJSON with additional request headers (e.g. Anthropic's
// x-api-key / anthropic-version for native /v1/models).
func (c *Client) GetJSONWithHeaders(ctx context.Context, baseURL, path, apiKey string, extra http.Header) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, JoinEndpoint(baseURL, path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	for k, vs := range extra {
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return c.do(req)
}

// AnthropicHeaders returns the headers Anthropic-native endpoints require in
// addition to (or instead of) a Bearer token.
func AnthropicHeaders(apiKey string) http.Header {
	return http.Header{
		"X-Api-Key":         {apiKey},
		"Anthropic-Version": {"2023-06-01"},
	}
}

func (c *Client) PostJSON(ctx context.Context, baseURL, path, apiKey string, body any, extra http.Header) (*Result, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, JoinEndpoint(baseURL, path), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return c.do(req)
}

func (c *Client) do(req *http.Request) (*Result, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return &Result{Status: resp.StatusCode, Body: b, Headers: resp.Header.Clone()}, nil
}

func (c *Client) DoRaw(req *http.Request) (*http.Response, error) {
	return c.http.Do(req)
}

type UsageBalance struct {
	Remaining *float64
	Source    string
}

func ParseBalance(body []byte) UsageBalance {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return UsageBalance{}
	}
	if q, ok := asMap(top["quota"]); ok {
		if v, ok := asFloat(q["remaining"]); ok {
			return UsageBalance{Remaining: &v, Source: "quota.remaining"}
		}
	}
	if v, ok := asFloat(top["remaining"]); ok {
		return UsageBalance{Remaining: &v, Source: "remaining"}
	}
	if v, ok := asFloat(top["balance"]); ok {
		return UsageBalance{Remaining: &v, Source: "balance"}
	}
	if data, ok := asMap(top["data"]); ok {
		if q, ok := asMap(data["quota"]); ok {
			if v, ok := asFloat(q["remaining"]); ok {
				return UsageBalance{Remaining: &v, Source: "data.quota.remaining"}
			}
		}
		if v, ok := asFloat(data["remaining"]); ok {
			return UsageBalance{Remaining: &v, Source: "data.remaining"}
		}
		if v, ok := asFloat(data["balance"]); ok {
			return UsageBalance{Remaining: &v, Source: "data.balance"}
		}
	}
	return UsageBalance{}
}

// NewAPIUnlimitedUSD is what new-api reports as hard_limit_usd for tokens with
// unlimited quota (controller/billing.go).
const NewAPIUnlimitedUSD = 100000000

// NewAPIQuotaPerUnit is new-api's default quota units per USD (common.QuotaPerUnit).
const NewAPIQuotaPerUnit = 500000.0

// ParseSubscriptionLimit parses the OpenAI-style
// GET /v1/dashboard/billing/subscription response (new-api / one-api).
// unlimited=true when the token has no quota cap.
func ParseSubscriptionLimit(body []byte) (limitUSD float64, unlimited bool, ok bool) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return 0, false, false
	}
	if _, hasErr := top["error"]; hasErr {
		return 0, false, false
	}
	v, ok := asFloat(top["hard_limit_usd"])
	if !ok {
		return 0, false, false
	}
	return v, v >= NewAPIUnlimitedUSD, true
}

// ParseBillingUsage parses GET /v1/dashboard/billing/usage; total_usage is in
// US cents, returned here as USD.
func ParseBillingUsage(body []byte) (usedUSD float64, ok bool) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return 0, false
	}
	if _, hasErr := top["error"]; hasErr {
		return 0, false
	}
	v, ok := asFloat(top["total_usage"])
	if !ok {
		return 0, false
	}
	return v / 100, true
}

// NewAPISessionCookie turns the operator-pasted access token into a Cookie
// header for GET /api/user/self. new-api authenticates that endpoint with the
// browser session cookie, typically `session=<value>`.
func NewAPISessionCookie(token string) string {
	token = strings.TrimSpace(token)
	token = strings.TrimSpace(strings.TrimPrefix(token, "Cookie:"))
	token = strings.TrimSpace(strings.TrimPrefix(token, "cookie:"))
	if token == "" {
		return ""
	}
	if strings.Contains(token, "=") {
		return token
	}
	return "session=" + token
}

// ParseNewAPIUserSelf parses GET /api/user/self. Remaining quota lives on
// data.quota as raw units (not a {remaining} object) and is converted with
// QuotaPerUnit. quota==0 is a valid remaining balance of $0.
func ParseNewAPIUserSelf(body []byte) (remainingUSD float64, unlimited bool, ok bool) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return 0, false, false
	}
	if success, isBool := top["success"].(bool); isBool && !success {
		return 0, false, false
	}
	data, ok := asMap(top["data"])
	if !ok {
		return 0, false, false
	}
	v, ok := asFloat(data["quota"])
	if !ok {
		return 0, false, false
	}
	return v / NewAPIQuotaPerUnit, false, true
}

// ParseNewAPITokenUsage parses new-api's GET /api/usage/token/ which returns raw
// quota units. Remaining is converted with the default QuotaPerUnit.
func ParseNewAPITokenUsage(body []byte) (remainingUSD float64, unlimited bool, ok bool) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return 0, false, false
	}
	if success, isBool := top["success"].(bool); isBool && !success {
		return 0, false, false
	}
	if code, isBool := top["code"].(bool); isBool && !code {
		return 0, false, false
	}
	data, ok := asMap(top["data"])
	if !ok {
		data = top
	}
	if v, hasUserQuota := asFloat(data["user_quota"]); hasUserQuota {
		return v / NewAPIQuotaPerUnit, false, true
	}
	if u, isBool := data["unlimited_quota"].(bool); isBool && u {
		return 0, true, true
	}
	v, ok := asFloat(data["total_available"])
	if !ok {
		v, ok = asFloat(data["remain_quota"])
	}
	if !ok {
		return 0, false, false
	}
	return v / NewAPIQuotaPerUnit, false, true
}

// MaybeRawQuotaToUSD converts new-api quota units to USD when a billing
// endpoint ignored QuotaDisplayType=TOKENS and returned raw remain+used.
func MaybeRawQuotaToUSD(v float64) float64 {
	if v >= NewAPIQuotaPerUnit && v < NewAPIUnlimitedUSD {
		return v / NewAPIQuotaPerUnit
	}
	return v
}

// ParseNewAPIGroupRatio parses GET /api/pricing and returns the group_ratio map
// (group name -> multiplier over base price).
func ParseNewAPIGroupRatio(body []byte) (map[string]float64, bool) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, false
	}
	raw, ok := asMap(top["group_ratio"])
	if !ok {
		if data, isMap := asMap(top["data"]); isMap {
			raw, ok = asMap(data["group_ratio"])
		}
	}
	if !ok {
		return nil, false
	}
	out := make(map[string]float64, len(raw))
	for k, v := range raw {
		if f, isNum := asFloat(v); isNum {
			out[k] = f
		}
	}
	return out, true
}

func ParseBillingMultiplier(body []byte) (float64, bool) {
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return 0, false
	}
	if v, ok := asFloat(top["effective_rate_multiplier"]); ok {
		return v, true
	}
	if data, ok := asMap(top["data"]); ok {
		if v, ok := asFloat(data["effective_rate_multiplier"]); ok {
			return v, true
		}
	}
	return 0, false
}

func ParseModelIDs(body []byte) []string {
	var top struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, m := range top.Data {
		add(m.ID)
	}
	for _, m := range top.Models {
		add(m.ID)
	}
	return out
}

type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	CostUSD             *float64
	UsageKnown          bool
}

func MergeUsage(dst *TokenUsage, src TokenUsage) {
	if src.UsageKnown {
		dst.UsageKnown = true
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheReadTokens > 0 {
		dst.CacheReadTokens = src.CacheReadTokens
	}
	if src.CacheCreationTokens > 0 {
		dst.CacheCreationTokens = src.CacheCreationTokens
	}
	if src.CostUSD != nil {
		dst.CostUSD = src.CostUSD
	}
}

func ParseUsageJSON(body []byte) TokenUsage {
	var u TokenUsage
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		return u
	}
	absorbUsageMap(&u, top)
	if usage, ok := asMap(top["usage"]); ok {
		absorbUsageMap(&u, usage)
	}
	if msg, ok := asMap(top["message"]); ok {
		if usage, ok := asMap(msg["usage"]); ok {
			absorbUsageMap(&u, usage)
		}
	}
	if resp, ok := asMap(top["response"]); ok {
		if usage, ok := asMap(resp["usage"]); ok {
			absorbUsageMap(&u, usage)
		}
	}
	return u
}

func ParseSSEUsageLine(line string) TokenUsage {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "data:") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	}
	if line == "" || line == "[DONE]" {
		return TokenUsage{}
	}
	return ParseUsageJSON([]byte(line))
}

func absorbUsageMap(u *TokenUsage, m map[string]any) {
	if m == nil {
		return
	}
	if _, ok := m["input_tokens"]; ok {
		u.UsageKnown = true
	}
	if _, ok := m["prompt_tokens"]; ok {
		u.UsageKnown = true
	}
	if _, ok := m["output_tokens"]; ok {
		u.UsageKnown = true
	}
	if _, ok := m["completion_tokens"]; ok {
		u.UsageKnown = true
	}
	if v, ok := asInt(m["input_tokens"]); ok {
		u.InputTokens = v
	}
	if v, ok := asInt(m["prompt_tokens"]); ok {
		u.InputTokens = v
	}
	if v, ok := asInt(m["output_tokens"]); ok {
		u.OutputTokens = v
	}
	if v, ok := asInt(m["completion_tokens"]); ok {
		u.OutputTokens = v
	}
	if v, ok := asInt(m["cache_read_tokens"]); ok {
		u.CacheReadTokens = v
	}
	if v, ok := asInt(m["cache_read_input_tokens"]); ok {
		u.CacheReadTokens = v
	}
	if v, ok := asInt(m["cache_creation_tokens"]); ok {
		u.CacheCreationTokens = v
	}
	if v, ok := asInt(m["cache_creation_input_tokens"]); ok {
		u.CacheCreationTokens = v
	}
	if v, ok := asInt(m["cache_write_tokens"]); ok {
		u.CacheCreationTokens = v
	}
	if details, ok := asMap(m["prompt_tokens_details"]); ok {
		if v, ok := asInt(details["cached_tokens"]); ok {
			u.CacheReadTokens = v
		}
		if v, ok := asInt(details["cache_write_tokens"]); ok {
			u.CacheCreationTokens = v
		}
	}
	if details, ok := asMap(m["input_tokens_details"]); ok {
		if v, ok := asInt(details["cached_tokens"]); ok {
			u.CacheReadTokens = v
		}
		if v, ok := asInt(details["cache_write_tokens"]); ok {
			u.CacheCreationTokens = v
		}
	}
	if v, ok := asFloat(m["cost"]); ok {
		u.CostUSD = &v
	}
	if v, ok := asFloat(m["cost_usd"]); ok {
		u.CostUSD = &v
	}
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		var f float64
		_, err := fmt.Sscanf(strings.TrimSpace(n), "%f", &f)
		return f, err == nil
	default:
		return 0, false
	}
}

func asInt(v any) (int64, bool) {
	f, ok := asFloat(v)
	if !ok {
		return 0, false
	}
	return int64(f), true
}
