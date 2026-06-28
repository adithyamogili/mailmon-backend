package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mohitd/mail-cron/internal/gmail"
	"github.com/mohitd/mail-cron/internal/scheduler"
	"github.com/mohitd/mail-cron/internal/user"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"
)

type Handlers struct {
	userStore      *user.Store
	gmailPool      *gmail.ClientPool
	scheduler      *scheduler.Scheduler
	rdb            *redis.Client
	jwtSecret      string
	googleClientID string
	googleSecret   string
	baseURL        string
	frontendURL    string
}

type HandlersConfig struct {
	UserStore      *user.Store
	GmailPool      *gmail.ClientPool
	Scheduler      *scheduler.Scheduler
	Redis          *redis.Client
	JWTSecret      string
	GoogleClientID string
	GoogleSecret   string
	BaseURL        string
	FrontendURL    string
}

func NewHandlers(cfg HandlersConfig) *Handlers {
	return &Handlers{
		userStore:      cfg.UserStore,
		gmailPool:      cfg.GmailPool,
		scheduler:      cfg.Scheduler,
		rdb:            cfg.Redis,
		jwtSecret:      cfg.JWTSecret,
		googleClientID: cfg.GoogleClientID,
		googleSecret:   cfg.GoogleSecret,
		baseURL:        cfg.BaseURL,
		frontendURL:    cfg.FrontendURL,
	}
}

func (h *Handlers) HandleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	info, err := verifyGoogleIDToken(body.Credential, h.googleClientID)
	if err != nil {
		slog.Error("api: google token verification failed", "err", err)
		jsonError(w, "google auth failed", http.StatusInternalServerError)
		return
	}

	u := &user.User{
		ID:      info.Sub,
		Email:   info.Email,
		Name:    info.Name,
		Picture: info.Picture,
		Keywords: []string{
			// Core job-search terms
			"interview", "interview invitation", "interview scheduled", "phone interview",
			"video interview", "virtual interview", "technical interview", "behavioural interview",
			"final interview", "panel interview", "hiring manager interview",
			// Offer & compensation
			"offer", "job offer", "employment offer", "offer letter",
			"compensation", "salary", "pay", "payroll", "equity", "signing bonus", "relocation",
			// Assessments & tasks
			"assessment", "online assessment", "take-home", "take home", "coding challenge",
			"code challenge", "hackerrank", "codility", "leetcode", "pair programming",
			// Onsite / office visits
			"onsite", "on-site", "office visit", "site visit", "fly-out",
			// Recruiter / sourcer outreach
			"recruiter", "talent acquisition", "sourcer", "headhunter",
			"hiring manager", "hm ", "people ops", "people team",
			// Application lifecycle
			"job application", "application received", "application update",
			"your application", "application status", "submitted your application",
			// Positive outcomes
			"congratulations", "shortlisted", "selected", "moved forward", "moving forward",
			"next round", "next step", "next stage", "proceed", "advance",
			// Negative outcomes
			"rejection", "regret", "unfortunately", "not moving forward", "not selected",
			"thank you for your interest", "we have decided", "not a match",
			// Scheduling
			"let's schedule", "pick a time", "book a slot", "calendly", "availability",
			"times that work for you",
		},
		CronIntervalMinutes: 30,
	}
	if err := h.userStore.Upsert(r.Context(), u); err != nil {
		slog.Error("api: upsert user failed", "err", err)
		jsonError(w, "failed to save user", http.StatusInternalServerError)
		return
	}

	jwtToken, err := CreateJWT(h.jwtSecret, info.Sub)
	if err != nil {
		jsonError(w, "token creation failed", http.StatusInternalServerError)
		return
	}

	slog.Info("api: user logged in", "user_id", info.Sub, "email", info.Email)

	jsonResponse(w, map[string]string{
		"token": jwtToken,
		"email": info.Email,
		"name":  info.Name,
	})
}

func (h *Handlers) HandleMe(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r.Context())
	u, err := h.userStore.Get(r.Context(), userID)
	if err != nil || u == nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"email":           u.Email,
		"name":            u.Name,
		"picture":         u.Picture,
		"gmail_connected": u.GmailToken != "",
		"telegram_linked": u.TelegramChatID != "",
		"cron_enabled":    u.CronEnabled,
		"cron_interval":   u.CronIntervalMinutes,
		"next_run_at":     u.NextRunAt,
	})
}

func (h *Handlers) HandleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r.Context())
	h.scheduler.Stop(userID)
	h.gmailPool.Remove(userID)
	if err := h.userStore.Delete(r.Context(), userID); err != nil {
		slog.Error("api: delete account failed", "user_id", userID, "err", err)
		jsonError(w, "failed to delete account", http.StatusInternalServerError)
		return
	}
	slog.Info("api: account deleted", "user_id", userID)
	jsonResponse(w, map[string]string{"status": "deleted"})
}

func (h *Handlers) HandleLogout(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, map[string]string{"status": "ok"})
}

