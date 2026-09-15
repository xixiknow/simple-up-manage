package handler

import (
	"fmt"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
	"testing"
)

func TestExplainDistinguishesMembershipFromModelMatch(t *testing.T) {
	db := logTestDB(t)
	up := domain.Upstream{Name: "provider", BaseURL: "https://example.test", Protocols: "openai", Status: domain.StatusEnabled}
	db.Create(&up)
	key := domain.PlatformKey{Name: "member", UpstreamID: up.ID, EncryptedKey: "test", Status: domain.StatusEnabled}
	db.Create(&key)
	other := domain.PlatformKey{Name: "nonmember", UpstreamID: up.ID, EncryptedKey: "test", Status: domain.StatusEnabled}
	db.Create(&other)
	g := domain.RouteGroup{Name: "0.20-gpt", Protocol: "openai", Models: domain.JSONStrings{"gpt-*"}, Status: domain.StatusEnabled}
	db.Create(&g)
	ck := domain.ConsumerKey{Name: "consumer", Key: "test", Status: domain.StatusEnabled}
	db.Create(&ck)
	db.Create(&domain.RouteGroupKey{RouteGroupID: g.ID, PlatformKeyID: key.ID})
	db.Create(&domain.ConsumerRouteGroup{ConsumerKeyID: ck.ID, RouteGroupID: g.ID})
	h := &Admin{DB: db, Picker: picker.NewBand(db, nil)}
	for _, tc := range []struct{ model, reason string }{{"", "model_required"}, {"claude-test", "route_model_mismatch"}, {"gpt-test", ""}} {
		t.Run(tc.model, func(t *testing.T) {
			data := getAdminPage[struct {
				Candidates []picker.Candidate `json:"candidates"`
			}](t, h.ExplainScheduler, fmt.Sprintf("consumer_key_id=%d&protocol=openai&model=%s", ck.ID, tc.model))
			if len(data.Candidates) != 2 {
				t.Fatal(data)
			}
			if data.Candidates[0].SkipReason != tc.reason || data.Candidates[1].SkipReason != "not_in_route_group" {
				t.Fatalf("wrong reasons: %+v", data.Candidates)
			}
		})
	}
}
