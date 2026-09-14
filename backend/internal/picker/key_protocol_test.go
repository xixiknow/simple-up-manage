package picker

import (
	"context"
	"simple-up-manage/internal/domain"
	"testing"
)

func TestKeyProtocolsExcludeStickyAndFailover(t *testing.T) {
	p, up, keys := testBandPicker(t)
	ctx := context.Background()
	if err := p.db.Model(&up).Update("protocols", "openai,anthropic").Error; err != nil {
		t.Fatal(err)
	}
	if err := p.db.Model(&keys[0]).Update("protocols", "anthropic").Error; err != nil {
		t.Fatal(err)
	}
	p.Reload()
	p.SetSticky(ctx, domain.ProtocolOpenAI, "fixture", keys[0].ID)
	k, _, err := p.Pick(ctx, Request{Protocol: domain.ProtocolOpenAI, Session: "fixture"})
	if err != nil || k.ID != keys[1].ID {
		t.Fatalf("sticky bypass: %v %v", k, err)
	}
	_, _, err = p.Pick(ctx, Request{Protocol: domain.ProtocolOpenAI, Exclude: []uint{keys[1].ID}})
	if err == nil {
		t.Fatal("failover selected incompatible key")
	}
	k, _, err = p.Pick(ctx, Request{Protocol: domain.ProtocolAnthropic, Exclude: []uint{keys[1].ID}})
	if err != nil || k.ID != keys[0].ID {
		t.Fatalf("anthropic rejected: %v %v", k, err)
	}
}
