package binding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/lib/pq"
)

type Store struct {
	db *sql.DB
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
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS telegram_bindings (
  canvas_api_key TEXT PRIMARY KEY,
  chat_id TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`)
	if err != nil {
		return fmt.Errorf("ensure schema: %w", err)
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
