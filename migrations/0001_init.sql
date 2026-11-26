-- Docktimer initial schema.
-- All timestamps are stored as UTC RFC3339 strings with millisecond precision.

PRAGMA foreign_keys = ON;

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('ADMIN','VIEWER')),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE TABLE sessions (
    id           TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at   TEXT    NOT NULL,
    created_at   TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL,
    user_agent   TEXT    NOT NULL DEFAULT '',
    ip           TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user    ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE TABLE container_rules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id    TEXT    NOT NULL,
    container_name  TEXT    NOT NULL,
    action          TEXT    NOT NULL CHECK (action IN ('START','STOP','RESTART')),
    cron_expression TEXT    NOT NULL,
    timezone        TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);
CREATE INDEX idx_rules_enabled   ON container_rules(enabled);
CREATE INDEX idx_rules_container ON container_rules(container_id);

CREATE TABLE execution_logs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id        INTEGER REFERENCES container_rules(id) ON DELETE SET NULL,
    container_id   TEXT    NOT NULL,
    container_name TEXT    NOT NULL,
    action         TEXT    NOT NULL CHECK (action IN ('START','STOP','RESTART')),
    trigger        TEXT    NOT NULL CHECK (trigger IN ('SCHEDULE','MANUAL')),
    status         TEXT    NOT NULL CHECK (status IN ('SUCCESS','FAILED')),
    message        TEXT    NOT NULL DEFAULT '',
    duration_ms    INTEGER NOT NULL CHECK (duration_ms >= 0),
    created_at     TEXT    NOT NULL
);
CREATE INDEX idx_logs_created           ON execution_logs(created_at DESC, id DESC);
CREATE INDEX idx_logs_container_created ON execution_logs(container_id, created_at DESC);
CREATE INDEX idx_logs_status_created    ON execution_logs(status, created_at DESC);
CREATE INDEX idx_logs_rule              ON execution_logs(rule_id);
