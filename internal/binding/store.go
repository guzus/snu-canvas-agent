package binding

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	_ "github.com/lib/pq"
)

type Store struct {
	db *sql.DB
}

type SentAlert struct {
	DedupeKey string
	AlertType string
	CourseID  *int
	EntityID  *int64
	Metadata  map[string]any
}

func New(databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema tx: %w", err)
	}
	defer tx.Rollback()

	// Avoid concurrent CREATE TABLE races during rolling deploy/startup.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(781234567890123)`); err != nil {
		return fmt.Errorf("acquire schema lock: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS telegram_bindings (
  canvas_api_key TEXT PRIMARY KEY,
  chat_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`)
	if err != nil {
		return fmt.Errorf("ensure schema: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS telegram_chat_settings (
  chat_id TEXT PRIMARY KEY,
  lang TEXT NOT NULL DEFAULT 'ko',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`)
	if err != nil {
		return fmt.Errorf("ensure chat settings schema: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS telegram_chat_courses (
  chat_id TEXT NOT NULL,
  course_id INTEGER NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (chat_id, course_id)
)`)
	if err != nil {
		return fmt.Errorf("ensure chat courses schema: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_telegram_chat_courses_chat_id
ON telegram_chat_courses (chat_id)`)
	if err != nil {
		return fmt.Errorf("ensure chat courses index: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS telegram_sent_alerts (
  chat_id TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  alert_type TEXT NOT NULL,
  course_id INTEGER NULL,
  entity_id BIGINT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (chat_id, dedupe_key)
)`)
	if err != nil {
		return fmt.Errorf("ensure sent alerts schema: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_telegram_sent_alerts_chat_sent_at
ON telegram_sent_alerts (chat_id, sent_at DESC)`)
	if err != nil {
		return fmt.Errorf("ensure sent alerts index: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema tx: %w", err)
	}
	return nil
}

func (s *Store) Upsert(ctx context.Context, canvasAPIKey, chatID string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO telegram_bindings (canvas_api_key, chat_id)
VALUES ($1, $2)
ON CONFLICT (canvas_api_key)
DO UPDATE SET
  chat_id = EXCLUDED.chat_id,
  updated_at = NOW()
`, canvasAPIKey, chatID)
	if err != nil {
		return fmt.Errorf("upsert binding: %w", err)
	}
	return nil
}

func (s *Store) LookupChatID(ctx context.Context, canvasAPIKey string) (string, error) {
	var chatID string
	err := s.db.QueryRowContext(ctx, `
SELECT chat_id
FROM telegram_bindings
WHERE canvas_api_key = $1
`, canvasAPIKey).Scan(&chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup chat id: %w", err)
	}
	return chatID, nil
}

func (s *Store) SetChatLanguage(ctx context.Context, chatID, lang string) error {
	if lang != "ko" && lang != "en" {
		return fmt.Errorf("unsupported language: %s", lang)
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO telegram_chat_settings (chat_id, lang)
VALUES ($1, $2)
ON CONFLICT (chat_id)
DO UPDATE SET
  lang = EXCLUDED.lang,
  updated_at = NOW()
`, chatID, lang)
	if err != nil {
		return fmt.Errorf("set chat language: %w", err)
	}
	return nil
}

func (s *Store) GetChatLanguage(ctx context.Context, chatID string) (string, error) {
	var lang string
	err := s.db.QueryRowContext(ctx, `
SELECT lang
FROM telegram_chat_settings
WHERE chat_id = $1
`, chatID).Scan(&lang)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get chat language: %w", err)
	}
	return lang, nil
}

func (s *Store) AddChatCourse(ctx context.Context, chatID string, courseID int) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO telegram_chat_courses (chat_id, course_id)
VALUES ($1, $2)
ON CONFLICT (chat_id, course_id)
DO UPDATE SET
  updated_at = NOW()
`, chatID, courseID)
	if err != nil {
		return fmt.Errorf("add chat course: %w", err)
	}
	return nil
}

func (s *Store) RemoveChatCourse(ctx context.Context, chatID string, courseID int) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM telegram_chat_courses
WHERE chat_id = $1 AND course_id = $2
`, chatID, courseID)
	if err != nil {
		return fmt.Errorf("remove chat course: %w", err)
	}
	return nil
}

func (s *Store) ListChatCourses(ctx context.Context, chatID string) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT course_id
FROM telegram_chat_courses
WHERE chat_id = $1
`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list chat courses: %w", err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan chat course: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat courses: %w", err)
	}
	sort.Ints(ids)
	return ids, nil
}

func (s *Store) InsertSentAlertIfNew(ctx context.Context, chatID string, alert SentAlert) (bool, error) {
	if chatID == "" {
		return false, errors.New("empty chatID")
	}
	if alert.DedupeKey == "" {
		return false, errors.New("empty dedupe key")
	}
	if alert.AlertType == "" {
		return false, errors.New("empty alert type")
	}

	metadata := alert.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return false, fmt.Errorf("marshal alert metadata: %w", err)
	}

	var courseID any
	if alert.CourseID != nil {
		courseID = *alert.CourseID
	}
	var entityID any
	if alert.EntityID != nil {
		entityID = *alert.EntityID
	}

	res, err := s.db.ExecContext(ctx, `
INSERT INTO telegram_sent_alerts (chat_id, dedupe_key, alert_type, course_id, entity_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6::jsonb)
ON CONFLICT (chat_id, dedupe_key) DO NOTHING
`, chatID, alert.DedupeKey, alert.AlertType, courseID, entityID, string(metadataJSON))
	if err != nil {
		return false, fmt.Errorf("insert sent alert: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sent alert rows affected: %w", err)
	}
	return n > 0, nil
}

func (s *Store) DeleteSentAlert(ctx context.Context, chatID, dedupeKey string) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM telegram_sent_alerts
WHERE chat_id = $1 AND dedupe_key = $2
`, chatID, dedupeKey)
	if err != nil {
		return fmt.Errorf("delete sent alert: %w", err)
	}
	return nil
}
