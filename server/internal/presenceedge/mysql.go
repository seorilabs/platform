package presenceedge

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

type MySQLRepository struct {
	db *sql.DB
}

func OpenMySQL(databaseURL string) (*MySQLRepository, error) {
	dsn, err := mysqlDSN(databaseURL)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("presence edge: MySQL 열기 실패: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)
	return &MySQLRepository{db: db}, nil
}

func (r *MySQLRepository) Close() error { return r.db.Close() }

func (r *MySQLRepository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

func (r *MySQLRepository) Upsert(ctx context.Context, session Session) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO platform_presence_session
  (app_id, session_hash, platform, app_version, last_sequence, last_seen_at, expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  platform = VALUES(platform),
  app_version = VALUES(app_version),
  last_sequence = GREATEST(last_sequence, VALUES(last_sequence)),
  last_seen_at = VALUES(last_seen_at),
  expires_at = VALUES(expires_at),
  updated_at = VALUES(updated_at)`,
		session.AppID,
		session.SessionHash,
		session.Platform,
		session.AppVersion,
		session.Sequence,
		session.LastSeenAt,
		session.ExpiresAt,
		session.LastSeenAt,
		session.LastSeenAt,
	)
	if err != nil {
		return fmt.Errorf("presence edge: session upsert 실패: %w", err)
	}
	return nil
}

func (r *MySQLRepository) Cleanup(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 10_000 {
		limit = 1000
	}
	result, err := r.db.ExecContext(ctx,
		"DELETE FROM platform_presence_session WHERE expires_at < ? LIMIT ?", before, limit)
	if err != nil {
		return 0, fmt.Errorf("presence edge: 만료 session 정리 실패: %w", err)
	}
	return result.RowsAffected()
}

func mysqlDSN(databaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(databaseURL))
	if err != nil || parsed.Scheme != "mysql" || parsed.Host == "" || parsed.User == nil {
		return "", fmt.Errorf("presence edge: PRESENCE_DATABASE_URL 형식이 올바르지 않다")
	}
	password, _ := parsed.User.Password()
	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("presence edge: MySQL database 이름이 올바르지 않다")
	}
	config := mysql.NewConfig()
	config.User = parsed.User.Username()
	config.Passwd = password
	config.Net = "tcp"
	config.Addr = parsed.Host
	config.DBName = name
	config.ParseTime = true
	config.Loc = time.UTC
	config.Timeout = time.Second
	config.ReadTimeout = time.Second
	config.WriteTimeout = time.Second
	config.Params = map[string]string{"charset": "utf8mb4"}
	return config.FormatDSN(), nil
}
