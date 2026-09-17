package routinghealth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"simple-up-manage/internal/domain"
)

type Dimension struct {
	KeyID                 uint
	Protocol, Model, Path string
	Stream                bool
}

func (d Dimension) Scope() string {
	b, _ := json.Marshal(d)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func KeyScope(id uint) string { return fmt.Sprintf("key:%d", id) }

type Outcome struct {
	RequestID                                         string
	StartedAt                                         time.Time
	Success, Neutral, AuthFailure, Limited, Immediate bool
	RetryAfter                                        time.Duration
	Reason                                            string
}

type Store struct {
	DB       *gorm.DB
	Settings domain.SchedulerSettings
}

func (s Store) Snapshot(ctx context.Context, dims []Dimension) (map[string]domain.RoutingCircuit, error) {
	ids := make([]string, 0, len(dims)*2)
	for _, d := range dims {
		ids = append(ids, d.Scope(), KeyScope(d.KeyID))
	}
	out := map[string]domain.RoutingCircuit{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []domain.RoutingCircuit
	err := s.DB.WithContext(ctx).Where("scope IN ? AND open = ?", ids, true).Find(&rows).Error
	for _, r := range rows {
		out[r.Scope] = r
	}
	return out, err
}

// UPDATE before reading serializes writers on both PostgreSQL and SQLite.
func lock(tx *gorm.DB, d Dimension, scope string) (domain.RoutingCircuit, error) {
	r := domain.RoutingCircuit{Scope: scope, PlatformKeyID: d.KeyID, Protocol: d.Protocol, Model: d.Model, Path: d.Path, Stream: d.Stream}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; err != nil {
		return r, err
	}
	if err := tx.Model(&domain.RoutingCircuit{}).Where("scope = ?", scope).UpdateColumn("scope", scope).Error; err != nil {
		return r, err
	}
	err := tx.Where("scope = ?", scope).First(&r).Error
	return r, err
}

var ErrUnavailable = errors.New("routing circuit unavailable")

// Admit reserves all applicable gates in a single transaction. The lease exceeds
// the gateway's five-minute overall deadline, including retries.
func (s Store) Admit(ctx context.Context, d Dimension, recovery bool) (string, error) {
	token := uuid.NewString()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for _, scope := range []string{KeyScope(d.KeyID), d.Scope()} {
			r, err := lock(tx, d, scope)
			if err != nil {
				return err
			}
			if !r.Open {
				continue
			}
			if !recovery || r.Until.After(now) || r.LeaseUntil.After(now) || (r.CheckUntil != nil && r.CheckUntil.After(now)) ||
				(!r.CheckOK && r.NextCheckAt != nil && r.NextCheckAt.After(now)) {
				return ErrUnavailable
			}
			if scope == KeyScope(d.KeyID) {
				var busy int64
				if err := tx.Model(&domain.RoutingCircuit{}).Where("platform_key_id = ? AND scope <> ? AND (check_until > ? OR lease_until > ?)", d.KeyID, scope, now, now).Count(&busy).Error; err != nil {
					return err
				}
				if busy > 0 {
					return ErrUnavailable
				}
			}
			r.Lease = token
			r.LeaseUntil = now.Add(330 * time.Second)
			r.RecoveryAt = &now
			if err := tx.Save(&r).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return token, err
}

func open(r *domain.RoutingCircuit, now time.Time, reason string, retry time.Duration, cfg domain.SchedulerSettings) {
	seconds := cfg.CircuitCooldownSec
	if r.Open && r.BackoffSec > 0 {
		seconds = min(max(r.BackoffSec, cfg.CircuitCooldownSec)*2, cfg.CircuitMaxCooldownSec)
		if seconds > cfg.CircuitMaxCooldownSec/2 {
			seconds = cfg.CircuitMaxCooldownSec
		}
	}
	if retry > 0 {
		seconds = max(1, int(retry.Seconds()+0.999))
	}
	r.Open = true
	r.BackoffSec = min(seconds, cfg.CircuitMaxCooldownSec)
	r.OpenedAt = now
	r.Until = now.Add(time.Duration(seconds) * time.Second)
	r.Reason = reason
	r.Lease = ""
	r.LeaseUntil = time.Time{}
	r.CheckOK = false
	r.CheckLease = ""
	r.CheckUntil = nil
	r.NextCheckAt = nil
	r.CheckBackoff = 0
}

func (s Store) Observe(ctx context.Context, d Dimension, token string, o Outcome) error {
	cfg := s.Settings
	cfg.Normalize()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		key, err := lock(tx, d, KeyScope(d.KeyID))
		if err != nil {
			return err
		}
		r, err := lock(tx, d, d.Scope())
		if err != nil {
			return err
		}
		if o.RequestID != "" {
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&domain.RoutingObservation{Scope: r.Scope, RequestID: o.RequestID})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil
			}
		}
		now := time.Now()
		for _, row := range []*domain.RoutingCircuit{&key, &r} {
			owned := row.Lease != "" && row.Lease == token
			if !owned && o.StartedAt.Before(row.OpenedAt) {
				continue
			}
			if row.Open && !owned && row.Lease != "" {
				continue
			}
			if o.Neutral {
				if owned {
					row.Lease = ""
					row.LeaseUntil = time.Time{}
					row.CheckLease = ""
					row.CheckUntil = nil
				}
			} else if o.Success {
				if !row.Open || owned {
					row.Open = false
					row.OpenedAt = now
					row.Failures = 0
					row.FailureTimes = nil
					row.BackoffSec = 0
					row.Reason = ""
					row.Lease = ""
					row.LeaseUntil = time.Time{}
					row.CheckLease, row.CheckUntil = "", nil
				}
			} else if row.Scope == key.Scope {
				if o.AuthFailure || owned {
					row.Protocol, row.Model, row.Path, row.Stream = d.Protocol, d.Model, d.Path, d.Stream
					open(row, now, o.Reason, o.RetryAfter, cfg)
				}
			} else if owned || !o.AuthFailure {
				kept := row.FailureTimes[:0]
				for _, at := range row.FailureTimes {
					if at > now.Add(-time.Duration(cfg.CircuitWindowSec)*time.Second).UnixMilli() {
						kept = append(kept, at)
					}
				}
				row.FailureTimes = append(kept, now.UnixMilli())
				if len(row.FailureTimes) > cfg.CircuitFailureThreshold {
					row.FailureTimes = row.FailureTimes[len(row.FailureTimes)-cfg.CircuitFailureThreshold:]
				}
				row.Failures = len(row.FailureTimes)
				if owned || o.Limited || o.Immediate || row.Failures >= cfg.CircuitFailureThreshold {
					open(row, now, o.Reason, o.RetryAfter, cfg)
				}
			}
			if err := tx.Save(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Slot shares one exploration allowance across processes and modes.
// Read-only previews never consume it. Call once per incoming request.
func (s Store) Slot(ctx context.Context, scope string, interval int, mutate bool) (bool, error) {
	if interval <= 0 {
		return false, nil
	}
	var slot bool
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := domain.RoutingBudget{Scope: scope}
		if mutate {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; err != nil {
				return err
			}
			if err := tx.Model(&r).Updates(map[string]any{"counter": gorm.Expr("(counter + 1) % ?", interval), "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("scope = ?", scope).First(&r).Error; err != nil {
			if !mutate && errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		count := r.Counter
		if !mutate {
			count++
		}
		slot = count%interval == 0
		return nil
	})
	return slot, err
}
