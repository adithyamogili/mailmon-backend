package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type GroqClassifier struct {
	apiKey string
	model  string
	client *http.Client
}

func NewGroq(apiKey string) *GroqClassifier {
	return &GroqClassifier{
		apiKey: apiKey,
		model:  "llama-3.3-70b-versatile",
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (g *GroqClassifier) ClassifyBatch(ctx context.Context, inputs []EmailInput) ([]Classification, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	userPrompt := buildBatchUserPrompt(inputs)

	reqBody := chatRequest{
		Model: g.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.1,
		MaxTokens:   512,
	}

	var lastErr error
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

	for attempt, wait := range backoff {
		body, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("classifier.GroqClassifyBatch: marshal: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("classifier.GroqClassifyBatch: new request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+g.apiKey)

		resp, err := g.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("classifier.GroqClassifyBatch: attempt %d: %w", attempt+1, err)
			slog.Warn("groq call failed, retrying", "attempt", attempt+1, "err", err)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("classifier.GroqClassifyBatch: context cancelled: %w", ctx.Err())
			}
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("classifier.GroqClassifyBatch: attempt %d: read body: %w", attempt+1, err)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("classifier.GroqClassifyBatch: context cancelled: %w", ctx.Err())
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("classifier.GroqClassifyBatch: attempt %d: status %d: %s", attempt+1, resp.StatusCode, string(respBody))
			slog.Warn("groq call failed, retrying", "attempt", attempt+1, "status", resp.StatusCode)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("classifier.GroqClassifyBatch: context cancelled: %w", ctx.Err())
			}
			continue
		}

		var chatResp chatResponse
		if err := json.Unmarshal(respBody, &chatResp); err != nil {
			lastErr = fmt.Errorf("classifier.GroqClassifyBatch: attempt %d: unmarshal response: %w", attempt+1, err)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("classifier.GroqClassifyBatch: context cancelled: %w", ctx.Err())
			}
			continue
		}

		if len(chatResp.Choices) == 0 {
			lastErr = fmt.Errorf("classifier.GroqClassifyBatch: attempt %d: no choices", attempt+1)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("classifier.GroqClassifyBatch: context cancelled: %w", ctx.Err())
			}
			continue
		}

		text := chatResp.Choices[0].Message.Content
		results, err := parseBatchClassification(text, len(inputs))
		if err != nil {
			lastErr = fmt.Errorf("classifier.GroqClassifyBatch: attempt %d: %w", attempt+1, err)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("classifier.GroqClassifyBatch: context cancelled: %w", ctx.Err())
			}
			continue
		}

		return results, nil
	}

	return nil, fmt.Errorf("classifier.GroqClassifyBatch: all retries exhausted: %w", lastErr)
}
