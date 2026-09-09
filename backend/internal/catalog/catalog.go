package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	SourceURL     = "https://models.dev/api.json"
	DefaultSource = "models.dev"
	fetchTimeout  = 45 * time.Second
)

// sourceProviders maps models.dev provider ids onto our probe vendors.
// Coding-plan / Vertex mirrors are skipped; China + international catalogs merge.
var sourceProviders = map[string]string{
	"openai":        domain.VendorOpenAI,
	"anthropic":     domain.VendorAnthropic,
	"xai":           domain.VendorGrok,
	"zhipuai":       domain.VendorZhipu,
	"zai":           domain.VendorZhipu,
	"moonshotai":    domain.VendorMoonshot,
	"moonshotai-cn": domain.VendorMoonshot,
	"deepseek":      domain.VendorDeepseek,
}

type FetchFunc func(ctx context.Context) ([]byte, error)

type Result struct {
	Source     string    `json:"source"`
	ModelCount int       `json:"model_count"`
	Vendors    int       `json:"vendors"`
	SyncedAt   time.Time `json:"synced_at"`
	Updated    []string  `json:"updated,omitempty"`
}

type VendorGroup struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Protocol string                `json:"protocol"`
	Models   []domain.CatalogModel `json:"models"`
}

type Snapshot struct {
	Source     string        `json:"source"`
	SyncedAt   *time.Time    `json:"synced_at"`
	ModelCount int           `json:"model_count"`
	Vendors    []VendorGroup `json:"vendors"`
}

type apiProvider struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Models map[string]apiModel `json:"models"`
}

type apiModel struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Status      string        `json:"status"`
	ReleaseDate string        `json:"release_date"`
	Modalities  apiModalities `json:"modalities"`
	Cost        apiCost       `json:"cost"`
}

type apiModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type apiCost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

func DefaultFetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "simple-up-manage/catalog")
	client := &http.Client{Timeout: fetchTimeout}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("models.dev status %d", res.StatusCode)
	}
	return body, nil
}

func Sync(ctx context.Context, db *gorm.DB, fetch FetchFunc) (Result, error) {
	if fetch == nil {
		fetch = DefaultFetch
	}
	raw, err := fetch(ctx)
	if err != nil {
		_ = writeMetaError(db, err)
		return Result{}, err
	}
	rows, err := Parse(raw)
	if err != nil {
		_ = writeMetaError(db, err)
		return Result{}, err
	}
	now := time.Now()
	for i := range rows {
		rows[i].SyncedAt = now
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("vendor IN ?", domain.ProbeVendorIDs()).Delete(&domain.CatalogModel{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("models.dev returned no supported chat models")
		}
		return tx.CreateInBatches(rows, 100).Error
	})
	if err != nil {
		_ = writeMetaError(db, err)
		return Result{}, err
	}
	updated := FillMissingProbeModels(ctx, db, rows)
	meta := domain.CatalogMeta{
		ID:         1,
		Source:     DefaultSource,
		ModelCount: len(rows),
		SyncedAt:   &now,
		LastError:  "",
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&meta).Error; err != nil {
		return Result{}, err
	}
	vendors := map[string]struct{}{}
	for _, r := range rows {
		vendors[r.Vendor] = struct{}{}
	}
	return Result{
		Source:     DefaultSource,
		ModelCount: len(rows),
		Vendors:    len(vendors),
		SyncedAt:   now,
		Updated:    updated,
	}, nil
}

func Load(ctx context.Context, db *gorm.DB) (Snapshot, error) {
	var rows []domain.CatalogModel
	if err := db.WithContext(ctx).Order("vendor, model_id").Find(&rows).Error; err != nil {
		return Snapshot{}, err
	}
	meta := domain.CatalogMeta{ID: 1, Source: DefaultSource}
	_ = db.WithContext(ctx).First(&meta, 1).Error
	return Snapshot{
		Source:     firstNonEmpty(meta.Source, DefaultSource),
		SyncedAt:   meta.SyncedAt,
		ModelCount: len(rows),
		Vendors:    Group(rows),
	}, nil
}

func Group(rows []domain.CatalogModel) []VendorGroup {
	byVendor := map[string][]domain.CatalogModel{}
	for _, r := range rows {
		byVendor[r.Vendor] = append(byVendor[r.Vendor], r)
	}
	out := make([]VendorGroup, 0, len(domain.ProbeVendors()))
	for _, v := range domain.ProbeVendors() {
		models := byVendor[v.ID]
		if models == nil {
			models = []domain.CatalogModel{}
		}
		sort.SliceStable(models, newerReleaseFirst(models))
		out = append(out, VendorGroup{
			ID:       v.ID,
			Name:     v.Name,
			Protocol: v.Protocol,
			Models:   models,
		})
	}
	return out
}

