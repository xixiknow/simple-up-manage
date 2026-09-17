package routinghealth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"simple-up-manage/internal/domain"
)

func TestPostgresConcurrentLeasesAndFailures(t *testing.T) {
	dsn := os.Getenv("TEST_ROUTING_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_ROUTING_DATABASE_URL is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	defer conn.Close()
	schema := "routing_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	isolated, err := gorm.Open(postgres.Open(dsn+sep+"search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := isolated.DB()
	pool.SetMaxOpenConns(20)
	defer pool.Close()
	if err := isolated.AutoMigrate(&domain.RoutingCircuit{}, &domain.RoutingObservation{}, &domain.RoutingBudget{}); err != nil {
		t.Fatal(err)
	}
	s := Store{DB: isolated}
	d := Dimension{KeyID: 1, Protocol: "openai", Model: "m", Path: "/v1/responses", Stream: true}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); record(t, s, d, fmt.Sprintf("request-%d", i%3), false) }(i)
	}
	wg.Wait()
	if r := row(t, s, d.Scope()); !r.Open || r.Failures != 3 {
		t.Fatalf("concurrent dedup: %+v", r)
	}
	if err := isolated.Model(&domain.RoutingCircuit{}).Where("scope = ?", d.Scope()).Update("until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int32
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (Store{DB: isolated.Session(&gorm.Session{NewDB: true})}).Admit(ctx, d, true)
			if err == nil {
				admitted.Add(1)
			} else if err != ErrUnavailable {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("PostgreSQL admitted %d requests", admitted.Load())
	}
	var slots atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot, err := s.Slot(ctx, "pool", 20, true)
			if err != nil {
				t.Error(err)
			}
			if slot {
				slots.Add(1)
			}
		}()
	}
	wg.Wait()
	if slots.Load() != 5 {
		t.Fatalf("PostgreSQL exploration budget %d", slots.Load())
	}
	var recoveries atomic.Int32
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.RecoverySlot(ctx, "pool", true)
			if err != nil {
				t.Error(err)
			}
			if ok {
				recoveries.Add(1)
			}
		}()
	}
	wg.Wait()
	if recoveries.Load() != 1 {
		t.Fatalf("PostgreSQL recovery budget %d", recoveries.Load())
	}
	var checks atomic.Int32
	for i := uint(2); i <= 5; i++ {
		dim := d
		dim.KeyID = i
		expiredGate(t, s, dim)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dim := d
			dim.KeyID = uint(2 + i%4)
			_, err := s.ClaimCheck(ctx, dim.Scope(), dim)
			if err == nil {
				checks.Add(1)
			} else if err != ErrUnavailable {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if checks.Load() != 2 {
		t.Fatalf("PostgreSQL check workers %d", checks.Load())
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(ctx, d, true); err == nil {
		t.Fatal("storage outage admitted recovery")
	}
}
