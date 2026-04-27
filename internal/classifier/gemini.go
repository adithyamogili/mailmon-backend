package classifier

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/genai"
)

type GeminiClassifier struct {
	client *genai.Client
	model  string
}

func NewGemini(apiKey string) (*GeminiClassifier, error) {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("classifier.NewGemini: %w", err)
	}
	return &GeminiClassifier{
		client: client,
		model:  "gemini-flash-latest",
	}, nil
}

func (g *GeminiClassifier) ClassifyBatch(ctx context.Context, inputs []EmailInput) ([]Classification, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	userPrompt := buildBatchUserPrompt(inputs)

	var lastErr error
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

	for attempt, wait := range backoff {
		resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(userPrompt), &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
			Temperature:       genai.Ptr(float32(0.1)),
			MaxOutputTokens:   512,
		})
		if err != nil {
			lastErr = fmt.Errorf("classifier.ClassifyBatch: attempt %d: %w", attempt+1, err)
			slog.Warn("gemini call failed, retrying", "attempt", attempt+1, "err", err)
			time.Sleep(wait)
			continue
		}

		if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			lastErr = fmt.Errorf("classifier.ClassifyBatch: attempt %d: empty response", attempt+1)
			time.Sleep(wait)
			continue
		}

		text := ""
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.Text != "" {
				text += part.Text
			}
		}

		results, err := parseBatchClassification(text, len(inputs))
		if err != nil {
			lastErr = fmt.Errorf("classifier.ClassifyBatch: attempt %d: %w", attempt+1, err)
			time.Sleep(wait)
			continue
		}

		return results, nil
	}

	return nil, fmt.Errorf("classifier.ClassifyBatch: all retries exhausted: %w", lastErr)
}
