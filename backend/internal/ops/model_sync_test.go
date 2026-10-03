package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"
)

const testEncryptKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func mustEncrypt(t *testing.T, enc *crypto.AESGCM, plain string) string {
	t.Helper()
	out, err := enc.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type modelChangePayload struct {
	RouteGroupID uint     `json:"route_group_id"`
	GroupName    string   `json:"group_name"`
	Added        []string `json:"added"`
	Removed      []string `json:"removed"`
}

func modelNotices(t *testing.T, s *Service) map[uint]modelChangePayload {
	t.Helper()
	var rows []domain.Notice
	if err := s.DB.Where("kind = ?", domain.NoticeKindModelChange).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[uint]modelChangePayload{}
	for _, n := range rows {
		var p modelChangePayload
		if err := json.Unmarshal([]byte(n.Payload), &p); err != nil {
			t.Fatal(err)
		}
		out[p.RouteGroupID] = p
	}
	return out
}

// seedModelSync builds one upstream with two keys and four route groups:
// gAll (no pattern limit), gGlob (premium-*), gDrift (rate cap excludes k1),
// gExact (exact id, never fetched, so never part of a diff).
func seedModelSync(t *testing.T) (*Service, map[string][]string, *bool) {
	t.Helper()
	db := testDB(t)
	enc, err := crypto.New(testEncryptKey)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	lists := map[string][]string{
		"sk-k1": {"alpha", "beta", "gamma", "premium-x"},
		"sk-k2": {"alpha", "delta"},
	}
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		rawKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ids := lists[rawKey]
		out := struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}{}
		for _, id := range ids {
			out.Data = append(out.Data, struct {
				ID string `json:"id"`
			}{ID: id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)

	up := domain.Upstream{
		Name: "prov", BaseURL: srv.URL, Kind: domain.KindOpenAICompat,
		Protocols: domain.ProtocolOpenAI, Status: domain.StatusEnabled,
	}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	makeKey := func(name, rawKey string, rate float64, lastModels []string) domain.PlatformKey {
		k := domain.PlatformKey{
			UpstreamID:     up.ID,
			Name:           name,
			EncryptedKey:   mustEncrypt(t, enc, rawKey),
			Status:         domain.StatusEnabled,
			RateMultiplier: rate,
			LastModels:     domain.JSONStrings(lastModels),
		}
		if err := db.Create(&k).Error; err != nil {
			t.Fatal(err)
		}
		return k
	}
	k1 := makeKey("prov-a-1", "sk-k1", 1, []string{"alpha", "beta"})
	k2 := makeKey("prov-b-0.08", "sk-k2", 0.08, []string{"alpha"})

	rateMax := 0.5
	groups := []domain.RouteGroup{
		{Name: "g-all", Status: domain.StatusEnabled},
		{Name: "g-glob", Status: domain.StatusEnabled, Models: domain.JSONStrings{"premium-*"}},
		{Name: "g-drift", Status: domain.StatusEnabled, RateMax: &rateMax},
		{Name: "g-exact", Status: domain.StatusEnabled, Models: domain.JSONStrings{"always-on"}},
	}
	for i := range groups {
		if err := db.Create(&groups[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	members := []domain.RouteGroupKey{
		{RouteGroupID: groups[0].ID, PlatformKeyID: k1.ID},
		{RouteGroupID: groups[0].ID, PlatformKeyID: k2.ID},
		{RouteGroupID: groups[1].ID, PlatformKeyID: k1.ID},
		{RouteGroupID: groups[2].ID, PlatformKeyID: k1.ID},
		{RouteGroupID: groups[2].ID, PlatformKeyID: k2.ID},
		{RouteGroupID: groups[3].ID, PlatformKeyID: k2.ID},
	}
	for i := range members {
		if err := db.Create(&members[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &Service{DB: db, Enc: enc, Client: upstream.NewClient()}, lists, &fail
}

func TestSyncAllModelsNotifiesGroupDiff(t *testing.T) {
	s, lists, fail := seedModelSync(t)
	ctx := context.Background()

	// Phase 1: upstream grew — k1 adds premium-x (beta/gamma stay), k2 adds delta.
	res, err := s.SyncAllModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK != 2 || res.Failed != 0 {
		t.Fatalf("phase1 ok/failed=%d/%d", res.OK, res.Failed)
	}
	// g-all gains gamma+delta+premium-x, g-glob gains premium-x, g-drift gains delta.
	if res.GroupsNotified != 3 || res.Added != 5 || res.Removed != 0 {
		t.Fatalf("phase1 res=%+v", res)
	}
	notices := modelNotices(t, s)
	if len(notices) != 3 {
		t.Fatalf("phase1 notices=%d", len(notices))
	}
	var gAllID uint
	for id, p := range notices {
		if p.GroupName == "g-all" {
			gAllID = id
			if strings.Join(p.Added, ",") != "delta,gamma,premium-x" || len(p.Removed) != 0 {
				t.Fatalf("g-all payload=%+v", p)
			}
		}
	}
	if gAllID == 0 {
		t.Fatalf("g-all notice missing: %+v", notices)
	}
	if p := notices[gAllID]; !strings.Contains(p.GroupName, "g-all") {
		t.Fatalf("name=%s", p.GroupName)
	}

	// Phase 2: nothing changed — no new notices.
	res, err = s.SyncAllModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.GroupsNotified != 0 || res.Added != 0 || res.Removed != 0 {
		t.Fatalf("phase2 res=%+v", res)
	}
	if got := len(modelNotices(t, s)); got != 3 {
		t.Fatalf("phase2 notices=%d", got)
	}

	// Phase 3: k1 drops beta and gamma.
	lists["sk-k1"] = []string{"alpha", "premium-x"}
	res, err = s.SyncAllModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.GroupsNotified != 1 || res.Added != 0 || res.Removed != 2 {
		t.Fatalf("phase3 res=%+v", res)
	}
	notices = modelNotices(t, s)
	gAll, ok := notices[gAllID]
	if !ok {
		t.Fatalf("phase3 g-all notice missing: %+v", notices)
	}
	if len(gAll.Added) != 0 || strings.Join(gAll.Removed, ",") != "beta,gamma" {
		t.Fatalf("phase3 g-all payload=%+v", gAll)
	}

	// Phase 4: fetch fails — keys keep their old lists, so no phantom removal.
	*fail = true
	lists["sk-k2"] = []string{"alpha", "delta", "epsilon"}
	res, err = s.SyncAllModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK != 0 || res.Failed != 2 {
		t.Fatalf("phase4 ok/failed=%d/%d", res.OK, res.Failed)
	}
	if res.GroupsNotified != 0 {
		t.Fatalf("phase4 should not notify: %+v", res)
	}
	var k1 domain.PlatformKey
	if err := s.DB.Where("name = ?", "prov-a-1").First(&k1).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Join(k1.LastModels, ",") != "alpha,premium-x" {
		t.Fatalf("phase4 k1 last_models=%v", k1.LastModels)
	}
}

func TestSyncAllModelsSkipsDisabledGroupsAndUpstreams(t *testing.T) {
	db := testDB(t)
	enc, err := crypto.New(testEncryptKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	up := domain.Upstream{
		Name: "prov", BaseURL: srv.URL, Kind: domain.KindOpenAICompat,
		Protocols: domain.ProtocolOpenAI, Status: domain.StatusEnabled,
	}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	k := domain.PlatformKey{
		UpstreamID: up.ID, Name: "prov-a-1",
		EncryptedKey: mustEncrypt(t, enc, "sk-1"),
		Status:       domain.StatusEnabled, RateMultiplier: 1,
	}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	disabledGroup := domain.RouteGroup{Name: "g-off", Status: domain.StatusDisabled}
	if err := db.Create(&disabledGroup).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.RouteGroupKey{RouteGroupID: disabledGroup.ID, PlatformKeyID: k.ID}).Error; err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db, Enc: enc, Client: upstream.NewClient()}
	res, err := s.SyncAllModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.OK != 1 || res.GroupsNotified != 0 {
		t.Fatalf("res=%+v", res)
	}
	var n int64
	if err := s.DB.Model(&domain.Notice{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("disabled group must not notify, notices=%d", n)
	}

	// Disabling the upstream excludes its keys from both refresh and diff.
	if err := db.Model(&up).Update("status", domain.StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	res, err = s.SyncAllModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.OK != 0 || res.Failed != 0 {
		t.Fatalf("disabled upstream should not refresh: %+v", res)
	}
}

func TestGroupModelSetGlobAndDrift(t *testing.T) {
	g := domain.RouteGroup{Name: "g", Models: domain.JSONStrings{"grok-*", "fixed"}}
	rateHigh, rateLow := 2.0, 0.1
	keys := map[uint]domain.PlatformKey{
		1: {ID: 1, RateMultiplier: rateHigh, LastModels: domain.JSONStrings{"grok-4", "other"}},
		2: {ID: 2, RateMultiplier: rateLow, LastModels: domain.JSONStrings{"grok-5"}},
	}
	set := groupModelSet(&g, []uint{1, 2}, keys)
	// key 1 drifts out of the default [nil, nil) range? No — no bounds, both in.
	if _, ok := set["fixed"]; !ok {
		t.Fatalf("exact pattern missing: %v", set)
	}
	if _, ok := set["grok-4"]; !ok {
		t.Fatalf("glob expansion wrong: %v", set)
	}
	if _, ok := set["grok-5"]; !ok {
		t.Fatalf("glob expansion wrong: %v", set)
	}
	if _, ok := set["other"]; ok {
		t.Fatalf("unmatched id leaked: %v", set)
	}

	gCapped := domain.RouteGroup{Name: "g", RateMax: &rateHigh}
	set = groupModelSet(&gCapped, []uint{1, 2}, keys)
	if _, ok := set["grok-4"]; ok {
		t.Fatalf("drifted key should be excluded: %v", set)
	}
	if _, ok := set["grok-5"]; !ok {
		t.Fatalf("in-range key should contribute: %v", set)
	}
}
