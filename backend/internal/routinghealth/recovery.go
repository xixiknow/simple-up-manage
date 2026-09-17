package routinghealth

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"simple-up-manage/internal/domain"
)

const RecoveryInterval = time.Minute
const CheckLeaseDuration = 45 * time.Second

// RecoverySlot is independent of exploration and is consumed only when a
// request can validate a candidate. Previews never create or update budgets.
func (s Store) RecoverySlot(ctx context.Context, scope string, mutate bool) (bool, error) {
	var allowed bool
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := domain.RoutingBudget{Scope: "recovery:" + scope}
		if mutate {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; err != nil {
				return err
			}
			if err := tx.Model(&r).UpdateColumn("scope", r.Scope).Error; err != nil {
				return err
			}
		}
		if err := tx.First(&r, "scope = ?", r.Scope).Error; err != nil {
			if !mutate && err == gorm.ErrRecordNotFound {
				allowed = true
				return nil
			}
			return err
		}
		now := time.Now()
		allowed = r.RecoveryAfter == nil || !r.RecoveryAfter.After(now)
		if allowed && mutate {
			next := now.Add(RecoveryInterval)
			return tx.Model(&r).Updates(map[string]any{"recovery_after": next, "updated_at": now}).Error
		}
		return nil
	})
	return allowed, err
}

// ClaimCheck serializes a global two-worker allowance in SQL, as well as the
// circuit lease. Business validation and synthetic checks cannot overlap.
func (s Store) ClaimCheck(ctx context.Context, scope string, d Dimension) (string, error) {
	token := uuid.NewString()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		budget := domain.RoutingBudget{Scope: "recovery-check-workers"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&budget).Error; err != nil {
			return err
		}
		if err := tx.Model(&budget).Updates(map[string]any{"scope": budget.Scope, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		now := time.Now()
		var active int64
		if err := tx.Model(&domain.RoutingCircuit{}).Where("check_until > ?", now).Count(&active).Error; err != nil {
			return err
		}
		if active >= 2 {
			return ErrUnavailable
		}
		// Use the same key-first lock order as business admission. A key-wide
		// check must also exclude every model's business validation lease.
		keyGate, err := lock(tx, d, KeyScope(d.KeyID))
		if err != nil {
			return err
		}
		if scope != KeyScope(d.KeyID) && keyGate.Open && (keyGate.Until.After(now) || (!keyGate.CheckOK && keyGate.NextCheckAt != nil && keyGate.NextCheckAt.After(now))) {
			return ErrUnavailable
		}
		var conflicting int64
		q := tx.Model(&domain.RoutingCircuit{}).Where("platform_key_id = ? AND (lease_until > ? OR check_until > ?)", d.KeyID, now, now)
		if scope != KeyScope(d.KeyID) {
			q = q.Where("scope IN ?", []string{scope, KeyScope(d.KeyID)})
		}
		if err := q.Count(&conflicting).Error; err != nil {
			return err
		}
		if conflicting > 0 {
			return ErrUnavailable
		}
		var r domain.RoutingCircuit
		if err := tx.Model(&r).Where("scope = ?", scope).UpdateColumn("scope", scope).Error; err != nil {
			return err
		}
		if err := tx.First(&r, "scope = ?", scope).Error; err != nil {
			return err
		}
		if !r.Open || r.Until.After(now) || r.LeaseUntil.After(now) ||
			(r.CheckUntil != nil && r.CheckUntil.After(now)) || (r.NextCheckAt != nil && r.NextCheckAt.After(now)) {
			return ErrUnavailable
		}
		until := now.Add(CheckLeaseDuration)
		return tx.Model(&r).Updates(map[string]any{
			"check_lease": token, "check_until": until, "check_ok": false,
			"protocol": d.Protocol, "model": d.Model, "path": d.Path, "stream": d.Stream,
		}).Error
	})
	return token, err
}

// FinishCheck records diagnostic readiness only; it never clears business
// failures, changes rankings, or closes a circuit. Stale owners are ignored.
func (s Store) FinishCheck(ctx context.Context, scope, token string, success, neutral bool, message string, retryAfter time.Duration) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var r domain.RoutingCircuit
		if err := tx.Model(&r).Where("scope = ?", scope).UpdateColumn("scope", scope).Error; err != nil {
			return err
		}
		if err := tx.First(&r, "scope = ?", scope).Error; err != nil {
			return err
		}
		if !r.Open || r.CheckLease != token || token == "" {
			return nil
		}
		now := time.Now()
		if r.CheckUntil == nil || !r.CheckUntil.After(now) {
			return nil
		}
		r.CheckLease, r.CheckUntil = "", nil
		r.CheckAt, r.CheckOK, r.CheckError = &now, success, message
		delay := time.Minute
		if success {
			r.CheckBackoff = 0
			delay = 5 * time.Minute
		} else if !neutral {
			if r.CheckBackoff == 0 {
				r.CheckBackoff = 30
			} else {
				r.CheckBackoff = min(r.CheckBackoff*2, 300)
				if r.CheckBackoff > 120 {
					r.CheckBackoff = 300
				}
			}
			delay = time.Duration(r.CheckBackoff) * time.Second
		}
		delay = max(delay, retryAfter)
		next := now.Add(delay)
		r.NextCheckAt = &next
		return tx.Save(&r).Error
	})
}

// ResolveDimension upgrades historical hash-only circuits without guessing the
// model. Missing historical attempts leave the circuit available for real
// validation but cannot produce a synthetic request.
func (s Store) ResolveDimension(ctx context.Context, r domain.RoutingCircuit) (Dimension, bool, error) {
	d := Dimension{KeyID: r.PlatformKeyID, Protocol: r.Protocol, Model: r.Model, Path: r.Path, Stream: r.Stream}
	if d.Path != "" {
		return d, true, nil
	}
	var rows []domain.RequestAttempt
	err := s.DB.WithContext(ctx).Model(&domain.RequestAttempt{}).
		Select("protocol, model, path, stream").Where("platform_key_id = ?", r.PlatformKeyID).
		Group("protocol, model, path, stream").Order("MAX(completed_at) DESC").Find(&rows).Error
	if err != nil {
		return d, false, err
	}
	for _, a := range rows {
		d = Dimension{KeyID: r.PlatformKeyID, Protocol: a.Protocol, Model: a.Model, Path: a.Path, Stream: a.Stream}
		if r.Scope == KeyScope(r.PlatformKeyID) || d.Scope() == r.Scope {
			return d, true, nil
		}
	}
	return d, false, nil
}
