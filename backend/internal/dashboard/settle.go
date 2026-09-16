package dashboard

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type queuedEvent struct {
	Key      string
	Kind     string
	Payload  []byte
	Attempts int
	Next     time.Time
	At       time.Time
}

const (
	kindReqStart   = "request_start"
	kindAttEnd     = "attempt_end"
	kindReqEnd     = "request_end"
	kindConcSample = "conc_sample"
)

type settler struct {
	db       *gorm.DB
	mu       sync.Mutex
	q        []queuedEvent
	overflow bool
	stop     chan struct{}
	wg       sync.WaitGroup
}

func newSettler(db *gorm.DB) *settler {
	s := &settler{db: db, stop: make(chan struct{})}
	s.wg.Add(1)
	go s.loop()
	return s
}

func (s *settler) Stop() {
	if s == nil {
		return
	}
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.wg.Wait()
}

func (s *settler) Enqueue(kind, key string, payload any, at time.Time) {
	if s == nil {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if time.Since(at) > ReplayRejectAge {
		return
	}
	b, _ := json.Marshal(payload)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.q) >= QueueLimit {
		s.overflow = true
		_ = s.db.Create(&Gap{StartedAt: at, EndedAt: at, Reason: "queue_overflow", CreatedAt: time.Now().UTC()}).Error
		_ = s.db.Model(&Meta{}).Where("id = 1").Update("overflow", true).Error
		return
	}
	s.q = append(s.q, queuedEvent{Key: key, Kind: kind, Payload: b, Next: time.Now(), At: at})
}

func (s *settler) Overflow() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overflow
}

func (s *settler) QueueDepth() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.q)
}

func (s *settler) loop() {
	defer s.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	s.recoverUnprocessed()
	for {
		select {
		case <-s.stop:
			s.drainFinal()
			return
		case <-t.C:
			s.drainOnce()
		}
	}
}

func (s *settler) drainFinal() {
	// Handler completion can arrive before its deferred attempt event. Ignore
	// retry backoff on shutdown and make another pass while dependencies progress.
	for {
		s.mu.Lock()
		before := len(s.q)
		for i := range s.q {
			s.q[i].Next = time.Time{}
		}
		s.mu.Unlock()
		if before == 0 {
			return
		}
		s.drainOnce()
		if s.QueueDepth() >= before {
			log.Printf("dashboard shutdown: %d unsettled events", before)
			return
		}
	}
}

func (s *settler) drainOnce() {
	now := time.Now()
	s.mu.Lock()
	var due []queuedEvent
	rest := s.q[:0]
	for _, e := range s.q {
		if !e.Next.After(now) {
			due = append(due, e)
		} else {
			rest = append(rest, e)
		}
	}
	s.q = rest
	s.mu.Unlock()
	for _, e := range due {
		if err := s.process(e); err != nil {
			e.Attempts++
			wait := time.Duration(1<<min(e.Attempts, 5)) * time.Second
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			e.Next = time.Now().Add(wait)
			s.mu.Lock()
			s.q = append(s.q, e)
			s.mu.Unlock()
			log.Printf("dashboard settle %s: %v", e.Key, err)
		}
	}
}

func (s *settler) recoverUnprocessed() {
	if s.db == nil {
		return
	}
	var facts []RequestFact
	_ = s.db.Where("completed_at IS NULL AND interrupted = ?", false).Find(&facts).Error
	for _, f := range facts {
		if time.Since(f.StartedAt) > 6*time.Minute {
			s.Enqueue(kindReqEnd, "req:"+f.UUID+":end", RequestEnd{
				UUID:        f.UUID,
				Interrupted: true,
				CompletedAt: time.Now().UTC(),
				Success:     false,
			}, f.StartedAt)
		}
	}
}

type RequestStart struct {
	UUID             string
	ConsumerKeyID    uint
	RouteGroupID     *uint
	RouteGroupName   string
	SaleMultiplier   *float64
	CatalogVersionID uint
	Protocol         string
	Path             string
	Stream           bool
	Source           string
	StartedAt        time.Time
	RequestLogID     uint
}

type AttemptEnd struct {
	AttemptFact
}

