package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
	"github.com/mohitd/mail-cron/internal/notify"
	"github.com/mohitd/mail-cron/internal/pipeline"
)

type Processor struct {
	pipe     *pipeline.Pipeline
	notifier *notify.TelegramNotifier
}

func NewProcessor(pipe *pipeline.Pipeline, notifier *notify.TelegramNotifier) *Processor {
	return &Processor{pipe: pipe, notifier: notifier}
}

func (p *Processor) HandleOnDemand(ctx context.Context, t *asynq.Task) error {
	var payload OnDemandPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("worker.HandleOnDemand: unmarshal: %w", err)
	}

	slog.Info("worker: processing on-demand request", "user_id", payload.UserID, "reply_to", payload.ReplyTo)

	summary, err := p.pipe.RunOnDemand(ctx, payload.UserID)
	if err != nil {
		slog.Error("worker: pipeline failed", "err", err)
		return fmt.Errorf("worker.HandleOnDemand: pipeline: %w", err)
	}

	if err := p.notifier.Send(ctx, payload.ReplyTo, summary); err != nil {
		slog.Error("worker: send reply failed", "err", err)
		return fmt.Errorf("worker.HandleOnDemand: send reply: %w", err)
	}

	slog.Info("worker: on-demand reply sent", "to", payload.ReplyTo)
	return nil
}
