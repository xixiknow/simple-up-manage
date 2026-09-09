package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"simple-up-manage/internal/config"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/jobs"
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
	pick := picker.NewBand(db, rdb)
	stop := make(chan struct{})
	jobs.Start(cfg, opsSvc, pick.Reload, stop)

	engine := router.New(cfg, db, enc, opsSvc, pick)
	go func() {
		log.Printf("listening on %s", cfg.Listen)
		if err := engine.Run(cfg.Listen); err != nil {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	close(stop)
	log.Printf("shutting down")
}
