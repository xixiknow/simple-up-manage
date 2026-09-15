package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"simple-up-manage/internal/config"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/jobs"
	"simple-up-manage/internal/logarchive"
	"simple-up-manage/internal/ops"
	"simple-up-manage/internal/picker"
	"simple-up-manage/internal/router"
	"simple-up-manage/internal/store"

	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	enc, err := crypto.New(cfg.EncryptKey)
	if err != nil {
		log.Fatalf("encrypt key: %v", err)
	}
	db, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	if err := store.AutoMigrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Printf("database ready")

	var rdb *redis.Client
	if cfg.RedisURL != "" {
		opt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Fatalf("redis url: %v", err)
		}
		rdb = redis.NewClient(opt)
	}

	opsSvc := ops.New(db, enc, rdb)
	archives, err := logarchive.New(db, cfg.LogBodiesDir, cfg.LogBodyMaxBytes, cfg.LogBodiesMaxBytes)
	if err != nil {
		log.Fatalf("log archives: %v", err)
	}
	opsSvc.Archives = archives
	pick := picker.NewBand(db, rdb)
	stop := make(chan struct{})
	jobs.Start(cfg, opsSvc, pick.Reload, stop)

	engine := router.New(cfg, db, enc, opsSvc, pick)
	server := &http.Server{Addr: cfg.Listen, Handler: engine, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("listening on %s", cfg.Listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	close(stop)
	log.Printf("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 310*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
		_ = server.Close()
	}
	done := make(chan struct{})
	go func() { archives.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("archive shutdown deadline exceeded")
	}
}
