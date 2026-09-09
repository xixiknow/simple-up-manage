package domain

import (
	"testing"
	"time"
)

func TestFormatRate(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "1"},
		{1.0, "1"},
		{0.08, "0.08"},
		{0.0800, "0.08"},
		{1.2500, "1.25"},
		{0, "0"},
	}
	for _, c := range cases {
		if got := FormatRate(c.in); got != c.want {
			t.Errorf("FormatRate(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComposeKeyName(t *testing.T) {
	got := ComposeKeyName(" plus ", " 稳定 ", 0.08)
	if got != "plus-稳定-0.08" {
		t.Fatalf("ComposeKeyName = %q", got)
	}
}

func TestInferNameTag(t *testing.T) {
	cases := []struct {
		name, provider, want string
	}{
		{"plus-稳定", "plus", "稳定"},
		{"plus-稳定-0.08", "plus", "稳定"},
		{"plus-vip-1", "plus", "vip"},
		{"稳定", "", "稳定"},
	}
	for _, c := range cases {
		if got := InferNameTag(c.name, c.provider); got != c.want {
			t.Errorf("InferNameTag(%q, %q) = %q, want %q", c.name, c.provider, got, c.want)
		}
	}
	k := PlatformKey{Name: "plus-稳定", RateMultiplier: 1}
	if got := k.EffectiveNameTag("plus"); got != "稳定" {
		t.Errorf("EffectiveNameTag = %q", got)
	}
	k.NameTag = "vip"
	if got := k.EffectiveNameTag("plus"); got != "vip" {
		t.Errorf("stored NameTag = %q", got)
	}
}

func TestProbeEvery(t *testing.T) {
	fallback := time.Minute
	if got := (*PlatformKey)(nil).ProbeEvery(fallback); got != fallback {
		t.Fatalf("nil key: %s", got)
	}
	k := &PlatformKey{}
	if got := k.ProbeEvery(fallback); got != fallback {
		t.Fatalf("zero: %s", got)
	}
	k.ProbeIntervalSec = 300
	if got := k.ProbeEvery(fallback); got != 5*time.Minute {
		t.Fatalf("override: %s", got)
	}
}
