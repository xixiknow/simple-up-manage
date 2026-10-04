package ops

import (
	"context"
	"log"

	"simple-up-manage/internal/catalog"
	"simple-up-manage/internal/dashboard"
)

func (s *Service) ListModelCatalog(ctx context.Context) (catalog.Snapshot, error) {
	return catalog.Load(ctx, s.DB)
}

func (s *Service) SyncModelCatalog(ctx context.Context) (catalog.Result, error) {
	res, err := catalog.Sync(ctx, s.DB, nil)
	if err != nil {
		return res, err
	}
	if _, err := dashboard.PublishCatalogVersion(ctx, s.DB); err != nil {
		return res, err
	}
	return res, nil
}

// BillingSyncResult reports one LiteLLM price-card sync round.
type BillingSyncResult struct {
	Models    int    `json:"models"`
	Published bool   `json:"published"`
	Hash      string `json:"hash"`
}

// SyncLiteLLMPrices refreshes the LiteLLM price card. The probe (commit sha /
// content hash) short-circuits unchanged cards; force bypasses the shortcut
// for the periodic full sync. Any failure keeps the previous price snapshot.
func (s *Service) SyncLiteLLMPrices(ctx context.Context, src catalog.LiteLLMSource, force bool) (BillingSyncResult, error) {
	meta := catalog.LiteLLMMetaLoad(s.DB)
	res := BillingSyncResult{Models: meta.ModelCount, Hash: meta.Hash}

	probe, err := catalog.FetchLiteLLMHash(ctx, src)
	if err != nil {
		s.writeLiteLLMError(err)
		return res, err
	}
	if !force && probe != "" && meta.Hash != "" && probe == meta.ProbeHash {
		return res, nil
	}

	body, err := catalog.FetchLiteLLM(ctx, src)
	if err != nil {
		s.writeLiteLLMError(err)
		return res, err
	}
	bodyHash := catalog.HashBody(body)
	if !force && bodyHash == meta.Hash {
		s.clearLiteLLMErrorWithProbe(bodyHash, probe, meta.ModelCount)
		return res, nil
	}

	rows, err := catalog.ParseLiteLLM(body)
	if err != nil {
		s.writeLiteLLMError(err)
		return res, err
	}
	if err := catalog.StoreLiteLLMPrices(ctx, s.DB, rows, bodyHash, probe); err != nil {
		return res, err
	}
	if _, err := dashboard.PublishCatalogVersion(ctx, s.DB); err != nil {
		return BillingSyncResult{Models: len(rows), Hash: bodyHash}, err
	}
	res.Models, res.Published, res.Hash = len(rows), true, bodyHash
	return res, nil
}

func (s *Service) writeLiteLLMError(err error) {
	if err == nil {
		return
	}
	log.Printf("billing prices: %v", err)
	_ = catalog.StoreLiteLLMMetaError(s.DB, err)
}

func (s *Service) clearLiteLLMErrorWithProbe(hash, probe string, models int) {
	_ = catalog.StoreLiteLLMProbe(s.DB, hash, probe, models)
}
