package jobs

import (
	"context"
	"log"
	"time"

	"simple-up-manage/internal/config"
	"simple-up-manage/internal/ops"
)

func Start(cfg *config.Config, opsSvc *ops.Service, afterCatalog func(), stop <-chan struct{}) {
	go runTicker("balance", cfg.Jobs.BalanceInterval, stop, func(ctx context.Context) {
		ok, fail := opsSvc.RefreshAllBalances(ctx)
		log.Printf("job balance: ok=%d failed=%d", ok, fail)
	})
	go runTicker("billing", cfg.Jobs.BillingInterval, stop, func(ctx context.Context) {
		ok, fail := opsSvc.RefreshAllBilling(ctx)
		log.Printf("job billing: ok=%d failed=%d", ok, fail)
	})
	go runTicker("probe", cfg.Jobs.ProbeInterval, stop, func(ctx context.Context) {
		ok, fail, skipped := opsSvc.ProbeAllEnabled(ctx, cfg.Jobs.ProbeInterval)
		log.Printf("job probe: ok=%d failed=%d skipped=%d", ok, fail, skipped)
	})
	syncCatalog := func(ctx context.Context) {
		res, err := opsSvc.SyncModelCatalog(ctx)
		if err != nil {
			log.Printf("job catalog: %v", err)
			return
		}
		if afterCatalog != nil {
			afterCatalog()
		}
		log.Printf("job catalog: models=%d vendors=%d", res.ModelCount, res.Vendors)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		syncCatalog(ctx)
	}()
	go runTicker("catalog", cfg.Jobs.CatalogInterval, stop, syncCatalog)

	purgeLogs := func(ctx context.Context) {
		n, err := opsSvc.PurgeRequestLogs(ctx, cfg.Jobs.LogRetention)
		if err != nil {
			log.Printf("job log-retention: %v", err)
			return
		}
		log.Printf("job log-retention: deleted=%d older_than=%s", n, cfg.Jobs.LogRetention)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		purgeLogs(ctx)
	}()
	go runTicker("log-retention", cfg.Jobs.LogRetentionInterval, stop, purgeLogs)
}

func runTicker(name string, interval time.Duration, stop <-chan struct{}, fn func(context.Context)) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	log.Printf("job %s started interval=%s", name, interval)
	for {
		select {
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
			fn(ctx)
			cancel()
		case <-stop:
			log.Printf("job %s stopped", name)
			return
		}
	}
}
