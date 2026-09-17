package ops

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/routinghealth"
)

// CheckRecoveries runs at most two synthetic requests. SQL leases enforce the
// same bound across workers and restarts; diagnostic results never close gates.
func (s *Service) CheckRecoveries(ctx context.Context) error {
	now := time.Now()
	var gates []domain.RoutingCircuit
	err := s.DB.WithContext(ctx).Model(&domain.RoutingCircuit{}).
		Select("routing_circuits.*").
		Joins("JOIN platform_keys k ON k.id = routing_circuits.platform_key_id").
		Joins("JOIN upstreams u ON u.id = k.upstream_id").
		Where("routing_circuits.open = ? AND routing_circuits.until <= ? AND routing_circuits.lease_until <= ?", true, now, now).
		Where("(check_until IS NULL OR check_until <= ?) AND (next_check_at IS NULL OR next_check_at <= ?)", now, now).
		Where("k.status = ? AND u.status = ? AND k.probe_enabled = ?", domain.StatusEnabled, domain.StatusEnabled, true).
		Where("u.last_balance IS NULL OR u.last_balance > 0").
		Order("COALESCE(next_check_at, routing_circuits.until), routing_circuits.scope").Find(&gates).Error
	if err != nil {
		return err
	}
	health := routinghealth.Store{DB: s.DB}
	var wg sync.WaitGroup
	defer wg.Wait()
	started := 0
	for _, gate := range gates {
		if started == 2 {
			break
		}
		dim, known, err := health.ResolveDimension(ctx, gate)
		if err != nil {
			return err
		}
		if !known || (dim.Path != "/v1/responses" && dim.Path != "/v1/chat/completions" && dim.Path != "/v1/messages") {
			continue
		}
		var key domain.PlatformKey
		if err := s.DB.WithContext(ctx).Preload("Upstream").First(&key, gate.PlatformKeyID).Error; err != nil {
			return err
		}
		if !key.AllowsProbe() || key.Status != domain.StatusEnabled || key.Upstream == nil || key.Upstream.Status != domain.StatusEnabled || !key.SupportsProtocol(dim.Protocol) {
			continue
		}
		token, err := health.ClaimCheck(ctx, gate.Scope, dim)
		if errors.Is(err, routinghealth.ErrUnavailable) {
			continue
		}
		if err != nil {
			return err
		}
		started++
		wg.Add(1)
		go func(gate domain.RoutingCircuit, dim routinghealth.Dimension, key domain.PlatformKey, token string) {
			defer wg.Done()
			checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			out := ProbeOutcome{Protocol: dim.Protocol, Model: dim.Model, Path: dim.Path, Stream: dim.Stream}
			// Refresh operator switches immediately before sending a queued check.
			err := s.DB.WithContext(checkCtx).Preload("Upstream").First(&key, key.ID).Error
			if err == nil && (!key.AllowsProbe() || key.Status != domain.StatusEnabled || key.Upstream == nil || key.Upstream.Status != domain.StatusEnabled || (key.Upstream.LastBalance != nil && *key.Upstream.LastBalance <= 0)) {
				err = errors.New("recovery check disabled")
			}
			secret := ""
			if err == nil {
				secret, err = s.decrypt(&key)
			}
			neutral := err != nil
			if err != nil {
				out.Error = "credential unavailable"
			} else {
				out = s.sendBusinessProbe(checkCtx, &key, secret, out, "Reply OK.")
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
			if err := health.FinishCheck(finishCtx, gate.Scope, token, out.Success, neutral, out.Error, retry); err != nil {
				log.Printf("recovery result key=%d: %v", key.ID, err)
			}
		}(gate, dim, key, token)
	}
	return nil
}
