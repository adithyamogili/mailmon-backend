package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mohitd/mail-cron/internal/user"
)

type RunFunc func(ctx context.Context, userID string)

type entry struct {
	timer    *time.Timer
	interval time.Duration
	stop     chan struct{}
}

type Scheduler struct {
	entries   map[string]*entry
	mu        sync.Mutex
	userStore *user.Store
	runFn     RunFunc
}

func New(userStore *user.Store, runFn RunFunc) *Scheduler {
	return &Scheduler{
		entries:   make(map[string]*entry),
		userStore: userStore,
		runFn:     runFn,
	}
}

func (s *Scheduler) Start(userID string, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[userID]; ok {
		e.timer.Stop()
		close(e.stop)
		delete(s.entries, userID)
	}

	nextRun := time.Now().Add(interval)
	s.persistNextRun(userID, &nextRun)

	stopCh := make(chan struct{})
	e := &entry{
		interval: interval,
		stop:     stopCh,
	}
	e.timer = time.AfterFunc(interval, func() {
		s.fire(userID, e)
	})
	s.entries[userID] = e

	slog.Info("scheduler: started", "user_id", userID, "interval", interval, "next_run", nextRun.Format(time.RFC3339))
}

func (s *Scheduler) StartAt(userID string, interval time.Duration, nextRunAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[userID]; ok {
		e.timer.Stop()
		close(e.stop)
		delete(s.entries, userID)
	}

	delay := time.Until(nextRunAt)
	if delay <= 0 {
		slog.Info("scheduler: overdue, firing immediately", "user_id", userID, "was_due", nextRunAt.Format(time.RFC3339))
		delay = 1 * time.Millisecond
	}

	stopCh := make(chan struct{})
	e := &entry{
		interval: interval,
		stop:     stopCh,
	}
	e.timer = time.AfterFunc(delay, func() {
		s.fire(userID, e)
	})
	s.entries[userID] = e

	slog.Info("scheduler: started at", "user_id", userID, "interval", interval, "next_run", nextRunAt.Format(time.RFC3339), "delay", delay)
}

func (s *Scheduler) Stop(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[userID]; ok {
		e.timer.Stop()
		close(e.stop)
		delete(s.entries, userID)
	}

	s.persistNextRun(userID, nil)
	slog.Info("scheduler: stopped", "user_id", userID)
}

func (s *Scheduler) IsRunning(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[userID]
	return ok
}

func (s *Scheduler) fire(userID string, e *entry) {
	select {
	case <-e.stop:
		return
	default:
	}

	slog.Info("scheduler: firing", "user_id", userID)
	s.runFn(context.Background(), userID)

	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-e.stop:
		return
	default:
	}

	nextRun := time.Now().Add(e.interval)
	s.persistNextRun(userID, &nextRun)

	e.timer.Reset(e.interval)
	slog.Info("scheduler: rescheduled", "user_id", userID, "next_run", nextRun.Format(time.RFC3339))
}

func (s *Scheduler) persistNextRun(userID string, nextRun *time.Time) {
	if err := s.userStore.UpdateNextRunAt(context.Background(), userID, nextRun); err != nil {
		slog.Error("scheduler: persist next_run_at failed", "user_id", userID, "err", err)
	}
}

// Recover loads all enabled users and resumes their schedules.
func (s *Scheduler) Recover() {
	users, err := s.userStore.ListEnabled(context.Background())
	if err != nil {
		slog.Error("scheduler: recovery failed", "err", err)
		return
	}
	for _, u := range users {
		interval := time.Duration(u.CronIntervalMinutes) * time.Minute
		if u.NextRunAt != nil {
			s.StartAt(u.ID, interval, *u.NextRunAt)
		} else {
			s.Start(u.ID, interval)
		}
	}
	slog.Info("scheduler: recovered", "users", len(users))
}

func (s *Scheduler) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.entries {
		e.timer.Stop()
		close(e.stop)
		delete(s.entries, id)
	}
}
