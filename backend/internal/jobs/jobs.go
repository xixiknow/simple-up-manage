package jobs

import (
	"context"
	"log"
	"time"

	"simple-up-manage/internal/config"
	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/ops"
)

func Start(cfg *config.Config, opsSvc *ops.Service, afterCatalog func(), stop <-chan struct{}, dash *dashboard.Service) {
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

	finalizeStale := func(ctx context.Context) {
		n, err := opsSvc.FinalizeStaleInFlightLogs(ctx)
		if err != nil {
			log.Printf("job stale-logs: %v", err)
			return
		}
		if n > 0 {
			log.Printf("job stale-logs: finalized=%d", n)
		}
	}
	purgeLogs := func(ctx context.Context) {
		n, err := opsSvc.PurgeRequestLogs(ctx, cfg.Jobs.LogRetention)
		if err != nil {
			log.Printf("job log-retention: %v", err)
		} else {
			log.Printf("job log-retention: deleted=%d older_than=%s", n, cfg.Jobs.LogRetention)
		}
		n2, err := opsSvc.PurgeRateChangeNotices(ctx)
		if err != nil {
			log.Printf("job rate-notice-retention: %v", err)
			return
		}
		log.Printf("job rate-notice-retention: deleted=%d older_than=%s", n2, ops.RateChangeNoticeRetention)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		finalizeStale(ctx)
	}()
	go runTicker("stale-logs", 20*time.Second, stop, finalizeStale)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		purgeLogs(ctx)
	}()
	go runTicker("log-retention", cfg.Jobs.LogRetentionInterval, stop, purgeLogs)

	if dash != nil {
		go runTicker("dash-purge", 6*time.Hour, stop, func(ctx context.Context) {
			if err := dash.Purge(ctx); err != nil {
				log.Printf("job dash-purge: %v", err)
			}
		})
		go runTicker("dash-stale", time.Minute, stop, func(ctx context.Context) {
			dash.InterruptStale(ctx, 6*time.Minute)
		})
	}
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
