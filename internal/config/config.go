package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	GroqAPIKey         string
	TelegramBotToken   string
	GoogleClientID     string
	GoogleClientSecret string
	JWTSecret          string
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	WebhookPort        string
	BaseURL            string
	FrontendURL        string
	RedisURL           string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		GroqAPIKey:         os.Getenv("GROQ_API_KEY"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		RedisAddr:          envOr("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      os.Getenv("REDIS_PASSWORD"),
		WebhookPort:        envOr("WEBHOOK_PORT", "8080"),
		BaseURL:            envOr("BASE_URL", "http://localhost:8080"),
		FrontendURL:        envOr("FRONTEND_URL", "http://localhost:5173"),
		RedisURL:           os.Getenv("REDIS_URL"),
	}

	dbStr := envOr("REDIS_DB", "0")
	db, err := strconv.Atoi(dbStr)
	if err != nil {
		return nil, fmt.Errorf("REDIS_DB must be an integer: %w", err)
	}
	cfg.RedisDB = db

	var missing []string
	for _, req := range []struct{ name, val string }{
		{"GROQ_API_KEY", cfg.GroqAPIKey},
		{"TELEGRAM_BOT_TOKEN", cfg.TelegramBotToken},
		{"GOOGLE_CLIENT_ID", cfg.GoogleClientID},
		{"GOOGLE_CLIENT_SECRET", cfg.GoogleClientSecret},
		{"JWT_SECRET", cfg.JWTSecret},
	} {
		if req.val == "" {
			missing = append(missing, req.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
