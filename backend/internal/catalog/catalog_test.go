package catalog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/store"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testCatalogDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := store.Open("sqlite://" + filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	db.Logger = db.Logger.LogMode(logger.Silent)
	if err := store.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

const fixture = `{
  "openai": {
    "id": "openai",
    "name": "OpenAI",
    "models": {
      "gpt-4o-mini": {
        "id": "gpt-4o-mini",
        "name": "GPT-4o mini",
        "release_date": "2024-07-18",
        "modalities": {"input": ["text"], "output": ["text"]},
        "cost": {"input": 0.15, "output": 0.6}
      },
      "text-embedding-3-small": {
        "id": "text-embedding-3-small",
        "name": "Embedding 3 Small",
        "modalities": {"input": ["text"], "output": ["embedding"]},
        "cost": {"input": 0.02, "output": 0}
      }
    }
  },
  "anthropic": {
    "id": "anthropic",
    "models": {
      "claude-haiku-4-5": {
        "id": "claude-haiku-4-5",
        "name": "Claude Haiku 4.5",
        "modalities": {"input": ["text"], "output": ["text"]},
        "cost": {"input": 1, "output": 5}
      }
    }
  },
  "xai": {
    "id": "xai",
    "models": {
      "grok-3-mini": {
        "id": "grok-3-mini",
        "name": "Grok 3 Mini",
        "modalities": {"input": ["text"], "output": ["text"]},
        "cost": {"input": 0.3, "output": 0.5}
      }
    }
  },
  "zhipuai": {
    "id": "zhipuai",
    "models": {
      "glm-4.5-flash": {
        "id": "glm-4.5-flash",
        "name": "GLM-4.5 Flash",
        "modalities": {"input": ["text"], "output": ["text"]},
        "cost": {"input": 0, "output": 0}
      }
    }
  },
  "zai": {
    "id": "zai",
    "models": {
      "glm-4.5-flash": {
        "id": "glm-4.5-flash",
        "name": "GLM-4.5 Flash",
        "modalities": {"input": ["text"], "output": ["text"]},
        "cost": {"input": 0.1, "output": 0.1}
      }
    }
  },
  "moonshotai-cn": {
    "id": "moonshotai-cn",
    "models": {
      "kimi-k2-turbo-preview": {
        "id": "kimi-k2-turbo-preview",
        "name": "Kimi K2 Turbo",
        "modalities": {"input": ["text"], "output": ["text"]},
        "cost": {"input": 0.15, "output": 2.5}
      }
    }
  },
  "deepseek": {
    "id": "deepseek",
    "models": {
      "deepseek-chat": {
        "id": "deepseek-chat",
        "name": "DeepSeek Chat",
        "modalities": {"input": ["text"], "output": ["text"]},
        "cost": {"input": 0.14, "output": 0.28}
      }
    }
  },
  "ignored": {
    "id": "ignored",
    "models": {
      "other": {"id": "other", "name": "Other"}
    }
  }
}`

func TestParseKeepsSupportedChatModels(t *testing.T) {
	rows, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.CatalogModel{}
	for _, r := range rows {
		got[r.Vendor+"/"+r.ModelID] = r
	}
	if _, ok := got["openai/text-embedding-3-small"]; ok {
		t.Fatal("embeddings must be skipped")
	}
	if _, ok := got["openai/gpt-4o-mini"]; !ok {
		t.Fatal("missing openai chat model")
	}
	if got["openai/gpt-4o-mini"].ReleaseDate != "2024-07-18" {
		t.Fatalf("release_date=%q", got["openai/gpt-4o-mini"].ReleaseDate)
	}
	if _, ok := got["anthropic/claude-haiku-4-5"]; !ok {
		t.Fatal("missing anthropic model")
	}
	if _, ok := got["grok/grok-3-mini"]; !ok {
		t.Fatal("missing grok model")
	}
	zhipu, ok := got["zhipu/glm-4.5-flash"]
	if !ok {
		t.Fatal("missing merged zhipu model")
	}
	if zhipu.Cost() == 0 {
		t.Fatal("merged zhipu row should keep the priced zai copy")
	}
	if _, ok := got["moonshot/kimi-k2-turbo-preview"]; !ok {
		t.Fatal("missing moonshot model")
	}
	if _, ok := got["deepseek/deepseek-chat"]; !ok {
		t.Fatal("missing deepseek model")
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 models, got %d", len(got))
	}
}

