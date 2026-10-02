package ops

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/routinghealth"
)

type recoveryTask struct {
	gate domain.RoutingCircuit
	dim  routinghealth.Dimension
}

// pendingRecoveries pages through due gates without reserving a lease. The queue
// is bounded; unresolved historical dimensions cannot starve later candidates.
func (s *Service) pendingRecoveries(ctx context.Context, limit int, excluded map[string]bool) ([]recoveryTask, error) {
	now := time.Now()
	health := routinghealth.Store{DB: s.DB}
	tasks := make([]recoveryTask, 0, limit)
	for offset := 0; len(tasks) < limit; offset += 64 {
		var gates []domain.RoutingCircuit
		q := s.DB.WithContext(ctx).Model(&domain.RoutingCircuit{}).
			Select("routing_circuits.*").
			Joins("JOIN platform_keys k ON k.id = routing_circuits.platform_key_id").
			Joins("JOIN upstreams u ON u.id = k.upstream_id").
			Where("routing_circuits.open = ? AND routing_circuits.until <= ? AND routing_circuits.lease_until <= ?", true, now, now).
			Where("(check_until IS NULL OR check_until <= ?) AND (check_ok = ? OR next_check_at IS NULL OR next_check_at <= ?)", now, true, now).
			Where("k.status = ? AND u.status = ? AND (k.probe_enabled IS NULL OR k.probe_enabled = ?)", domain.StatusEnabled, domain.StatusEnabled, true).
			Where("u.last_balance IS NULL OR u.last_balance > 0")
		if err := q.Order("CASE WHEN check_ok THEN routing_circuits.until ELSE COALESCE(next_check_at, routing_circuits.until) END, routing_circuits.scope").Offset(offset).Limit(64).Find(&gates).Error; err != nil {
			return nil, err
		}
		for _, gate := range gates {
			if excluded[gate.Scope] {
				continue
			}
			dim, known, err := health.ResolveDimension(ctx, gate)
			if err != nil {
				return nil, err
			}
			if !known || !routinghealth.SupportsCheck(dim.Path) {
				continue
			}
			tasks = append(tasks, recoveryTask{gate: gate, dim: dim})
			if len(tasks) == limit {
				break
			}
		}
		if len(gates) < 64 {
			break
		}
	}
	return tasks, nil
}

// RunRecoveries continuously fills a bounded pool. SQL leases enforce the limit
// across instances; completion wakes the scheduler instead of waiting for a batch.
func (s *Service) RunRecoveries(ctx context.Context, concurrency int) {
	if concurrency < 1 || concurrency > 64 {
		concurrency = routinghealth.DefaultCheckConcurrency
	}
	health := routinghealth.Store{DB: s.DB, CheckConcurrency: concurrency}
	log.Printf("job recovery started concurrency=%d scan_interval=1s", concurrency)
	defer log.Printf("job recovery stopped")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	done := make(chan string, concurrency)
	active := 0
	queued := make(map[string]bool)
	var pending []recoveryTask
	defer func() {
		for active > 0 {
			<-done
			active--
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		if active < concurrency {
			tasks, err := s.pendingRecoveries(ctx, concurrency*8-len(pending), queued)
			if err != nil && ctx.Err() == nil {
				log.Printf("job recovery scan: %v", err)
			}
			for _, task := range tasks {
				queued[task.gate.Scope] = true
				pending = append(pending, task)
			}
			for active < concurrency && len(pending) > 0 && ctx.Err() == nil {
				task := pending[0]
				token, err := health.ClaimCheck(ctx, task.gate.Scope, task.dim, task.gate.Generation)
				if errors.Is(err, routinghealth.ErrCheckCapacity) {
					break
				}
				pending = pending[1:]
				if err != nil {
					delete(queued, task.gate.Scope)
					if !errors.Is(err, routinghealth.ErrUnavailable) && ctx.Err() == nil {
						log.Printf("job recovery claim: %v", err)
					}
					continue
				}
				active++
				go func(task recoveryTask, token string) {
					s.runRecoveryCheck(ctx, health, task, token)
					done <- task.gate.Scope
				}(task, token)
			}
		}
		select {
		case <-ctx.Done():
			return
		case scope := <-done:
			active--
			delete(queued, scope)
		case <-ticker.C:
		}
	}
}

// CheckRecoveries executes one bounded pass for explicit callers/tests. Production
// uses RunRecoveries so a slow request never blocks replenishment of other slots.
func (s *Service) CheckRecoveries(ctx context.Context) error {
	health := routinghealth.Store{DB: s.DB}
	tasks, err := s.pendingRecoveries(ctx, routinghealth.DefaultCheckConcurrency, nil)
	if err != nil {
		return err
	}
	done := make(chan struct{}, len(tasks))
	active := 0
	defer func() {
		for active > 0 {
			<-done
			active--
		}
	}()
	for _, task := range tasks {
		token, err := health.ClaimCheck(ctx, task.gate.Scope, task.dim, task.gate.Generation)
		if errors.Is(err, routinghealth.ErrCheckCapacity) {
			break
		}
		if errors.Is(err, routinghealth.ErrUnavailable) {
			continue
		}
		if err != nil {
			return err
		}
		active++
		go func(task recoveryTask, token string) {
			s.runRecoveryCheck(ctx, health, task, token)
			done <- struct{}{}
		}(task, token)
	}
	return nil
}

func (s *Service) runRecoveryCheck(ctx context.Context, health routinghealth.Store, task recoveryTask, token string) {
	// The check probes the failing dimension's own model; the timeout must be
	// generous enough for reasoning models whose TTFT p95 approaches 30s.
	timeout := time.Duration(s.probeSettings(ctx).RecoveryCheckTimeoutSec) * time.Second
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dim := task.dim
	out := ProbeOutcome{Protocol: dim.Protocol, Model: dim.Model, Path: dim.Path, Stream: dim.Stream}
	var key domain.PlatformKey
	err := s.DB.WithContext(checkCtx).Preload("Upstream").First(&key, dim.KeyID).Error
	if err == nil && (!key.AllowsProbe() || key.Status != domain.StatusEnabled || key.Upstream == nil || key.Upstream.Status != domain.StatusEnabled || !key.SupportsProtocol(dim.Protocol) || (key.Upstream.LastBalance != nil && *key.Upstream.LastBalance <= 0)) {
		err = errors.New("recovery check disabled")
	}
	secret := ""
	if err == nil {
		secret, err = s.decrypt(&key)
	}
	neutral := err != nil
	if err != nil {
		out.Error = "recovery check unavailable"
	} else {
		probeService := *s
		probeService.Client = s.Client.WithTimeout(timeout)
		out = probeService.sendBusinessProbe(checkCtx, &key, secret, out, "Reply OK.")
	}
	neutral = neutral || ctx.Err() != nil
	retry := time.Duration(0)
	if seconds, err := strconv.Atoi(out.RetryAfter); err == nil && seconds > 0 {
		retry = time.Duration(min(seconds, 86400)) * time.Second
	} else if at, err := http.ParseTime(out.RetryAfter); err == nil {
		retry = min(time.Until(at), 24*time.Hour)
	}
	finishCtx, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	if err := health.FinishCheck(finishCtx, task.gate.Scope, token, out.Success, neutral, out.Error, retry); err != nil {
		log.Printf("recovery result key=%d: %v", dim.KeyID, err)
	} else {
		log.Printf("recovery result key=%d scope=%s success=%t neutral=%t", dim.KeyID, task.gate.Scope, out.Success && !neutral, neutral)
	}
}
