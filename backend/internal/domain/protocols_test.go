package domain

import "testing"

func TestKeyEffectiveProtocols(t *testing.T) {
	up := &Upstream{Protocols: "openai,anthropic"}
	k := &PlatformKey{Upstream: up}
	if !k.SupportsProtocol(ProtocolOpenAI) || !k.SupportsProtocol(ProtocolAnthropic) {
		t.Fatal("inheritance lost")
	}
	k.Protocols = "anthropic"
	if k.SupportsProtocol(ProtocolOpenAI) || !k.SupportsProtocol(ProtocolAnthropic) {
		t.Fatal("restriction ignored")
	}
	up.Protocols = "openai"
	if len(k.EffectiveProtocols()) != 0 {
		t.Fatal("provider ceiling ignored")
	}
	k.Protocols = ""
	if !k.SupportsProtocol(ProtocolOpenAI) {
		t.Fatal("inheritance not restored")
	}
}
