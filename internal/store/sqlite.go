package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	slog.Info("sqlite store ready", "path", path)
	return &Store{db: db}, nil
}

func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

func migrate(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS paper_fills (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		order_id TEXT NOT NULL,
		token_id TEXT NOT NULL,
		side TEXT NOT NULL,
		price REAL NOT NULL,
		size REAL NOT NULL,
		filled_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS pnl_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		realized REAL NOT NULL DEFAULT 0,
		unrealized REAL NOT NULL DEFAULT 0,
		timestamp INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_fills_token ON paper_fills(token_id);
	CREATE INDEX IF NOT EXISTS idx_fills_time ON paper_fills(filled_at);
	CREATE INDEX IF NOT EXISTS idx_pnl_time ON pnl_snapshots(timestamp);
	`

	_, err := db.Exec(schema)
	return err
}

func (s *Store) RecordFill(orderID, tokenID, side string, price, size float64) error {
	_, err := s.db.Exec(
		"INSERT INTO paper_fills (order_id, token_id, side, price, size, filled_at) VALUES (?, ?, ?, ?, ?, ?)",
		orderID, tokenID, side, price, size, time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) RecentFills(limit int) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(
		"SELECT order_id, token_id, side, price, size, filled_at FROM paper_fills ORDER BY filled_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fills []map[string]interface{}
	for rows.Next() {
		var orderID, tokenID, side string
		var price, size float64
		var ts int64
		if err := rows.Scan(&orderID, &tokenID, &side, &price, &size, &ts); err != nil {
			continue
		}
		fills = append(fills, map[string]interface{}{
			"order_id": orderID,
			"token_id": tokenID,
			"side":     side,
			"price":    price,
			"size":     size,
			"time":     time.UnixMilli(ts).Format(time.RFC3339),
		})
	}
	return fills, nil
}

func (s *Store) RecordPnL(realized, unrealized float64) error {
	_, err := s.db.Exec(
		"INSERT INTO pnl_snapshots (realized, unrealized, timestamp) VALUES (?, ?, ?)",
		realized, unrealized, time.Now().UnixMilli(),
	)
	return err
}

func (s *Store) TotalRealizedPnL() (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRow("SELECT SUM(size * price) FROM paper_fills WHERE side = 'SELL'").Scan(&total)
	if err != nil {
		return 0, err
	}
	var buyTotal sql.NullFloat64
	_ = s.db.QueryRow("SELECT SUM(size * price) FROM paper_fills WHERE side = 'BUY'").Scan(&buyTotal)
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}
