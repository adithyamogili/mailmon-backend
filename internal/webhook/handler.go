package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hibiken/asynq"
	"github.com/mohitd/mail-cron/internal/notify"
	"github.com/mohitd/mail-cron/internal/user"
	"github.com/mohitd/mail-cron/internal/worker"
	"log/slog"
	"net/http"
	"strings"
	"time"
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
	Message       *telegramMessage       `json:"message"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query"`
}

type telegramCallbackQuery struct {
	From    telegramUser    `json:"from"`
	Data    string          `json:"data"`
	Message telegramMessage `json:"message"`
}

type telegramUser struct {
	ID int64 `json:"id"`
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

	if update.Message == nil && update.CallbackQuery == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	chatID := ""
	text := ""

	if update.Message != nil {
		chatID = fmt.Sprintf("%d", update.Message.Chat.ID)
		text = strings.TrimSpace(update.Message.Text)
		slog.Info("webhook: received telegram message", "chat_id", chatID)
	} else if update.CallbackQuery != nil {
		chatID = fmt.Sprintf("%d", update.CallbackQuery.Message.Chat.ID)
		text = update.CallbackQuery.Data
		slog.Info("webhook: received telegram callback query", "chat_id", chatID)
	}

	w.WriteHeader(http.StatusOK)

	// Handle /link command
	if strings.HasPrefix(text, "/link ") {
		code := strings.TrimSpace(strings.TrimPrefix(text, "/link "))
		h.handleLink(r, chatID, code)
		return
	}

	// Handle /start (just greet)
	if text == "/start" {
		if err := h.notifier.Send(r.Context(), chatID, "Welcome to Mail Monitor! Register at the website to get started."); err != nil {
			slog.Error("webhook: failed to send start message", "err", err)
		}
		return
	}

	// On-demand query for registered users
	u, err := h.userStore.GetByChatID(r.Context(), chatID)
	if err != nil || u == nil {
		if err := h.notifier.Send(r.Context(), chatID, "You're not registered yet. Visit the website to sign up."); err != nil {
			slog.Error("webhook: failed to send unregistered message", "err", err)
		}
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
		if err := h.notifier.Send(r.Context(), chatID, "Usage: /link <code>"); err != nil {
			slog.Error("webhook: failed to send usage message", "err", err)
		}
		return
	}

	email, err := h.linkResolver.ResolveLinkCode(r.Context(), code)
	if err != nil {
		slog.Error("webhook: resolve link code failed", "err", err)
		if err := h.notifier.Send(r.Context(), chatID, "Something went wrong. Try again."); err != nil {
			slog.Error("webhook: failed to send error msg", "err", err)
		}
		return
	}
	if email == "" {
		if err := h.notifier.Send(r.Context(), chatID, "Invalid or expired code. Go back to the website and try again."); err != nil {
			slog.Error("webhook: failed to send invalid code msg", "err", err)
		}
		return
	}

	if err := h.userStore.UpdateTelegramChatID(r.Context(), email, chatID); err != nil {
		slog.Error("webhook: update chat id failed", "err", err)
		if err := h.notifier.Send(r.Context(), chatID, "Failed to link. Try again."); err != nil {
			slog.Error("webhook: failed to send update fail msg", "err", err)
		}
		return
	}

	slog.Info("webhook: telegram linked", "email", email, "chat_id", chatID)
	if err := h.notifier.Send(r.Context(), chatID, fmt.Sprintf("Linked to %s! Go back to the browser to connect Gmail.", email)); err != nil {
		slog.Error("webhook: failed to send linked msg", "err", err)
	}
}
