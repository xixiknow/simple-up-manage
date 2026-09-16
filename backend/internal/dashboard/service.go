package dashboard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db           *gorm.DB
	Metrics      *Metrics
	settle       *settler
	BalanceStale time.Duration
	stop         chan struct{}
	sampleWG     sync.WaitGroup
	stopOnce     sync.Once
}

func New(db *gorm.DB) *Service {
	s := &Service{
		db:           db,
		Metrics:      NewMetrics(nil),
		settle:       newSettler(db),
		BalanceStale: 2 * time.Minute,
		stop:         make(chan struct{}),
	}
	return s
}

func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	_, _ = EnsureCatalogVersion(ctx, s.db)
	s.flushSamples()
	s.sampleWG.Add(1)
	go s.sampleLoop()
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stop)
		s.sampleWG.Wait()
		s.Metrics.Stop()
		s.flushSamples()
		s.settle.Stop()
	})
}

func (s *Service) sampleLoop() {
	defer s.sampleWG.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.flushSamples()
		case <-s.stop:
			return
		}
	}
}

func (s *Service) flushSamples() {
	if s == nil || s.Metrics == nil || s.settle == nil {
		return
	}
	for _, sample := range s.Metrics.TakeSamples() {
		key := fmt.Sprintf("conc:%s:%s:%d", sample.Family, sample.Bucket.UTC().Format("200601021504"), sample.Peak)
		s.settle.Enqueue(kindConcSample, key, sample, sample.Bucket)
	}
}

func (s *Service) EnqueueStart(p RequestStart) {
	if s == nil || s.settle == nil {
		return
	}
	s.settle.Enqueue(kindReqStart, "req:"+p.UUID+":start", p, p.StartedAt)
}

func (s *Service) EnqueueAttempt(a AttemptFact) {
	if s == nil || s.settle == nil {
		return
	}
	s.settle.Enqueue(kindAttEnd, "att:"+a.UUID+":end", a, a.CompletedAt)
}

func (s *Service) EnqueueEnd(p RequestEnd) {
	if s == nil || s.settle == nil {
		return
	}
	s.settle.Enqueue(kindReqEnd, "req:"+p.UUID+":end", p, p.CompletedAt)
}

func (s *Service) DataQualityExtra() (overflow bool, depth int, warmup bool) {
	if s == nil {
		return false, 0, false
	}
	if s.settle != nil {
		overflow = s.settle.Overflow()
		depth = s.settle.QueueDepth()
	}
	if s.Metrics != nil {
		warmup = !s.Metrics.Snapshot().WindowComplete
	}
	return
}

func (s *Service) LoadSettings() Settings {
	cfg := DefaultSettings()
	if s == nil || s.db == nil {
		return cfg
	}
	var row Settings
	if err := s.db.First(&row, 1).Error; err != nil {
		return cfg
	}
	return row
}

func (s *Service) SaveSettings(in Settings) (Settings, error) {
	if err := ValidateSettings(in); err != nil {
		return Settings{}, err
	}
	in.ID = 1
	in.UpdatedAt = time.Now().UTC()
	if err := s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&in).Error; err != nil {
		return Settings{}, err
	}
	return s.LoadSettings(), nil
}

func ValidateSettings(in Settings) error {
	if in.RenewalHorizonHours <= 0 || in.MinQualitySamples <= 0 || in.MinTTFTSamples <= 0 || in.MaxTTFTP95Ms <= 0 {
		return errors.New("counts and durations must be positive")
	}
	if in.MinSuccessRate <= 0 || in.MinSuccessRate > 1 || in.MinFinanceCoverage <= 0 || in.MinFinanceCoverage > 1 || in.MinCommonDemandCoverage <= 0 || in.MinCommonDemandCoverage > 1 {
		return errors.New("ratios must be in (0,1]")
	}
	return nil
}

func ValidSaleMultiplier(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return errors.New("sale_multiplier must be a finite number")
	}
	if v < 0 {
		return errors.New("sale_multiplier must be >= 0")
	}
	if v > 999999.999999 {
		return errors.New("sale_multiplier exceeds decimal(12,6)")
	}
	scaled := v * 1e6
	if math.Abs(scaled-math.Round(scaled)) > 1e-4 {
		return errors.New("sale_multiplier exceeds 6 decimal places")
	}
	return nil
}

func (s *Service) Purge(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	now := time.Now().UTC()
	factCut := now.Add(-FactRetention)
	if err := s.db.WithContext(ctx).Where("started_at < ? AND completed_at IS NOT NULL", factCut).Delete(&RequestFact{}).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Where("completed_at < ?", factCut).Delete(&AttemptFact{}).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Where("bucket < ?", now.Add(-MinuteRetention)).Delete(&MinuteAgg{}).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Where("processed_at < ?", now.Add(-LedgerRetention)).Delete(&EventLedger{}).Error; err != nil {
		return err
	}
	return nil
}

func (s *Service) InterruptStale(ctx context.Context, age time.Duration) {
	if s == nil || s.db == nil {
		return
	}
	cut := time.Now().Add(-age)
	var facts []RequestFact
	_ = s.db.WithContext(ctx).Where("completed_at IS NULL AND started_at < ? AND interrupted = ?", cut, false).Find(&facts).Error
	now := time.Now().UTC()
	for _, f := range facts {
		s.EnqueueEnd(RequestEnd{UUID: f.UUID, Interrupted: true, CompletedAt: now, Success: false})
	}
}
