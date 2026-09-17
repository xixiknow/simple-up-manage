package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"gorm.io/gorm"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"
)

func (s *Service) businessProbe(ctx context.Context, key *domain.PlatformKey, apiKey string, options ...ProbeOptions) ProbeOutcome {
	opts := firstProbeOptions(options)
	cfg := s.probeSettings(ctx)
	var catalog []domain.CatalogModel
	_ = s.DB.WithContext(ctx).Find(&catalog).Error
	target := PickProbeTarget(key, cfg, catalog)
	if opts.Protocol != "" {
		if !key.SupportsProtocol(opts.Protocol) {
			return ProbeOutcome{Skipped: true, Reason: "protocol_mismatch", Message: "该 Key 不支持所选探测协议"}
		}
		protocolKey := *key
		protocolKey.Protocols = opts.Protocol
		target = PickProbeTarget(&protocolKey, cfg, catalog)
	}
	if opts.Model != "" {
		target.Model = opts.Model
		target.Vendor = InferVendor(opts.Model, catalog)
		if opts.Protocol == "" && target.Vendor != "" {
			inferred := domain.VendorProtocol(target.Vendor)
			if key.SupportsProtocol(inferred) {
				target.Protocol = inferred
			}
		}
	}
	out := ProbeOutcome{Protocol: target.Protocol, Model: target.Model, Vendor: target.Vendor, Path: "/v1/chat/completions"}
	if target.Protocol == "" {
		out.Error = "key has no effective protocol"
		return out
	}
	if target.Protocol == domain.ProtocolAnthropic {
		out.Path = "/v1/messages"
	}
	// Rotate observed dimensions by their least recent probe. Never replay user
	// prompts: only the model, endpoint and transport mode are reused.
	var dims []domain.RequestAttempt
	if err := s.DB.WithContext(ctx).Model(&domain.RequestAttempt{}).Select("protocol, model, path, stream").Where("platform_key_id = ? AND stats_version = ? AND completed_at > ? AND path IN ?", key.ID, domain.AttemptStatsVersion, time.Now().Add(-24*time.Hour), []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"}).Group("protocol, model, path, stream").Order("MAX(completed_at) DESC").Limit(32).Find(&dims).Error; err != nil {
		out.Error = err.Error()
		return out
	}
	var oldest time.Time
	chosen := false
	for _, d := range dims {
		if !key.SupportsProtocol(d.Protocol) {
			continue
		}
		if opts.Model != "" && d.Model != opts.Model {
			continue
		}
		if opts.Protocol != "" && d.Protocol != opts.Protocol {
			continue
		}
		var last domain.ProbeLog
		err := s.DB.WithContext(ctx).Where("platform_key_id = ? AND kind = ? AND protocol = ? AND model = ? AND path = ? AND stream = ?", key.ID, domain.ProbeDeep, d.Protocol, d.Model, d.Path, d.Stream).Order("created_at DESC").First(&last).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			out.Error = err.Error()
			return out
		}
		if !chosen || last.CreatedAt.Before(oldest) {
			chosen = true
			oldest = last.CreatedAt
			out.Protocol = d.Protocol
			out.Model = d.Model
			out.Path = d.Path
			out.Stream = d.Stream
			out.Vendor = domain.VendorOpenAI
			if d.Protocol == domain.ProtocolAnthropic {
				out.Vendor = domain.VendorAnthropic
			}
			for _, m := range catalog {
				if m.ModelID == d.Model {
					out.Vendor = m.Vendor
					break
				}
			}
		}
	}
	prompt := "Reply OK."
	if opts.Prompt != nil {
		prompt = *opts.Prompt
	}
	return s.sendBusinessProbe(ctx, key, apiKey, out, prompt)
}

func (s *Service) sendBusinessProbe(ctx context.Context, key *domain.PlatformKey, apiKey string, out ProbeOutcome, prompt string) ProbeOutcome {
	body := map[string]any{"model": out.Model, "stream": out.Stream, "messages": []map[string]string{{"role": "user", "content": prompt}}}
	if out.Path == "/v1/responses" {
		delete(body, "messages")
		body["input"] = prompt
		body["max_output_tokens"] = 256
	} else {
		body["max_tokens"] = 256
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.JoinEndpoint(key.Upstream.BaseURL, out.Path), bytes.NewReader(raw))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	if out.Protocol == domain.ProtocolAnthropic {
		for k, v := range upstream.AnthropicHeaders(apiKey) {
			req.Header[k] = v
		}
	}
	res, err := s.Client.DoRaw(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer res.Body.Close()
	out.StatusCode = res.StatusCode
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		out.RetryAfter = res.Header.Get("Retry-After")
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		out.Error = string(raw)
		return out
	}
	if err := upstream.ValidateProbeResponse(out.Path, res.Header.Get("Content-Type"), out.Stream, res.Body); err != nil {
		out.Error = err.Error()
		return out
	}
	out.Success = true
	return out
}
