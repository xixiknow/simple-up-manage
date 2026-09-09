package ops

import (
	"context"

	"simple-up-manage/internal/catalog"
)

func (s *Service) ListModelCatalog(ctx context.Context) (catalog.Snapshot, error) {
	return catalog.Load(ctx, s.DB)
}

func (s *Service) SyncModelCatalog(ctx context.Context) (catalog.Result, error) {
	return catalog.Sync(ctx, s.DB, nil)
}
