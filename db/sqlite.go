package db

import (
	"database/sql"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteDB struct {
	Conn *sql.DB
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
	db, err := sql.Open("sqlite", filepath)
	if err != nil {
		return nil, err
	}

	// Create tables if missing
	createTableQuery := `
	CREATE TABLE IF NOT EXISTS erp_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		tenant_id TEXT,
		user_id TEXT,
		action TEXT,
		details TEXT,
		timestamp DATETIME
	);
	`
	_, err = db.Exec(createTableQuery)
	if err != nil {
		return nil, err
	}

	return &SQLiteDB{Conn: db}, nil
}

func (s *SQLiteDB) Log(tenantID, userID, action, details string) {
	query := `INSERT INTO erp_logs (tenant_id, user_id, action, details, timestamp) VALUES (?, ?, ?, ?, ?)`
	_, err := s.Conn.Exec(query, tenantID, userID, action, details, time.Now())
	if err != nil {
		log.Printf("Warning: Failed to insert SQLite log: %v", err)
	}
}

func (s *SQLiteDB) GetLogs() ([]DBLog, error) {
	rows, err := s.Conn.Query(`SELECT id, tenant_id, user_id, action, details, timestamp FROM erp_logs ORDER BY id DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []DBLog
	for rows.Next() {
		var l DBLog
		var tsStr string
		err := rows.Scan(&l.ID, &l.TenantID, &l.UserID, &l.Action, &l.Details, &tsStr)
		if err != nil {
			return nil, err
		}
		// Parse timestamp string safely
		parsed, err := time.Parse("2006-01-02T15:04:05Z07:00", tsStr)
		if err == nil {
			l.Timestamp = parsed
		} else {
			parsed, err = time.Parse("2006-01-02 15:04:05 -0700 MST", tsStr)
			if err == nil {
				l.Timestamp = parsed
			} else {
				parsed, err = time.Parse(time.RFC3339, tsStr)
				if err == nil {
					l.Timestamp = parsed
				} else {
					l.Timestamp = time.Now()
				}
			}
		}
		logs = append(logs, l)
	}
	return logs, nil
}
