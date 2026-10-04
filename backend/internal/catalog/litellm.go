package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// LiteLLMSourceURL is the upstream model price card (sub2api's billing
	// source); LiteLLMHashURL probes the file's latest commit so the periodic
	// check stays a few-hundred-byte request instead of a full download.
	LiteLLMSourceURL    = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	LiteLLMHashURL      = "https://api.github.com/repos/BerriAI/litellm/commits?path=model_prices_and_context_window.json&per_page=1"
	liteLLMFetchTimeout = 90 * time.Second
	liteLLMMaxBody      = 64 << 20
)

// LiteLLMSource selects where the price card and its change probe live; empty
// fields fall back to the defaults above.
type LiteLLMSource struct {
	RemoteURL string
	HashURL   string
}

// litellmVendors maps LiteLLM litellm_provider ids onto the gateway's probe
// vendors. Providers outside this set (azure, bedrock, vertex_ai, ...) price
// the same models again; keeping only canonical entries leaves one deterministic
// row per model.
var litellmVendors = map[string]string{
	"openai":     domain.VendorOpenAI,
	"anthropic":  domain.VendorAnthropic,
	"deepseek":   domain.VendorDeepseek,
	"xai":        domain.VendorGrok,
	"grok":       domain.VendorGrok,
	"moonshot":   domain.VendorMoonshot,
	"moonshotai": domain.VendorMoonshot,
	"zhipu":      domain.VendorZhipu,
	"zhipuai":    domain.VendorZhipu,
	"zai":        domain.VendorZhipu,
}

// aboveTierPricePattern matches LiteLLM long-context absolute price fields
// (input_cost_per_token_above_272k_tokens / output_cost_per_token_above_200k_tokens).
var aboveTierPricePattern = regexp.MustCompile(`^(input|output)_cost_per_token_above_(\d+)k_tokens$`)

type litellmEntry struct {
	InputCostPerToken                   *float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority           *float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                  *float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority          *float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost         *float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority *float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr *float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             *float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     *float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      *int64   `json:"long_context_input_token_threshold"`
	LongContextInputCostMultiplier      *float64 `json:"long_context_input_cost_multiplier"`
	LongContextOutputCostMultiplier     *float64 `json:"long_context_output_cost_multiplier"`
	Mode                                string   `json:"mode"`
	LiteLLMProvider                     string   `json:"litellm_provider"`
}

func FetchLiteLLM(ctx context.Context, src LiteLLMSource) ([]byte, error) {
	url := src.RemoteURL
	if url == "" {
		url = LiteLLMSourceURL
	}
	return httpGetJSON(ctx, url, liteLLMMaxBody)
}

// FetchLiteLLMHash returns a change probe for the price card. With a hash URL
// it accepts the GitHub commits API (array of objects with "sha") or a bare
// hex digest document; without one it hashes the full card body.
func FetchLiteLLMHash(ctx context.Context, src LiteLLMSource) (string, error) {
	if src.HashURL == "" {
		body, err := FetchLiteLLM(ctx, src)
		if err != nil {
			return "", err
		}
		return HashBody(body), nil
	}
	body, err := httpGetJSON(ctx, src.HashURL, 1<<20)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var commits []struct {
			Sha string `json:"sha"`
		}
		if err := json.Unmarshal(body, &commits); err == nil && len(commits) > 0 && commits[0].Sha != "" {
			return commits[0].Sha, nil
		}
		return "", fmt.Errorf("parse commit hash response")
	}
	if trimmed == "" {
		return "", fmt.Errorf("empty hash response")
	}
	return strings.TrimSpace(trimmed), nil
}

// HashBody is the content hash recorded for a downloaded price card.
func HashBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func httpGetJSON(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "simple-up-manage/billing")
	client := &http.Client{Timeout: liteLLMFetchTimeout}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("billing price source status %d", res.StatusCode)
	}
	return body, nil
}

