package db

import (
	"context"
	"log"
	"time"


	"github.com/jackc/pgx/v5/pgxpool"
)

type SQLiteDB struct {
	Pool *pgxpool.Pool
}

type DBLog struct {
	ID        int       `json:"id"`
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	Action    string    `json:"action"`
	Details   string    `json:"details"`
	Timestamp time.Time `json:"timestamp"`
}

func InitSQLite(filepath string) (*SQLiteDB, error) {
	// Dummy implementation to preserve signature, main.go sets the Pool right after
	return &SQLiteDB{}, nil
}

func (s *SQLiteDB) Log(tenantID, userID, action, details string) {
	if s.Pool == nil {
		log.Printf("Warning: Postgres pool not set in SQLiteDB logger")
		return
	}
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		log.Printf("Warning: Failed to start transaction for Postgres log: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant_id', $1, true)", tenantID)

	_, err = tx.Exec(ctx, `
		INSERT INTO erp_logs (tenant_id, user_id, action, details, created_at)
		VALUES ($1, $2, $3, $4, NOW())`,
		tenantID, userID, action, details)
	if err != nil {
		log.Printf("Warning: Failed to insert Postgres log: %v", err)
		return
	}

	_ = tx.Commit(ctx)
}

func (s *SQLiteDB) GetLogs() ([]DBLog, error) {
	if s.Pool == nil {
		return nil, nil
	}
	ctx := context.Background()
	rows, err := s.Pool.Query(ctx, "SELECT id, tenant_id, user_id, action, details, created_at FROM erp_logs ORDER BY id DESC LIMIT 50")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []DBLog
	for rows.Next() {
		var l DBLog
		err := rows.Scan(&l.ID, &l.TenantID, &l.UserID, &l.Action, &l.Details, &l.Timestamp)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

func (s *SQLiteDB) GetLogsForTenant(tenantID string) ([]DBLog, error) {
	if s.Pool == nil {
		return nil, nil
	}
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant_id', $1, true)", tenantID)

	rows, err := tx.Query(ctx, "SELECT id, tenant_id, user_id, action, details, created_at FROM erp_logs WHERE tenant_id = $1 ORDER BY id DESC LIMIT 50", tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []DBLog
	for rows.Next() {
		var l DBLog
		err := rows.Scan(&l.ID, &l.TenantID, &l.UserID, &l.Action, &l.Details, &l.Timestamp)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	_ = tx.Commit(ctx)
	return logs, nil
}