func Parse(raw []byte) ([]domain.CatalogModel, error) {
	var providers map[string]apiProvider
	if err := json.Unmarshal(raw, &providers); err != nil {
		return nil, fmt.Errorf("parse models.dev: %w", err)
	}
	now := time.Now()
	seen := map[string]domain.CatalogModel{}
	for pid, p := range providers {
		vendor, ok := sourceProviders[pid]
		if !ok {
			continue
		}
		for mid, m := range p.Models {
			id := strings.TrimSpace(m.ID)
			if id == "" {
				id = strings.TrimSpace(mid)
			}
			if id == "" || !isChatModel(m) {
				continue
			}
			row := domain.CatalogModel{
				Vendor:      vendor,
				ModelID:     id,
				Name:        firstNonEmpty(strings.TrimSpace(m.Name), id),
				Protocol:    domain.VendorProtocol(vendor),
				InputCost:   m.Cost.Input,
				OutputCost:  m.Cost.Output,
				ReleaseDate: strings.TrimSpace(m.ReleaseDate),
				SyncedAt:    now,
			}
			key := vendor + "\x00" + strings.ToLower(id)
			if prev, ok := seen[key]; ok {
				if prev.Cost() > 0 && row.Cost() == 0 {
					continue
				}
				if row.ReleaseDate == "" {
					row.ReleaseDate = prev.ReleaseDate
				}
			}
			seen[key] = row
		}
	}
	out := make([]domain.CatalogModel, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return vendorIndex(out[i].Vendor) < vendorIndex(out[j].Vendor)
		}
		if c := out[i].Cost() - out[j].Cost(); c != 0 {
			return c < 0
		}
		return out[i].ModelID < out[j].ModelID
	})
	return out, nil
}

func SuggestDefaults(rows []domain.CatalogModel) map[string]string {
	best := map[string]domain.CatalogModel{}
	for _, r := range rows {
		prev, ok := best[r.Vendor]
		if !ok || cheaper(r, prev) {
			best[r.Vendor] = r
		}
	}
	out := map[string]string{}
	for vendor, row := range best {
		out[vendor] = row.ModelID
	}
	return out
}

// FillMissingProbeModels writes the cheapest catalog model into any probe slot
// whose current id is empty or no longer in that vendor's catalog.
func FillMissingProbeModels(ctx context.Context, db *gorm.DB, rows []domain.CatalogModel) []string {
	suggest := SuggestDefaults(rows)
	inCatalog := map[string]map[string]struct{}{}
	for _, r := range rows {
		if inCatalog[r.Vendor] == nil {
			inCatalog[r.Vendor] = map[string]struct{}{}
		}
		inCatalog[r.Vendor][strings.ToLower(r.ModelID)] = struct{}{}
	}
	var cfg domain.SchedulerSettings
	if err := db.WithContext(ctx).First(&cfg, 1).Error; err != nil {
		return nil
	}
	cfg.Normalize()
	var updated []string
	for _, v := range domain.ProbeVendors() {
		cur := strings.ToLower(cfg.ProbeModel(v.ID))
		ids := inCatalog[v.ID]
		_, ok := ids[cur]
		if cur != "" && ok {
			continue
		}
		next := suggest[v.ID]
		if next == "" || strings.EqualFold(next, cfg.ProbeModel(v.ID)) {
			continue
		}
		cfg.SetProbeModel(v.ID, next)
		updated = append(updated, v.ID+":"+next)
	}
	if len(updated) == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Save(&cfg).Error; err != nil {
		return nil
	}
	return updated
}

func isChatModel(m apiModel) bool {
	st := strings.ToLower(strings.TrimSpace(m.Status))
	switch st {
	case "deprecated", "retired", "end_of_life":
		return false
	}
	id := strings.ToLower(strings.TrimSpace(m.ID))
	for _, skip := range []string{"embedding", "whisper", "tts", "dall-e", "dall_e", "moderation", "transcribe", "realtime"} {
		if strings.Contains(id, skip) {
			return false
		}
	}
	if len(m.Modalities.Output) == 0 {
		return true
	}
	for _, out := range m.Modalities.Output {
		if strings.EqualFold(strings.TrimSpace(out), "text") {
			return true
		}
	}
	return false
}

func cheaper(a, b domain.CatalogModel) bool {
	ca, cb := a.Cost(), b.Cost()
	if ca == cb {
		return a.ModelID < b.ModelID
	}
	if ca == 0 && cb > 0 {
		return false
	}
	if cb == 0 && ca > 0 {
		return true
	}
	return ca < cb
}

func newerReleaseFirst(models []domain.CatalogModel) func(i, j int) bool {
	return func(i, j int) bool {
		a, b := models[i].ReleaseDate, models[j].ReleaseDate
		if a != b {
			if a == "" {
				return false
			}
			if b == "" {
				return true
			}
			return a > b
		}
		return models[i].ModelID < models[j].ModelID
	}
}

func vendorIndex(id string) int {
	for i, v := range domain.ProbeVendors() {
		if v.ID == id {
			return i
		}
	}
	return 99
}

func writeMetaError(db *gorm.DB, err error) error {
	if db == nil || err == nil {
		return nil
	}
	meta := domain.CatalogMeta{ID: 1, Source: DefaultSource, LastError: err.Error()}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_error"}),
	}).Create(&meta).Error
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