func TestSuggestDefaultsPrefersPricedCheapest(t *testing.T) {
	rows, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	got := SuggestDefaults(rows)
	if got[domain.VendorOpenAI] != "gpt-4o-mini" {
		t.Fatalf("openai=%q", got[domain.VendorOpenAI])
	}
	if got[domain.VendorDeepseek] != "deepseek-chat" {
		t.Fatalf("deepseek=%q", got[domain.VendorDeepseek])
	}
}

func TestLiveModelsDevShape(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	raw, err := DefaultFetch(ctx)
	if err != nil {
		t.Skipf("models.dev unreachable: %v", err)
	}
	rows, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 {
		t.Fatalf("too few models: %d", len(rows))
	}
	got := map[string]int{}
	for _, r := range rows {
		got[r.Vendor]++
	}
	for _, id := range domain.ProbeVendorIDs() {
		if got[id] == 0 {
			t.Fatalf("vendor %s missing from live catalog", id)
		}
	}
}

func TestGroupPreservesVendorOrder(t *testing.T) {
	rows, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	groups := Group(rows)
	if len(groups) != 6 {
		t.Fatalf("vendors=%d", len(groups))
	}
	if groups[0].ID != domain.VendorOpenAI || groups[5].ID != domain.VendorDeepseek {
		t.Fatalf("order %s ... %s", groups[0].ID, groups[5].ID)
	}
	if groups[1].Protocol != domain.ProtocolAnthropic {
		t.Fatalf("anthropic protocol=%s", groups[1].Protocol)
	}
}

func TestGroupSortsByReleaseDateDesc(t *testing.T) {
	rows := []domain.CatalogModel{
		{Vendor: domain.VendorOpenAI, ModelID: "gpt-4o-mini", ReleaseDate: "2024-07-18"},
		{Vendor: domain.VendorOpenAI, ModelID: "gpt-5.6", ReleaseDate: "2026-03-01"},
		{Vendor: domain.VendorOpenAI, ModelID: "legacy", ReleaseDate: ""},
		{Vendor: domain.VendorOpenAI, ModelID: "gpt-5", ReleaseDate: "2025-08-07"},
	}
	groups := Group(rows)
	got := make([]string, 0, 4)
	for _, m := range groups[0].Models {
		got = append(got, m.ModelID)
	}
	want := []string{"gpt-5.6", "gpt-5", "gpt-4o-mini", "legacy"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestSyncAndFillMissingProbeModels(t *testing.T) {
	db := testCatalogDB(t)
	cfg := domain.DefaultSchedulerSettings()
	cfg.ID = 1
	cfg.ProbeOpenAIModel = "gone-model"
	cfg.ProbeAnthropicModel = "claude-haiku-4-5"
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	res, err := Sync(context.Background(), db, func(context.Context) ([]byte, error) {
		return []byte(fixture), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ModelCount != 6 {
		t.Fatalf("model_count=%d", res.ModelCount)
	}
	var saved domain.SchedulerSettings
	if err := db.First(&saved, 1).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ProbeOpenAIModel != "gpt-4o-mini" {
		t.Fatalf("openai filled to %q", saved.ProbeOpenAIModel)
	}
	if saved.ProbeAnthropicModel != "claude-haiku-4-5" {
		t.Fatalf("anthropic should stay, got %q", saved.ProbeAnthropicModel)
	}
	snap, err := Load(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ModelCount != 6 || snap.Vendors[0].Models[0].ModelID != "gpt-4o-mini" {
		t.Fatalf("snapshot %+v", snap)
	}
}
