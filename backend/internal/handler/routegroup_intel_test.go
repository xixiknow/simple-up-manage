package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"simple-up-manage/internal/domain"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// seedQuarantinedMember builds a group whose member key carries a live
// quarantine state under the group's candy plan — the state a system
// quarantine leaves behind. The operator-facing flows must clear it the
// moment the key is removed from the group.
func seedQuarantinedMember(t *testing.T, name string) (*gorm.DB, *domain.RouteGroup, *domain.PlatformKey, *domain.IntelTestPlan) {
	t.Helper()
	db := logTestDB(t)
	group := domain.RouteGroup{Name: name, Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: name + "-provider", BaseURL: "https://example.com", Kind: domain.KindOpenAICompat, Protocols: "openai"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	key := domain.PlatformKey{UpstreamID: up.ID, Name: name + "-key", Status: domain.StatusEnabled}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	plan := domain.IntelTestPlan{RouteGroupID: group.ID, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Parallel: 4, Enabled: true, QuarantineEnabled: true}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.RouteGroupKey{RouteGroupID: group.ID, PlatformKeyID: key.ID}).Error; err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(-time.Minute)
	state := domain.IntelQuarantineState{
		PlanID: plan.ID, PlatformKeyID: key.ID, UpstreamID: up.ID,
		Status: domain.IntelQuarantineQuarantined, BackoffSec: 60,
		QuarantinedAt: next, NextTestAt: &next,
	}
	if err := db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	return db, &group, &key, &plan
}

func quarantineStateCount(t *testing.T, db *gorm.DB, planID, keyID uint) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&domain.IntelQuarantineState{}).
		Where("plan_id = ? AND platform_key_id = ?", planID, keyID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func memberCount(t *testing.T, db *gorm.DB, groupID, keyID uint) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&domain.RouteGroupKey{}).
		Where("route_group_id = ? AND platform_key_id = ?", groupID, keyID).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}

func postJSON(c *gin.Context, body any) {
	raw, _ := json.Marshal(body)
	c.Request = httptest.NewRequest("POST", "/test", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
}

func TestSetRouteGroupKeysClearsQuarantineOnRemoval(t *testing.T) {
	db, group, key, plan := seedQuarantinedMember(t, "rg-replace")
	admin := &Admin{DB: db}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	postJSON(c, routeGroupKeysBody{KeyIDs: []uint{}})
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(group.ID), 10)}}
	admin.SetRouteGroupKeys(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if n := quarantineStateCount(t, db, plan.ID, key.ID); n != 0 {
		t.Fatalf("quarantine states left = %d, want 0 after member removal", n)
	}
	if n := memberCount(t, db, group.ID, key.ID); n != 0 {
		t.Fatalf("membership left = %d, want 0", n)
	}
}

func TestBatchRouteGroupKeysClearsQuarantineOnRemove(t *testing.T) {
	db, group, key, plan := seedQuarantinedMember(t, "rg-batch")
	admin := &Admin{DB: db}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	postJSON(c, routeGroupBatchBody{Remove: []uint{key.ID}})
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(group.ID), 10)}}
	admin.BatchRouteGroupKeys(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if n := quarantineStateCount(t, db, plan.ID, key.ID); n != 0 {
		t.Fatalf("quarantine states left = %d, want 0 after batch remove", n)
	}
	if n := memberCount(t, db, group.ID, key.ID); n != 0 {
		t.Fatalf("membership left = %d, want 0", n)
	}
}

func TestBatchRouteGroupKeysKeepsQuarantineOnAdd(t *testing.T) {
	db, group, key, plan := seedQuarantinedMember(t, "rg-batch-add")
	admin := &Admin{DB: db}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	postJSON(c, routeGroupBatchBody{Add: []uint{key.ID}})
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(group.ID), 10)}}
	admin.BatchRouteGroupKeys(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// Adding members must not touch existing quarantine states: the recovery
	// retest loop keeps running for quarantined keys.
	if n := quarantineStateCount(t, db, plan.ID, key.ID); n != 1 {
		t.Fatalf("quarantine states = %d, want 1 (add must not clear)", n)
	}
}

func TestSetKeyRouteGroupsClearsQuarantineOnLeftGroups(t *testing.T) {
	db, group1, key, plan1 := seedQuarantinedMember(t, "rg-key-left")
	group2 := domain.RouteGroup{Name: "rg-key-right", Status: domain.StatusEnabled}
	if err := db.Create(&group2).Error; err != nil {
		t.Fatal(err)
	}
	plan2 := domain.IntelTestPlan{RouteGroupID: group2.ID, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Parallel: 4, Enabled: true, QuarantineEnabled: true}
	if err := db.Create(&plan2).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.RouteGroupKey{RouteGroupID: group2.ID, PlatformKeyID: key.ID}).Error; err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(-time.Minute)
	state2 := domain.IntelQuarantineState{
		PlanID: plan2.ID, PlatformKeyID: key.ID, UpstreamID: key.UpstreamID,
		Status: domain.IntelQuarantineQuarantined, BackoffSec: 60,
		QuarantinedAt: next, NextTestAt: &next,
	}
	if err := db.Create(&state2).Error; err != nil {
		t.Fatal(err)
	}

	admin := &Admin{DB: db}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	postJSON(c, keyRouteGroupsBody{RouteGroupIDs: []uint{group2.ID}})
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(key.ID), 10)}}
	admin.SetKeyRouteGroups(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if n := quarantineStateCount(t, db, plan1.ID, key.ID); n != 0 {
		t.Fatalf("quarantine states on left group = %d, want 0", n)
	}
	if n := quarantineStateCount(t, db, plan2.ID, key.ID); n != 1 {
		t.Fatalf("quarantine states on kept group = %d, want 1", n)
	}
	if n := memberCount(t, db, group1.ID, key.ID); n != 0 {
		t.Fatalf("membership on left group = %d, want 0", n)
	}
	if n := memberCount(t, db, group2.ID, key.ID); n != 1 {
		t.Fatalf("membership on kept group = %d, want 1", n)
	}
}

func TestDeleteRouteGroupDisablesPlansAndClearsQuarantine(t *testing.T) {
	db, group, key, plan := seedQuarantinedMember(t, "rg-delete")
	admin := &Admin{DB: db}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("DELETE", "/test", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(group.ID), 10)}}
	admin.DeleteRouteGroup(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var planRow domain.IntelTestPlan
	if err := db.First(&planRow, plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if planRow.Enabled {
		t.Fatalf("plan %d must be disabled after its group is deleted", plan.ID)
	}
	if n := quarantineStateCount(t, db, plan.ID, key.ID); n != 0 {
		t.Fatalf("quarantine states left = %d, want 0 after group deletion", n)
	}
	var groupCount int64
	if err := db.Model(&domain.RouteGroup{}).Where("id = ?", group.ID).Count(&groupCount).Error; err != nil {
		t.Fatal(err)
	}
	if groupCount != 0 {
		t.Fatalf("route group rows left = %d, want 0", groupCount)
	}
}
