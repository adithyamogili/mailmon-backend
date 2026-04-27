package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/mohitd/mail-cron/internal/notify"
	"github.com/mohitd/mail-cron/internal/user"
	"github.com/mohitd/mail-cron/internal/worker"
)

type LinkResolver interface {
	ResolveLinkCode(ctx context.Context, code string) (string, error)
}

type Handler struct {
	asynqClient  *asynq.Client
	userStore    *user.Store
	notifier     *notify.TelegramNotifier
	linkResolver LinkResolver
}

type telegramUpdate struct {
	Message *telegramMessage `json:"message"`
}

type telegramMessage struct {
	Chat telegramChat `json:"chat"`
	Text string       `json:"text"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}

func (h *Handler) HandleTelegram(w http.ResponseWriter, r *http.Request) {
	var update telegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		slog.Error("webhook: parse telegram update failed", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	if update.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	chatID := fmt.Sprintf("%d", update.Message.Chat.ID)
	text := strings.TrimSpace(update.Message.Text)
	slog.Info("webhook: received telegram message", "chat_id", chatID, "text", text)

	w.WriteHeader(http.StatusOK)

	// Handle /link command
	if strings.HasPrefix(text, "/link ") {
		code := strings.TrimSpace(strings.TrimPrefix(text, "/link "))
		h.handleLink(r, chatID, code)
		return
	}

	// Handle /start (just greet)
	if text == "/start" {
		h.notifier.Send(r.Context(), chatID, "Welcome to Mail Monitor! Register at the website to get started.")
		return
	}

	// On-demand query for registered users
	u, err := h.userStore.GetByChatID(r.Context(), chatID)
	if err != nil || u == nil {
		h.notifier.Send(r.Context(), chatID, "You're not registered yet. Visit the website to sign up.")
		return
	}

	payload, err := worker.NewOnDemandTask(u.ID, chatID)
	if err != nil {
		slog.Error("webhook: marshal task payload failed", "err", err)
		return
	}

	task := asynq.NewTask(worker.TypeProcessOnDemand, payload, asynq.MaxRetry(3), asynq.Timeout(2*time.Minute))
	info, err := h.asynqClient.Enqueue(task)
	if err != nil {
		slog.Error("webhook: enqueue failed", "err", err)
		return
	}
	slog.Info("webhook: task enqueued", "task_id", info.ID, "user_id", u.ID)
}

func (h *Handler) handleLink(r *http.Request, chatID, code string) {
	if code == "" {
		h.notifier.Send(r.Context(), chatID, "Usage: /link <code>")
		return
	}

	email, err := h.linkResolver.ResolveLinkCode(r.Context(), code)
	if err != nil {
		slog.Error("webhook: resolve link code failed", "err", err)
		h.notifier.Send(r.Context(), chatID, "Something went wrong. Try again.")
		return
	}
	if email == "" {
		h.notifier.Send(r.Context(), chatID, "Invalid or expired code. Go back to the website and try again.")
		return
	}

	if err := h.userStore.UpdateTelegramChatID(r.Context(), email, chatID); err != nil {
		slog.Error("webhook: update chat id failed", "err", err)
		h.notifier.Send(r.Context(), chatID, "Failed to link. Try again.")
		return
	}

	slog.Info("webhook: telegram linked", "email", email, "chat_id", chatID)
	h.notifier.Send(r.Context(), chatID, fmt.Sprintf("Linked to %s! Go back to the browser to connect Gmail.", email))
}
