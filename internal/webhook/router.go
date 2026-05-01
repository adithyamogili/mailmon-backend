package webhook

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hibiken/asynq"
	"github.com/mohitd/mail-cron/internal/api"
	"github.com/mohitd/mail-cron/internal/notify"
	"github.com/mohitd/mail-cron/internal/user"
)

func NewRouter(asynqClient *asynq.Client, userStore *user.Store, apiHandlers *api.Handlers, notifier *notify.TelegramNotifier, jwtSecret string) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)

	tgHandler := &Handler{
		asynqClient:  asynqClient,
		userStore:    userStore,
		notifier:     notifier,
		linkResolver: apiHandlers,
	}

	// Public
	r.Post("/api/auth/google", apiHandlers.HandleGoogleLogin)
	r.Get("/api/gmail/callback", apiHandlers.HandleGmailCallback)
	r.Post("/webhook/telegram", tgHandler.HandleTelegram)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// Authenticated
	r.Group(func(r chi.Router) {
		r.Use(api.AuthMiddleware(jwtSecret))
		r.Get("/api/auth/me", apiHandlers.HandleMe)
		r.Post("/api/auth/logout", apiHandlers.HandleLogout)
		r.Get("/api/gmail/connect", apiHandlers.HandleGmailConnect)
		r.Post("/api/gmail/disconnect", apiHandlers.HandleGmailDisconnect)
		r.Put("/api/cron", apiHandlers.HandleUpdateCron)
		r.Get("/api/telegram/link-code", apiHandlers.HandleTelegramLinkCode)
		r.Delete("/api/account", apiHandlers.HandleDeleteAccount)
	})

	return r
}