func (h *Handlers) HandleGmailConnect(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r.Context())
	cfg := &oauth2.Config{
		ClientID:     h.googleClientID,
		ClientSecret: h.googleSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{gmailapi.GmailReadonlyScope},
		RedirectURL:  h.baseURL + "/api/gmail/callback",
	}
	url := cfg.AuthCodeURL(userID, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	jsonResponse(w, map[string]string{"url": url})
}

func (h *Handlers) HandleGmailCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	userID := r.URL.Query().Get("state")
	if code == "" || userID == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	cfg := &oauth2.Config{
		ClientID:     h.googleClientID,
		ClientSecret: h.googleSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{gmailapi.GmailReadonlyScope},
		RedirectURL:  h.baseURL + "/api/gmail/callback",
	}

	tok, err := cfg.Exchange(context.Background(), code)
	if err != nil {
		slog.Error("api: gmail oauth exchange failed", "err", err)
		http.Redirect(w, r, h.frontendURL+"?error=gmail_auth_failed", http.StatusFound)
		return
	}

	tokJSON, err := gmail.TokenToJSON(tok)
	if err != nil {
		slog.Error("api: token marshal failed", "err", err)
		http.Redirect(w, r, h.frontendURL+"?error=token_error", http.StatusFound)
		return
	}

	if err := h.userStore.UpdateGmailToken(r.Context(), userID, tokJSON); err != nil {
		slog.Error("api: save gmail token failed", "err", err)
		http.Redirect(w, r, h.frontendURL+"?error=save_failed", http.StatusFound)
		return
	}

	onRefresh := func(newTok *oauth2.Token) {
		if newJSON, err := gmail.TokenToJSON(newTok); err == nil {
			if err := h.userStore.UpdateGmailToken(context.Background(), userID, newJSON); err != nil {
				slog.Error("api: background gmail token refresh update failed", "err", err)
			}
		}
	}
	if err := h.gmailPool.AddFromToken(context.Background(), userID, tok, onRefresh); err != nil {
		slog.Error("api: gmail pool add failed", "err", err)
	}
	slog.Info("api: gmail connected", "user_id", userID)
	http.Redirect(w, r, h.frontendURL+"?gmail=connected", http.StatusFound)
}

func (h *Handlers) HandleGmailDisconnect(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r.Context())
	h.scheduler.Stop(userID)
	h.gmailPool.Remove(userID)
	if err := h.userStore.ClearGmailToken(r.Context(), userID); err != nil {
		jsonError(w, "failed to disconnect", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok"})
}

func (h *Handlers) HandleUpdateCron(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r.Context())
	var body struct {
		Enabled         *bool `json:"enabled"`
		IntervalMinutes *int  `json:"interval_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	u, err := h.userStore.Get(r.Context(), userID)
	if err != nil || u == nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	if u.GmailToken == "" {
		jsonError(w, "gmail not connected", http.StatusBadRequest)
		return
	}

	interval := u.CronIntervalMinutes
	if body.IntervalMinutes != nil {
		allowedIntervals := map[int]bool{
			30:   true,
			60:   true,
			120:  true,
			180:  true,
			360:  true,
			1440: true,
		}
		if !allowedIntervals[*body.IntervalMinutes] {
			jsonError(w, "invalid interval", http.StatusBadRequest)
			return
		}
		interval = *body.IntervalMinutes
	}

	enabled := u.CronEnabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	dur := time.Duration(interval) * time.Minute

	if enabled {
		nextRun := time.Now().Add(dur)
		if err := h.userStore.UpdateCron(r.Context(), userID, true, interval, &nextRun); err != nil {
			jsonError(w, "failed to update cron", http.StatusInternalServerError)
			return
		}
		h.scheduler.Start(userID, dur)
	} else {
		if err := h.userStore.UpdateCron(r.Context(), userID, false, interval, nil); err != nil {
			jsonError(w, "failed to update cron", http.StatusInternalServerError)
			return
		}
		h.scheduler.Stop(userID)
	}

	jsonResponse(w, map[string]interface{}{"enabled": enabled, "interval_minutes": interval})
}

func (h *Handlers) HandleTelegramLinkCode(w http.ResponseWriter, r *http.Request) {
	userID := GetUserID(r.Context())
	code := generateCode()
	h.rdb.Set(r.Context(), "linkcode:"+code, userID, 10*time.Minute)
	jsonResponse(w, map[string]string{"code": code})
}

func (h *Handlers) ResolveLinkCode(ctx context.Context, code string) (string, error) {
	key := "linkcode:" + code
	userID, err := h.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	h.rdb.Del(ctx, key)
	return userID, nil
}

func generateCode() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		slog.Error("api: crypto/rand read failed", "err", err)
	}
	return hex.EncodeToString(b)
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("api: failed to encode json response", "err", err)
	}
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		slog.Error("api: failed to encode json error response", "err", err)
	}
}
