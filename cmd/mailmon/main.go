package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mohitd/mail-cron/internal/api"
	"github.com/mohitd/mail-cron/internal/classifier"
	"github.com/mohitd/mail-cron/internal/config"
	"github.com/mohitd/mail-cron/internal/database"
	"github.com/mohitd/mail-cron/internal/dedup"
	"github.com/mohitd/mail-cron/internal/gmail"
	"github.com/mohitd/mail-cron/internal/notify"
	"github.com/mohitd/mail-cron/internal/pipeline"
	"github.com/mohitd/mail-cron/internal/scheduler"
	"github.com/mohitd/mail-cron/internal/store"
	"github.com/mohitd/mail-cron/internal/user"
	"github.com/mohitd/mail-cron/internal/webhook"
	"github.com/mohitd/mail-cron/internal/worker"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	// SQLite
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "mailmon.db"
	}
	db, err := database.Open(dbPath)
	if err != nil {
		log.Fatalf("database open failed: %v", err)
	}
	defer db.Close()
	slog.Info("database initialized")

	// Redis
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis connection failed: %v", err)
	}
	cancel()
	slog.Info("redis connected", "addr", opt.Addr)

	// Gmail OAuth config (shared)
	oauthCfg := gmail.NewOAuthConfig(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.BaseURL+"/api/gmail/callback")

	// Core components
	cls := classifier.NewGroq(cfg.GroqAPIKey)
	dd := dedup.New(rdb)
	tg := notify.NewTelegram(cfg.TelegramBotToken)
	st := store.New(rdb)
	userStore := user.NewStore(db.GetConn())
	gmailPool := gmail.NewPool(oauthCfg)

	// Pipeline
	pipe := pipeline.New(gmailPool, cls, dd, tg, st, userStore)

	// Scheduler with crash recovery
	sched := scheduler.New(userStore, pipe.RunScheduled)

	// Load existing users with Gmail tokens into the pool
	enabledUsers, err := userStore.ListEnabled(context.Background())
	if err != nil {
		slog.Error("failed to list enabled users", "err", err)
	}
	for _, u := range enabledUsers {
		tok, err := gmail.TokenFromJSON(u.GmailToken)
		if err != nil {
			slog.Error("failed to parse gmail token", "user_id", u.ID, "err", err)
			continue
		}
		uid := u.ID
		onRefresh := func(newTok *oauth2.Token) {
			if newJSON, err := gmail.TokenToJSON(newTok); err == nil {
				if updateErr := userStore.UpdateGmailToken(context.Background(), uid, newJSON); updateErr != nil {
					slog.Error("failed to update gmail token", "user_id", u.ID, "err", updateErr)
				}
			}
		}
		if err := gmailPool.AddFromToken(context.Background(), u.ID, tok, onRefresh); err != nil {
			slog.Error("failed to init gmail client", "user_id", u.ID, "err", err)
			continue
		}
		slog.Info("gmail client loaded", "user_id", u.ID)
	}

	// Recover schedules from DB
	sched.Recover()

	// Asynq worker
	redisOpt := asynq.RedisClientOpt{Addr: opt.Addr, Password: opt.Password, DB: opt.DB, TLSConfig: opt.TLSConfig}
	asynqServer := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: 3,
		RetryDelayFunc: func(n int, _ error, _ *asynq.Task) time.Duration {
			return time.Duration(1<<uint(n)) * time.Second
		},
	})
	proc := worker.NewProcessor(pipe, tg)
	mux := asynq.NewServeMux()
	mux.HandleFunc(worker.TypeProcessOnDemand, proc.HandleOnDemand)
	go func() {
		if err := asynqServer.Run(mux); err != nil {
			slog.Error("asynq server error", "err", err)
		}
	}()

	// API handlers
	apiHandlers := api.NewHandlers(api.HandlersConfig{
		UserStore:      userStore,
		GmailPool:      gmailPool,
		Scheduler:      sched,
		Redis:          rdb,
		JWTSecret:      cfg.JWTSecret,
		GoogleClientID: cfg.GoogleClientID,
		GoogleSecret:   cfg.GoogleClientSecret,
		BaseURL:        cfg.BaseURL,
		FrontendURL:    cfg.FrontendURL,
	})

	// HTTP server
	asynqClient := asynq.NewClient(redisOpt)
	router := webhook.NewRouter(asynqClient, userStore, apiHandlers, tg, cfg.JWTSecret)
	httpServer := &http.Server{Addr: ":" + cfg.WebhookPort, Handler: router}
	go func() {
		slog.Info("http server starting", "port", cfg.WebhookPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
		}
	}()

	slog.Info("mail-cron started", "webhook_port", cfg.WebhookPort, "frontend", cfg.FrontendURL)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = httpServer.Shutdown(shutCtx)
	sched.StopAll()
	asynqServer.Shutdown()
	_ = asynqClient.Close()
	_ = rdb.Close()
}
