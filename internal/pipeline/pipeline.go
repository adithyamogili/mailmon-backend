package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mohitd/mail-cron/internal/classifier"
	"github.com/mohitd/mail-cron/internal/dedup"
	"github.com/mohitd/mail-cron/internal/gmail"
	"github.com/mohitd/mail-cron/internal/notify"
	"github.com/mohitd/mail-cron/internal/store"
	"github.com/mohitd/mail-cron/internal/user"
)

type Pipeline struct {
	gmailPool  *gmail.ClientPool
	classifier classifier.Classifier
	dedup      *dedup.RedisDedup
	notifier   *notify.TelegramNotifier
	store      *store.RedisStore
	userStore  *user.Store
}

func New(
	pool *gmail.ClientPool,
	cls classifier.Classifier,
	dd *dedup.RedisDedup,
	notifier *notify.TelegramNotifier,
	st *store.RedisStore,
	us *user.Store,
) *Pipeline {
	return &Pipeline{
		gmailPool:  pool,
		classifier: cls,
		dedup:      dd,
		notifier:   notifier,
		store:      st,
		userStore:  us,
	}
}

func (p *Pipeline) RunScheduled(ctx context.Context, userID string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	u, err := p.userStore.Get(ctx, userID)
	if err != nil || u == nil {
		slog.Error("pipeline: user not found", "user_id", userID, "err", err)
		return
	}

	lastRun, err := p.store.GetLastRunTime(ctx, userID)
	if err != nil {
		slog.Error("pipeline: failed to get last run time", "user_id", userID, "err", err)
		return
	}

	results, err := p.classifyEmails(ctx, userID, u.Keywords, lastRun)
	if err != nil {
		slog.Error("pipeline: scheduled run failed", "user_id", userID, "err", err)
		return
	}

	if err := p.store.SetLastRunTime(ctx, userID, time.Now()); err != nil {
		slog.Error("pipeline: failed to update last run time", "user_id", userID, "err", err)
	}

	if u.TelegramChatID == "" {
		slog.Warn("pipeline: no telegram chat id, skipping notifications", "user_id", userID)
		return
	}

	for _, r := range results {
		notified, err := p.dedup.IsNotified(ctx, userID, r.email.ID)
		if err != nil {
			slog.Warn("pipeline: notification dedup check failed", "id", r.email.ID, "err", err)
		}
		if notified {
			continue
		}

		msg := formatNotification(r.email, r.classification)
		if err := p.notifier.Send(ctx, u.TelegramChatID, msg); err != nil {
			slog.Error("pipeline: failed to send notification", "user_id", userID, "subject", r.email.Subject, "err", err)
			continue
		}
		if err := p.dedup.MarkNotified(ctx, userID, r.email.ID); err != nil {
			slog.Warn("pipeline: mark notified failed", "id", r.email.ID, "err", err)
		}
	}
}

func (p *Pipeline) RunOnDemand(ctx context.Context, userID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	u, err := p.userStore.Get(ctx, userID)
	if err != nil || u == nil {
		return "", fmt.Errorf("pipeline: user not found: %s", userID)
	}

	since := time.Now().Add(-1 * time.Hour)
	results, err := p.classifyEmails(ctx, userID, u.Keywords, since)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "No job-related emails in the last hour.", nil
	}
	return formatSummary(results), nil
}

type result struct {
	email          gmail.Email
	classification *classifier.Classification
}

func (p *Pipeline) classifyEmails(ctx context.Context, userID string, keywords []string, since time.Time) ([]result, error) {
	gmailClient, ok := p.gmailPool.Get(userID)
	if !ok {
		return nil, fmt.Errorf("pipeline: no gmail client for user %s", userID)
	}

	emails, err := gmailClient.FetchSince(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("pipeline: fetch: %w", err)
	}
	slog.Info("pipeline: fetched emails", "user_id", userID, "count", len(emails), "since", since.Format(time.RFC3339))

	filtered := gmail.KeywordFilter(emails, keywords)
	slog.Info("pipeline: keyword filter", "user_id", userID, "survived", len(filtered), "total", len(emails))

	var results []result
	var uncachedEmails []gmail.Email
	var uncachedInputs []classifier.EmailInput
	cacheHits := 0

	for _, e := range filtered {
		cached, err := p.dedup.GetCached(ctx, userID, e.ID)
		if err != nil {
			slog.Warn("pipeline: cache lookup failed", "id", e.ID, "err", err)
		}
		if cached != nil {
			cacheHits++
			if cached.Relevant {
				results = append(results, result{email: e, classification: cached})
			}
			continue
		}
		uncachedEmails = append(uncachedEmails, e)
		uncachedInputs = append(uncachedInputs, classifier.EmailInput{
			Subject:     e.Subject,
			From:        e.From,
			BodyPreview: e.BodyPreview,
		})
	}

	if len(uncachedInputs) > 0 {
		classifications, err := p.classifier.ClassifyBatch(ctx, uncachedInputs)
		if err != nil {
			slog.Error("pipeline: batch classify failed", "user_id", userID, "err", err)
		} else {
			for i, cls := range classifications {
				e := uncachedEmails[i]
				if err := p.dedup.CacheResult(ctx, userID, e.ID, &cls); err != nil {
					slog.Warn("pipeline: cache write failed", "id", e.ID, "err", err)
				}
				if cls.Relevant {
					results = append(results, result{email: e, classification: &cls})
				}
			}
		}
	}

	slog.Info("pipeline: classification done", "user_id", userID, "relevant", len(results), "cache_hits", cacheHits, "llm_calls_batched", len(uncachedInputs))
	return results, nil
}

func formatNotification(e gmail.Email, c *classifier.Classification) string {
	category := strings.ReplaceAll(c.Category, "_", " ")
	return fmt.Sprintf("[%s]\nFrom: %s\nSubject: %s\n\n%s", strings.ToUpper(category), e.From, e.Subject, c.Summary)
}

func formatSummary(results []result) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d job email(s) in the last hour:\n\n", len(results)))
	for i, r := range results {
		category := strings.ReplaceAll(r.classification.Category, "_", " ")
		b.WriteString(fmt.Sprintf("%d. [%s] %s\n   %s\n\n", i+1, strings.ToUpper(category), r.email.Subject, r.classification.Summary))
	}
	return b.String()
}
