package ops

import (
	"context"
	"sync"

	"simple-up-manage/internal/domain"
)

// BootstrapKey pulls rate, balance, and models once after a key is created
// or its secret is rotated. Failures are recorded on the key and do not
// fail the caller.
func (s *Service) BootstrapKey(ctx context.Context, keyID uint) {
	var key domain.PlatformKey
	if err := s.DB.WithContext(ctx).Preload("Upstream").First(&key, keyID).Error; err != nil {
		return
	}
	var wg sync.WaitGroup
	if key.Upstream != nil && domain.KindHasBilling(key.Upstream.Kind) {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.RefreshBilling(ctx, keyID)
		}()
		go func() {
			defer wg.Done()
			_ = s.RefreshBalance(ctx, keyID)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.FetchModels(ctx, keyID)
	}()
	wg.Wait()
}
