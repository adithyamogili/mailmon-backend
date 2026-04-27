package database

const migrateSQL = `
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    name TEXT,
    picture TEXT,
    telegram_chat_id TEXT DEFAULT '',
    gmail_token TEXT DEFAULT '',
    keywords TEXT DEFAULT '',
    cron_enabled BOOLEAN DEFAULT 0,
    cron_interval_minutes INTEGER DEFAULT 30,
    next_run_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`