// ParseLiteLLM converts the LiteLLM card into working-table rows: chat models
// of the six canonical vendors only, one row per lowercased model key, with
// long-context ladders derived from *_above_XXXk_tokens absolute prices.
func ParseLiteLLM(raw []byte) ([]domain.LiteLLMPrice, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse litellm card: %w", err)
	}
	now := time.Now().UTC()
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]domain.LiteLLMPrice, 0, len(keys))
	for _, key := range keys {
		name := strings.ToLower(strings.TrimSpace(key))
		if name == "" || name == "sample_spec" {
			continue
		}
		var entry litellmEntry
		if err := json.Unmarshal(entries[key], &entry); err != nil {
			continue
		}
		vendor, ok := litellmVendors[strings.ToLower(strings.TrimSpace(entry.LiteLLMProvider))]
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(entry.Mode)) {
		case "", "chat", "completion":
		default:
			continue
		}
		if entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil {
			continue
		}
		row := domain.LiteLLMPrice{
			ModelKey: name,
			Vendor:   vendor,
			Mode:     strings.TrimSpace(entry.Mode),
			SyncedAt: now,
		}
		if entry.InputCostPerToken != nil {
			row.InputPricePerToken = *entry.InputCostPerToken
		}
		if entry.OutputCostPerToken != nil {
			row.OutputPricePerToken = *entry.OutputCostPerToken
		}
		if entry.CacheReadInputTokenCost != nil && *entry.CacheReadInputTokenCost > 0 {
			v := *entry.CacheReadInputTokenCost
			row.CacheReadPricePerToken = &v
		}
		if entry.CacheCreationInputTokenCost != nil && *entry.CacheCreationInputTokenCost > 0 {
			v := *entry.CacheCreationInputTokenCost
			row.CacheWrite5mPricePerToken = &v
		}
		if entry.CacheCreationInputTokenCostAbove1hr != nil && *entry.CacheCreationInputTokenCostAbove1hr > 0 {
			v := *entry.CacheCreationInputTokenCostAbove1hr
			row.CacheWrite1hPricePerToken = &v
		}
		if entry.InputCostPerTokenPriority != nil && *entry.InputCostPerTokenPriority > 0 {
			v := *entry.InputCostPerTokenPriority
			row.InputPriorityPerToken = &v
		}
		if entry.OutputCostPerTokenPriority != nil && *entry.OutputCostPerTokenPriority > 0 {
			v := *entry.OutputCostPerTokenPriority
			row.OutputPriorityPerToken = &v
		}
		if entry.CacheReadInputTokenCostPriority != nil && *entry.CacheReadInputTokenCostPriority > 0 {
			v := *entry.CacheReadInputTokenCostPriority
			row.CacheReadPriorityPerToken = &v
		}
		if entry.CacheCreationInputTokenCostPriority != nil && *entry.CacheCreationInputTokenCostPriority > 0 {
			v := *entry.CacheCreationInputTokenCostPriority
			row.CacheWrite5mPriorityPerToken = &v
		}
		applyLiteLLMLongContext(entries[key], entry, &row)
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("litellm card has no usable chat prices")
	}
	return out, nil
}

