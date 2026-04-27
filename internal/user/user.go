package user

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type User struct {
	ID                  string
	Email               string
	Name                string
	Picture             string
	TelegramChatID      string
	GmailToken          string
	Keywords            []string
	CronEnabled         bool
	CronIntervalMinutes int
	NextRunAt           *time.Time
	CreatedAt           time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Upsert(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, email, name, picture, keywords, cron_interval_minutes)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, picture = excluded.picture`,
		u.ID, u.Email, u.Name, u.Picture,
		strings.Join(u.Keywords, ","), u.CronIntervalMinutes,
	)
	if err != nil {
		return fmt.Errorf("user.Upsert: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, name, picture, telegram_chat_id, gmail_token, keywords,
		        cron_enabled, cron_interval_minutes, next_run_at, created_at
		 FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) GetByChatID(ctx context.Context, chatID string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, name, picture, telegram_chat_id, gmail_token, keywords,
		        cron_enabled, cron_interval_minutes, next_run_at, created_at
		 FROM users WHERE telegram_chat_id = ?`, chatID)
	return scanUser(row)
}

func (s *Store) ListEnabled(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, name, picture, telegram_chat_id, gmail_token, keywords,
		        cron_enabled, cron_interval_minutes, next_run_at, created_at
		 FROM users WHERE cron_enabled = 1 AND gmail_token != ''`)
	if err != nil {
		return nil, fmt.Errorf("user.ListEnabled: %w", err)
	}
	defer rows.Close()
	return scanUsers(rows)
}

func (s *Store) UpdateTelegramChatID(ctx context.Context, userID, chatID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET telegram_chat_id = ? WHERE id = ?`, chatID, userID)
	if err != nil {
		return fmt.Errorf("user.UpdateTelegramChatID: %w", err)
	}
	return nil
}

func (s *Store) UpdateGmailToken(ctx context.Context, userID, tokenJSON string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET gmail_token = ? WHERE id = ?`, tokenJSON, userID)
	if err != nil {
		return fmt.Errorf("user.UpdateGmailToken: %w", err)
	}
	return nil
}

func (s *Store) ClearGmailToken(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET gmail_token = '', cron_enabled = 0, next_run_at = NULL WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("user.ClearGmailToken: %w", err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("user.Delete: %w", err)
	}
	return nil
}

func (s *Store) UpdateCron(ctx context.Context, userID string, enabled bool, intervalMinutes int, nextRunAt *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET cron_enabled = ?, cron_interval_minutes = ?, next_run_at = ? WHERE id = ?`,
		enabled, intervalMinutes, nextRunAt, userID)
	if err != nil {
		return fmt.Errorf("user.UpdateCron: %w", err)
	}
	return nil
}

func (s *Store) UpdateNextRunAt(ctx context.Context, userID string, nextRunAt *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET next_run_at = ? WHERE id = ?`, nextRunAt, userID)
	if err != nil {
		return fmt.Errorf("user.UpdateNextRunAt: %w", err)
	}
	return nil
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var kw string
	var nextRun sql.NullTime
	var name, picture sql.NullString
	if err := row.Scan(&u.ID, &u.Email, &name, &picture, &u.TelegramChatID, &u.GmailToken, &kw,
		&u.CronEnabled, &u.CronIntervalMinutes, &nextRun, &u.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("user.scanUser: %w", err)
	}
	if name.Valid {
		u.Name = name.String
	}
	if picture.Valid {
		u.Picture = picture.String
	}
	if nextRun.Valid {
		u.NextRunAt = &nextRun.Time
	}
	u.Keywords = splitKeywords(kw)
	return &u, nil
}

func scanUsers(rows *sql.Rows) ([]*User, error) {
	var users []*User
	for rows.Next() {
		var u User
		var kw string
		var nextRun sql.NullTime
		var name, picture sql.NullString
		if err := rows.Scan(&u.ID, &u.Email, &name, &picture, &u.TelegramChatID, &u.GmailToken, &kw,
			&u.CronEnabled, &u.CronIntervalMinutes, &nextRun, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("user.scanUsers: %w", err)
		}
		if name.Valid {
			u.Name = name.String
		}
		if picture.Valid {
			u.Picture = picture.String
		}
		if nextRun.Valid {
			u.NextRunAt = &nextRun.Time
		}
		u.Keywords = splitKeywords(kw)
		users = append(users, &u)
	}
	return users, nil
}

func splitKeywords(s string) []string {
	var keywords []string
	for _, kw := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(kw); trimmed != "" {
			keywords = append(keywords, trimmed)
		}
	}
	return keywords
}
