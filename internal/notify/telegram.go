package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

type TelegramNotifier struct {
	botToken string
	client   *http.Client
}

func NewTelegram(botToken string) *TelegramNotifier {
	return &TelegramNotifier{
		botToken: botToken,
		client:   &http.Client{},
	}
}

type sendMessageRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

func (t *TelegramNotifier) Send(ctx context.Context, chatID string, message string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	body, err := json.Marshal(sendMessageRequest{
		ChatID: chatID,
		Text:   message,
	})
	if err != nil {
		return fmt.Errorf("notify.telegram.Send: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify.telegram.Send: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("notify.telegram.Send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("notify.telegram.Send: status %d: %s", resp.StatusCode, string(respBody))
	}

	slog.Info("telegram notification sent", "chat_id", chatID)
	return nil
}
