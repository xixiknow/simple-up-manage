package ops

import (
	"context"

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
