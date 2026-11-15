package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"p1228/gateway/internal/config"
	"p1228/gateway/internal/discord"
	"p1228/gateway/internal/logging"
	"p1228/gateway/internal/server"
	"p1228/gateway/internal/storage"
)

// ===== 1. エントリポイント =====
func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// ===== 2. 起動処理 =====
func run() error {
	configPath := flag.String("config", "./config/gateway.yaml", "path to gateway YAML configuration")
	flag.Parse()

	log := logging.New("info", "json")
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("configuration failed", "error", err)
		return err
	}
	log = logging.New(cfg.Logging.Level, cfg.Logging.Format)
	store, err := storage.Open(cfg.Database.Path)
	if err != nil {
		log.Error("storage initialization failed", "error", err)
		return err
	}
	defer store.Close()

	// ----- 2.1 Bot登録 -----
	for botID, bot := range cfg.Bots {
		if err := store.UpsertBot(context.Background(), botID, bot.DisplayName, bot.ApplicationID); err != nil {
			log.Error("bot registry initialization failed", "bot_id", botID, "error", err)
			return err
		}
	}

	manager := discord.NewManager(cfg, store, log)
	// ===== server.New re-indexes workers by worker_id, so the config map can be =====
	// ===== passed directly instead of copying it entry by entry. =====
	api := server.New(cfg, store, manager, cfg.Workers, log)

	// ----- 2.2 終了シグナルと定期クリーンアップ -----
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager.Start(ctx)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				deleted, err := store.PruneExpiredEvents(cleanupCtx, cfg.Gateway.EventPayloadRetentionHours)
				cancel()
				if err != nil {
					log.Warn("event retention cleanup failed", "error", err)
				} else if deleted > 0 {
					log.Info("expired events pruned", "count", deleted)
				}
			}
		}
	}()

	// ----- 2.3 起動とシャットダウン -----
	serveErr := make(chan error, 1)
	go func() { serveErr <- api.ListenAndServe() }()
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("internal API stopped unexpectedly", "error", err)
			runErr = err
		}
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP shutdown failed", "error", err)
		if runErr == nil {
			runErr = err
		}
	}
	log.Info("gateway stopped")
	return runErr
}
