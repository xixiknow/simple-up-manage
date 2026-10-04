package catalog

import (
	"context"
	"errors"
	"testing"

	"simple-up-manage/internal/domain"
)

const litellmFixture = `{
  "sample_spec": {"max_input_tokens": 1000},
  "claude-sonnet-4-5": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 3e-6,
    "output_cost_per_token": 1.5e-5,
    "cache_read_input_token_cost": 3e-7,
    "cache_creation_input_token_cost": 3.75e-6,
    "cache_creation_input_token_cost_above_1hr": 6e-6
  },
  "claude-sonnet-4-5-20250929": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 3e-6,
    "output_cost_per_token": 1.5e-5
  },
  "gpt-5.1": {
    "litellm_provider": "openai",
    "mode": "chat",
    "input_cost_per_token": 1.25e-6,
    "output_cost_per_token": 1e-5,
    "input_cost_per_token_priority": 2.5e-6,
    "output_cost_per_token_priority": 2e-5,
    "input_cost_per_token_above_272k_tokens": 2.5e-6,
    "output_cost_per_token_above_272k_tokens": 1.5e-5
  },
  "azure/gpt-5.1": {
    "litellm_provider": "azure",
    "mode": "chat",
    "input_cost_per_token": 9e-6,
    "output_cost_per_token": 9e-5
  },
  "text-embedding-3-small": {
    "litellm_provider": "openai",
    "mode": "embedding",
    "input_cost_per_token": 2e-8,
    "output_cost_per_token": 0
  },
  "deepseek-v4-flash": {
    "litellm_provider": "deepseek",
    "mode": "chat",
    "input_cost_per_token": 2.2e-7,
    "output_cost_per_token": 6.6e-7,
    "cache_read_input_token_cost": 7e-9
  }
}`

func findRow(t *testing.T, rows []domain.LiteLLMPrice, key string) domain.LiteLLMPrice {
	t.Helper()
	for _, r := range rows {
		if r.ModelKey == key {
			return r
		}
	}
	t.Fatalf("model %q not parsed", key)
	return domain.LiteLLMPrice{}
}

func TestParseLiteLLM(t *testing.T) {
	rows, err := ParseLiteLLM([]byte(litellmFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 rows (sample_spec, azure, embedding filtered), got %d: %+v", len(rows), rows)
	}

	sonnet := findRow(t, rows, "claude-sonnet-4-5")
	if sonnet.Vendor != domain.VendorAnthropic {
		t.Fatalf("sonnet vendor = %q", sonnet.Vendor)
	}
	if sonnet.CacheWrite5mPricePerToken == nil || *sonnet.CacheWrite5mPricePerToken != 3.75e-6 {
		t.Fatalf("sonnet 5m cache price = %v", sonnet.CacheWrite5mPricePerToken)
	}
	if sonnet.CacheWrite1hPricePerToken == nil || *sonnet.CacheWrite1hPricePerToken != 6e-6 {
		t.Fatalf("sonnet 1h cache price = %v", sonnet.CacheWrite1hPricePerToken)
	}

	// Long-context ladder derived from above_XXXk fields (input ×2, output ×1.5).
	gpt := findRow(t, rows, "gpt-5.1")
	if gpt.LongCtxThreshold != 272000 {
		t.Fatalf("gpt long-ctx threshold = %d", gpt.LongCtxThreshold)
	}
	if gpt.LongCtxInputMult != 2 || gpt.LongCtxOutputMult != 1.5 {
		t.Fatalf("gpt long-ctx mults = %v/%v", gpt.LongCtxInputMult, gpt.LongCtxOutputMult)
	}
	if gpt.InputPriorityPerToken == nil || *gpt.InputPriorityPerToken != 2.5e-6 {
		t.Fatalf("gpt priority input = %v", gpt.InputPriorityPerToken)
	}

	// Versioned duplicate folds onto the same card entry.
	findRow(t, rows, "claude-sonnet-4-5-20250929")

	ds := findRow(t, rows, "deepseek-v4-flash")
	if ds.CacheReadPricePerToken == nil || *ds.CacheReadPricePerToken != 7e-9 {
		t.Fatalf("deepseek cache read = %v", ds.CacheReadPricePerToken)
	}
}

func TestStoreLiteLLMPricesReplaces(t *testing.T) {
	db := testCatalogDB(t)
	rows, err := ParseLiteLLM([]byte(litellmFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := StoreLiteLLMPrices(context.Background(), db, rows, "hash-1", "probe-1"); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := StoreLiteLLMPrices(context.Background(), db, rows[:1], "hash-2", "probe-2"); err != nil {
		t.Fatalf("re-store: %v", err)
	}
	var n int64
	if err := db.Model(&domain.LiteLLMPrice{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("working table not replaced: %d rows", n)
	}
	meta := LiteLLMMetaLoad(db)
	if meta.Hash != "hash-2" || meta.ProbeHash != "probe-2" || meta.ModelCount != 1 {
		t.Fatalf("meta = %+v", meta)
	}

	// Error recording keeps the previous sync state intact.
	if err := StoreLiteLLMMetaError(db, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	meta = LiteLLMMetaLoad(db)
	if meta.LastError == "" || meta.Hash != "hash-2" {
		t.Fatalf("meta after error = %+v", meta)
	}
}
