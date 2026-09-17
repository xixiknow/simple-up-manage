package picker

import (
	"context"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func TestFailedCheckBackoffCannotBypassViaOutage(t *testing.T) {
	p, keys, req := stableFixture(t)
	req.AllowKeys = map[uint]struct{}{keys[0].ID: {}}
	now := time.Now()
	next := now.Add(10 * time.Minute)
	gate := domain.RoutingCircuit{Scope: dimension(req, keys[0].ID).Scope(), PlatformKeyID: keys[0].ID, Open: true, Until: now.Add(-time.Minute), CheckAt: &now, NextCheckAt: &next, CheckError: "429", Path: req.Path}
	if err := p.db.Create(&gate).Error; err != nil {
		t.Fatal(err)
	}
	_, _, _, err := p.PickDecision(context.Background(), req)
	if err != ErrNoUpstream {
		t.Fatalf("check retry deadline bypassed: %v", err)
	}
}

func TestRecoveryFairnessAfterNeutralValidation(t *testing.T) {
	p, keys, req := stableFixture(t)
	now := time.Now()
	for i, key := range keys {
		gate := domain.RoutingCircuit{Scope: dimension(req, key.ID).Scope(), PlatformKeyID: key.ID, Open: true, Until: now.Add(-time.Duration(3-i) * time.Minute)}
		if i == 0 {
			gate.RecoveryAt = &now
		}
		if err := p.db.Create(&gate).Error; err != nil {
			t.Fatal(err)
		}
	}
	key, _, d, err := p.PickDecision(context.Background(), req)
	if err != nil || key.ID != keys[1].ID || d.Reason != "recovery_validation" {
		t.Fatalf("oldest untried candidate did not get a turn: %+v %v", d, err)
	}
}