// applyLiteLLMLongContext resolves the long-context ladder for one entry:
// explicit long_context_* fields win; otherwise *_above_XXXk_tokens absolute
// prices are folded into a threshold + input/output multipliers (multiplier =
// above price ÷ base price, smallest threshold wins). Cache-side above-tier
// fields are ignored — the billing layer scales cache prices with the input
// multiplier.
func applyLiteLLMLongContext(raw json.RawMessage, entry litellmEntry, row *domain.LiteLLMPrice) {
	if entry.LongContextInputTokenThreshold != nil && *entry.LongContextInputTokenThreshold > 0 {
		row.LongCtxThreshold = *entry.LongContextInputTokenThreshold
		if entry.LongContextInputCostMultiplier != nil {
			row.LongCtxInputMult = *entry.LongContextInputCostMultiplier
		}
		if entry.LongContextOutputCostMultiplier != nil {
			row.LongCtxOutputMult = *entry.LongContextOutputCostMultiplier
		}
		return
	}
	if entry.LongContextInputCostMultiplier != nil || entry.LongContextOutputCostMultiplier != nil {
		// Explicit multipliers without a threshold cannot be applied safely.
		return
	}
	if !strings.Contains(string(raw), "_above_") {
		return
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return
	}
	type tierPrices struct{ input, output float64 }
	tiers := map[int64]*tierPrices{}
	for key, value := range fields {
		m := aboveTierPricePattern.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		price, ok := value.(float64)
		if !ok || price <= 0 {
			continue
		}
		thousands, err := strconv.Atoi(m[2])
		if err != nil || thousands <= 0 {
			continue
		}
		threshold := int64(thousands) * 1000
		tp := tiers[threshold]
		if tp == nil {
			tp = &tierPrices{}
			tiers[threshold] = tp
		}
		if m[1] == "input" {
			tp.input = price
		} else {
			tp.output = price
		}
	}
	if len(tiers) == 0 {
		return
	}
	var threshold int64
	for t := range tiers {
		if threshold == 0 || t < threshold {
			threshold = t
		}
	}
	tp := tiers[threshold]
	inputMult, outputMult := 1.0, 1.0
	if tp.input > 0 && row.InputPricePerToken > 0 {
		inputMult = tp.input / row.InputPricePerToken
	}
	if tp.output > 0 && row.OutputPricePerToken > 0 {
		outputMult = tp.output / row.OutputPricePerToken
	}
	// Above prices at or below base price mean no surcharge — no ladder.
	if inputMult <= 1 && outputMult <= 1 {
		return
	}
	row.LongCtxThreshold = threshold
	row.LongCtxInputMult = inputMult
	row.LongCtxOutputMult = outputMult
}

// StoreLiteLLMPrices atomically replaces the working table and records the
// sync result; callers publish a new price snapshot afterwards.
func StoreLiteLLMPrices(ctx context.Context, db *gorm.DB, rows []domain.LiteLLMPrice, hash, probe string) error {
	if db == nil {
		return fmt.Errorf("litellm store: nil db")
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&domain.LiteLLMPrice{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 100).Error; err != nil {
				return err
			}
		}
		return StoreLiteLLMSyncState(tx, hash, probe, len(rows), "")
	})
}

// StoreLiteLLMSyncState upserts the sync meta singleton.
func StoreLiteLLMSyncState(db *gorm.DB, hash, probe string, models int, lastError string) error {
	if db == nil {
		return fmt.Errorf("litellm store: nil db")
	}
	now := time.Now().UTC()
	meta := domain.LiteLLMMeta{ID: 1, Hash: hash, ProbeHash: probe, ModelCount: models, SyncedAt: &now, LastError: lastError}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"hash", "probe_hash", "model_count", "synced_at", "last_error"}),
	}).Create(&meta).Error
}

// StoreLiteLLMProbe records a content hash + probe pair after a download that
// turned out unchanged, so future rounds can skip the download again.
func StoreLiteLLMProbe(db *gorm.DB, hash, probe string, models int) error {
	return StoreLiteLLMSyncState(db, hash, probe, models, "")
}

// StoreLiteLLMMetaError records a failed sync round; the previous price
// snapshot stays in force.
func StoreLiteLLMMetaError(db *gorm.DB, err error) error {
	if db == nil || err == nil {
		return nil
	}
	meta := LiteLLMMetaLoad(db)
	return StoreLiteLLMSyncState(db, meta.Hash, meta.ProbeHash, meta.ModelCount, err.Error())
}

func LiteLLMMetaLoad(db *gorm.DB) domain.LiteLLMMeta {
	var meta domain.LiteLLMMeta
	_ = db.First(&meta, 1).Error
	return meta
}
