package picker

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestStableRedisSharedStateAndReadOnlyPreview(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set")
	}
	p, keys, req := stableFixture(t)
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	p.rdb = client
	second := NewBand(p.db, client)
	second.cfg = p.cfg
	ctx := context.Background()
	req.Session = "redis-session"
	redisKey := "sum:v2:stable:" + routeScope(req)
	if err := client.Del(ctx, redisKey).Err(); err != nil {
		t.Fatal(err)
	}
	defer client.Del(ctx, redisKey)
	p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "resp_shared")
	if second.ResolvePrevious(ctx, req, "resp_shared") != req.Session {
		t.Fatal("response binding not shared")
	}
	before, err := client.Get(ctx, redisKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := second.Explain(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := client.Get(ctx, redisKey).Result()
	if before != after {
		t.Fatal("preview mutated Redis state")
	}
	p.cfg.ExplorationRatio = .05
	second.cfg.ExplorationRatio = .05
	sessionReq := req
	sessionReq.Session = ""
	for i := 1; i <= 20; i++ {
		active := p
		if i%2 == 0 {
			active = second
		}
		_, _, d, err := active.PickDecision(ctx, sessionReq)
		if err != nil || d.Exploration != (i == 20) {
			t.Fatalf("shared exploration counter at %d: %+v %v", i, d, err)
		}
	}
	// Redis loss must retain the last locally confirmed binding.
	client.Close()
	if p.ResolvePrevious(ctx, req, "resp_shared") != req.Session {
		t.Fatal("local fallback lost binding")
	}
}