type RequestEnd struct {
	UUID              string
	Model             string
	Protocol          string
	Success           bool
	Interrupted       bool
	CompletedAt       time.Time
	HTTPAttempts      int
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheWriteTokens  int64
	UsageKnown        bool
	InputPrice        *float64
	OutputPrice       *float64
	CacheReadCoeff    float64
	CacheWriteCoeff   float64
	TTFTMs            int
	TTFTStatus        string
	FinalProviderID   *uint
	FinalProviderName string
}

func (s *settler) process(e queuedEvent) error {
	if s.db == nil {
		return nil
	}
	if time.Since(e.At) > ReplayRejectAge {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		led := EventLedger{EventKey: e.Key, Kind: e.Kind, ProcessedAt: time.Now().UTC()}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&led)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		switch e.Kind {
		case kindReqStart:
			var p RequestStart
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RequestFact{
				UUID:             p.UUID,
				ConsumerKeyID:    p.ConsumerKeyID,
				RouteGroupID:     p.RouteGroupID,
				RouteGroupName:   p.RouteGroupName,
				SaleMultiplier:   p.SaleMultiplier,
				CatalogVersionID: p.CatalogVersionID,
				Protocol:         p.Protocol,
				Path:             p.Path,
				Endpoint:         p.Path,
				Stream:           p.Stream,
				Source:           p.Source,
				StartedAt:        p.StartedAt,
				RequestLogID:     p.RequestLogID,
				CreatedAt:        time.Now().UTC(),
			}).Error
		case kindAttEnd:
			var p AttemptFact
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&p).Error; err != nil {
				return err
			}
			if p.Source != domain.SourceBusiness {
				return nil
			}
			return applyAttemptAgg(tx, p)
		case kindReqEnd:
			var p RequestEnd
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			var fact RequestFact
			if err := tx.Where("uuid = ?", p.UUID).First(&fact).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				return err
			}
			if fact.CompletedAt != nil {
				return nil
			}
			var attempts []AttemptFact
			if err := tx.Where("request_uuid = ?", p.UUID).Find(&attempts).Error; err != nil {
				return err
			}
			sent := 0
			for _, a := range attempts {
				if a.HTTPSent {
					sent++
				}
			}
			if !p.Interrupted && p.HTTPAttempts > sent {
				return errors.New("waiting for attempts")
			}
			updateRequestFact(&fact, p, attempts)
			if err := tx.Save(&fact).Error; err != nil {
				return err
			}
			if fact.Source != domain.SourceBusiness {
				return nil
			}
			return applyRequestAgg(tx, fact, attempts)
		case kindConcSample:
			var p minuteSample
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			return applyConcSample(tx, p)
		default:
			return nil
		}
	})
}

func updateRequestFact(f *RequestFact, p RequestEnd, attempts []AttemptFact) {
	f.Model = p.Model
	f.Protocol = p.Protocol
	f.Success = p.Success
	f.Interrupted = p.Interrupted
	f.CompletedAt = &p.CompletedAt
	f.HTTPAttempts = p.HTTPAttempts
	f.InputTokens = p.InputTokens
	f.OutputTokens = p.OutputTokens
	f.CacheReadTokens = p.CacheReadTokens
	f.CacheWriteTokens = p.CacheWriteTokens
	f.UsageKnown = p.UsageKnown
	f.InputPrice = p.InputPrice
	f.OutputPrice = p.OutputPrice
	f.CacheReadCoeff = p.CacheReadCoeff
	f.CacheWriteCoeff = p.CacheWriteCoeff
	f.TTFTMs = p.TTFTMs
	f.TTFTStatus = p.TTFTStatus
	f.FinalProviderID = p.FinalProviderID
	f.FinalProviderName = p.FinalProviderName
	if f.CacheReadCoeff == 0 {
		f.CacheReadCoeff = 0.1
	}
	if f.CacheWriteCoeff == 0 {
		f.CacheWriteCoeff = 1.25
	}
	priceOK := f.InputPrice != nil && f.OutputPrice != nil
	if priceOK {
		in, out := *f.InputPrice, *f.OutputPrice
		f.BaseCostUSD = BaseCostUSD(ModelPrice{Input: in, Output: out, CacheReadCoeff: f.CacheReadCoeff, CacheWriteCoeff: f.CacheWriteCoeff, OK: true}, f.Protocol, f.InputTokens, f.CacheReadTokens, f.CacheWriteTokens, f.OutputTokens, f.UsageKnown)
	}
	reasons := []string{}
	if f.RouteGroupID == nil {
		reasons = append(reasons, ReasonUnbound)
	}
	if f.SaleMultiplier == nil {
		reasons = append(reasons, ReasonMissingSale)
	}
	if !priceOK {
		reasons = append(reasons, ReasonMissingPrice)
	}
	if !f.UsageKnown && !f.Interrupted {
		reasons = append(reasons, ReasonMissingUsage)
	}
	if f.Interrupted {
		reasons = append(reasons, ReasonInterrupted)
	}
	if f.Success && f.SaleMultiplier != nil && f.BaseCostUSD != nil {
		rev := round8(*f.BaseCostUSD * *f.SaleMultiplier)
		f.RevenueUSD = &rev
	} else if !f.Success && !f.Interrupted {
		zero := 0.0
		f.RevenueUSD = &zero
	}
	attemptCostKnown := true
	for _, a := range attempts {
		if a.HTTPSent && a.EstimatedCostUSD == nil {
			attemptCostKnown = false
		}
	}
	if !attemptCostKnown && f.UsageKnown {
		reasons = append(reasons, ReasonMissingUsage)
	}
	f.Covered = f.RouteGroupID != nil && f.SaleMultiplier != nil && priceOK && (f.UsageKnown || (!f.Success && !f.Interrupted)) && attemptCostKnown && !f.Interrupted
	if f.Covered && f.RevenueUSD == nil && !f.Success {
		zero := 0.0
		f.RevenueUSD = &zero
	}
	b, _ := json.Marshal(reasons)
	f.Reasons = string(b)
}

func applyRequestAgg(tx *gorm.DB, f RequestFact, attempts []AttemptFact) error {
	completed := f.CompletedAt
	if completed == nil {
		return nil
	}
	startBucket := f.StartedAt.UTC().Truncate(time.Minute)
	endBucket := completed.UTC().Truncate(time.Minute)
	var cost Amount
	providerCosts := map[uint]Amount{}
	for _, a := range attempts {
		if a.HTTPSent {
			providerCosts[a.ProviderID] += 0
		}
		if a.EstimatedCostUSD != nil {
			cost = cost.Add(AmountFromFloat(*a.EstimatedCostUSD))
			if a.HTTPSent {
				providerCosts[a.ProviderID] = providerCosts[a.ProviderID].Add(AmountFromFloat(*a.EstimatedCostUSD))
			}
		}
	}
	type contribution struct {
		dim     string
		id      uint
		cost    Amount
		revenue *float64
		success bool
	}
	dims := []contribution{{DimGlobal, 0, cost, f.RevenueUSD, f.Success}}
	if f.RouteGroupID != nil {
		dims = append(dims, contribution{DimGroup, *f.RouteGroupID, cost, f.RevenueUSD, f.Success})
	}
	for id, providerCost := range providerCosts {
		zero := 0.0
		revenue := &zero
		success := f.Success && f.FinalProviderID != nil && *f.FinalProviderID == id
		if success {
			revenue = f.RevenueUSD
		}
		dims = append(dims, contribution{DimProvider, id, providerCost, revenue, success})
	}
	for _, d := range dims {
		row, err := getMinute(tx, startBucket, FamilyRequest, d.dim, d.id)
		if err != nil {
			return err
		}
		row.RequestsStarted++
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if err := addDayFromMinuteDelta(tx, shanghaiDay(f.StartedAt), FamilyRequest, d.dim, d.id, MinuteAgg{RequestsStarted: 1}); err != nil {
			return err
		}

		end, err := getMinute(tx, endBucket, FamilyRequest, d.dim, d.id)
		if err != nil {
			return err
		}
		delta := MinuteAgg{RequestsCompleted: 1}
		end.RequestsCompleted++
		if d.success {
			end.RequestsSuccess++
			delta.RequestsSuccess = 1
		}
		if f.HTTPAttempts >= 2 {
			end.RequestsRetried++
			delta.RequestsRetried = 1
		}
		if f.TTFTStatus == "measured" && d.success {
			h := parseHist(end.TTFTHistJSON)
			h = addHist(h, observeTTFT(f.TTFTMs))
			end.TTFTHistJSON = histJSON(h)
			end.TTFTSamples++
			delta.TTFTHistJSON = histJSON(observeTTFT(f.TTFTMs))
			delta.TTFTSamples = 1
		}
		if d.revenue != nil {
			end.KnownRevenueUSD = round8(end.KnownRevenueUSD + *d.revenue)
			delta.KnownRevenueUSD = *d.revenue
		}
		if d.cost != 0 {
			end.KnownCostUSD = round8(end.KnownCostUSD + d.cost.Float())
			delta.KnownCostUSD = d.cost.Float()
		}
		if f.Covered {
			end.CoveredCount++
			delta.CoveredCount = 1
			if d.revenue != nil {
				end.CoveredRevenueUSD = round8(end.CoveredRevenueUSD + *d.revenue)
				delta.CoveredRevenueUSD = *d.revenue
			}
			end.CoveredCostUSD = round8(end.CoveredCostUSD + d.cost.Float())
			delta.CoveredCostUSD = d.cost.Float()
		}
		incReason := func(dst *int64, n int64) {
			*dst += n
		}
		for _, r := range parseReasons(f.Reasons) {
			switch r {
			case ReasonUnbound:
				incReason(&end.UnboundCount, 1)
				delta.UnboundCount = 1
			case ReasonMissingSale:
				incReason(&end.MissingSaleCount, 1)
				delta.MissingSaleCount = 1
			case ReasonMissingPrice:
				incReason(&end.MissingPriceCount, 1)
				delta.MissingPriceCount = 1
			case ReasonMissingUsage:
				incReason(&end.MissingUsageCount, 1)
				delta.MissingUsageCount = 1
			case ReasonInterrupted:
				incReason(&end.InterruptedCount, 1)
				delta.InterruptedCount = 1
			}
		}
		if err := tx.Save(&end).Error; err != nil {
			return err
		}
		if err := addDayFromMinuteDelta(tx, shanghaiDay(*completed), FamilyRequest, d.dim, d.id, delta); err != nil {
			return err
		}
	}
	return nil
}

func applyAttemptAgg(tx *gorm.DB, a AttemptFact) error {
	bucket := a.CompletedAt.UTC().Truncate(time.Minute)
	dims := []struct {
		dim string
		id  uint
	}{{DimGlobal, 0}, {DimProvider, a.ProviderID}}
	quality := a.Result == "success" || a.Result == "upstream_failure"
	for _, d := range dims {
		row, err := getMinute(tx, bucket, FamilyAttempt, d.dim, d.id)
		if err != nil {
			return err
		}
		delta := MinuteAgg{}
		if quality {
			if a.Result == "success" {
				row.ProviderSuccess++
				delta.ProviderSuccess = 1
			} else {
				row.ProviderFailure++
				delta.ProviderFailure = 1
			}
		}
		if a.TTFTStatus == "measured" && a.Result == "success" {
			h := parseHist(row.TTFTHistJSON)
			obs := observeTTFT(a.TTFTMs)
			h = addHist(h, obs)
			row.TTFTHistJSON = histJSON(h)
			row.TTFTSamples++
			delta.TTFTHistJSON = histJSON(obs)
			delta.TTFTSamples = 1
		}
		if a.EstimatedCostUSD != nil {
			row.KnownCostUSD = round8(row.KnownCostUSD + *a.EstimatedCostUSD)
			delta.KnownCostUSD = *a.EstimatedCostUSD
		}
		if a.ConsumptionUSD != nil {
			row.ConsumptionUSD = round8(row.ConsumptionUSD + *a.ConsumptionUSD)
			delta.ConsumptionUSD = *a.ConsumptionUSD
		} else if a.HTTPSent {
			row.UnknownConsumption++
			delta.UnknownConsumption = 1
		}
		if a.ReportedCostUSD != nil {
			row.ReportedConsumptionUSD = round8(row.ReportedConsumptionUSD + *a.ReportedCostUSD)
			delta.ReportedConsumptionUSD = *a.ReportedCostUSD
		}
		if a.EstimatedCostUSD != nil && a.ReportedCostUSD == nil {
			row.EstimatedConsumptionUSD = round8(row.EstimatedConsumptionUSD + *a.EstimatedCostUSD)
			delta.EstimatedConsumptionUSD = *a.EstimatedCostUSD
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if err := addDayFromMinuteDelta(tx, shanghaiDay(a.CompletedAt), FamilyAttempt, d.dim, d.id, delta); err != nil {
			return err
		}
	}
	return nil
}

func applyConcSample(tx *gorm.DB, s minuteSample) error {
	row, err := getMinute(tx, s.Bucket, s.Family, DimGlobal, 0)
	if err != nil {
		return err
	}
	row.InflightIntegralSec += s.Integral
	if s.Peak > row.InflightPeak {
		row.InflightPeak = s.Peak
	}
	if err := tx.Save(&row).Error; err != nil {
		return err
	}
	return addDayFromMinuteDelta(tx, shanghaiDay(s.Bucket), s.Family, DimGlobal, 0, MinuteAgg{
		InflightIntegralSec: s.Integral,
		InflightPeak:        s.Peak,
	})
}

func getMinute(tx *gorm.DB, bucket time.Time, family, dim string, id uint) (MinuteAgg, error) {
	var row MinuteAgg
	err := tx.Where("bucket = ? AND family = ? AND dim = ? AND dim_id = ?", bucket, family, dim, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = MinuteAgg{Bucket: bucket, Family: family, Dim: dim, DimID: id, TTFTHistJSON: histJSON(emptyHist())}
		if err := tx.Create(&row).Error; err != nil {
			return row, err
		}
		return row, nil
	}
	return row, err
}

func getDay(tx *gorm.DB, bucket time.Time, family, dim string, id uint) (DayAgg, error) {
	var row DayAgg
	err := tx.Where("bucket = ? AND family = ? AND dim = ? AND dim_id = ?", bucket, family, dim, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = DayAgg{Bucket: bucket, Family: family, Dim: dim, DimID: id, TTFTHistJSON: histJSON(emptyHist())}
		if err := tx.Create(&row).Error; err != nil {
			return row, err
		}
		return row, nil
	}
	return row, err
}

func addDayFromMinuteDelta(tx *gorm.DB, day time.Time, family, dim string, id uint, d MinuteAgg) error {
	row, err := getDay(tx, day, family, dim, id)
	if err != nil {
		return err
	}
	row.RequestsStarted += d.RequestsStarted
	row.RequestsCompleted += d.RequestsCompleted
	row.RequestsSuccess += d.RequestsSuccess
	row.RequestsRetried += d.RequestsRetried
	row.ProviderSuccess += d.ProviderSuccess
	row.ProviderFailure += d.ProviderFailure
	row.InflightIntegralSec += d.InflightIntegralSec
	if d.InflightPeak > row.InflightPeak {
		row.InflightPeak = d.InflightPeak
	}
	row.TTFTHistJSON = histJSON(addHist(parseHist(row.TTFTHistJSON), parseHist(d.TTFTHistJSON)))
	row.TTFTSamples += d.TTFTSamples
	row.KnownRevenueUSD = round8(row.KnownRevenueUSD + d.KnownRevenueUSD)
	row.KnownCostUSD = round8(row.KnownCostUSD + d.KnownCostUSD)
	row.ConsumptionUSD = round8(row.ConsumptionUSD + d.ConsumptionUSD)
	row.ReportedConsumptionUSD = round8(row.ReportedConsumptionUSD + d.ReportedConsumptionUSD)
	row.EstimatedConsumptionUSD = round8(row.EstimatedConsumptionUSD + d.EstimatedConsumptionUSD)
	row.UnknownConsumption += d.UnknownConsumption
	row.CoveredRevenueUSD = round8(row.CoveredRevenueUSD + d.CoveredRevenueUSD)
	row.CoveredCostUSD = round8(row.CoveredCostUSD + d.CoveredCostUSD)
	row.CoveredCount += d.CoveredCount
	row.UnboundCount += d.UnboundCount
	row.MissingSaleCount += d.MissingSaleCount
	row.MissingPriceCount += d.MissingPriceCount
	row.MissingUsageCount += d.MissingUsageCount
	row.InterruptedCount += d.InterruptedCount
	return tx.Save(&row).Error
}

func parseReasons(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil {
		return strings.Split(s, ",")
	}
	return out
}

func shanghaiLoc() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

func shanghaiDay(t time.Time) time.Time {
	loc := shanghaiLoc()
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
