package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// ConnectionTool 是连接下的一个工具。
type ConnectionTool struct {
	AssetID     int64           `json:"asset_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	// OpenAPI 导入的工具才有：调用时用来拼请求
	Operation json.RawMessage `json:"operation,omitempty"`
	ReadOnly  bool            `json:"read_only"`
	Enabled   bool            `json:"enabled"`
	// 额外限定的角色；空表示连接可用范围内的人都能用
	Roles     []int64   `json:"roles"`
	UpdatedAt time.Time `json:"updated_at"`
}

const connToolCols = "asset_id, name, description, input_schema, operation, read_only, enabled, roles, updated_at"

func scanConnTool(row interface{ Scan(...any) error }) (*ConnectionTool, error) {
	var t ConnectionTool
	var schema string
	var op, roles sql.NullString
	if err := row.Scan(&t.AssetID, &t.Name, &t.Description, &schema, &op, &t.ReadOnly, &t.Enabled, &roles, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.InputSchema = json.RawMessage(schema)
	if op.Valid && op.String != "" {
		t.Operation = json.RawMessage(op.String)
	}
	t.Roles = []int64{}
	if roles.Valid && roles.String != "" {
		_ = json.Unmarshal([]byte(roles.String), &t.Roles)
	}
	return &t, nil
}

// ConnectionTools 列出连接的工具；assetIDs 为空表示全部连接。
func (s *Store) ConnectionTools(ctx context.Context, assetIDs ...int64) ([]*ConnectionTool, error) {
	q := "SELECT " + connToolCols + " FROM connection_tools"
	var args []any
	if len(assetIDs) > 0 {
		q += " WHERE asset_id IN (" + placeholders(len(assetIDs)) + ")"
		for _, id := range assetIDs {
			args = append(args, id)
		}
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY asset_id, name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ConnectionTool{}
	for rows.Next() {
		t, err := scanConnTool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReplaceConnectionTools 用新发现的工具替换清单。已存在的同名工具保留管理员设置的启用状态与角色限定；
// 新出现的工具按 defaultEnabled 决定是否启用（OpenAPI 的写操作默认关闭）。
func (s *Store) ReplaceConnectionTools(ctx context.Context, assetID int64, tools []*ConnectionTool) error {
	existing, err := s.ConnectionTools(ctx, assetID)
	if err != nil {
		return err
	}
	keep := map[string]*ConnectionTool{}
	for _, t := range existing {
		keep[t.Name] = t
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM connection_tools WHERE asset_id = ?", assetID); err != nil {
		return err
	}
	t0 := now()
	for _, t := range tools {
		if old := keep[t.Name]; old != nil {
			// 原来只读、现在变成了写操作，而写操作默认不启用的：按新工具对待，等管理员重新打开
			demoted := old.ReadOnly && !t.ReadOnly && !t.Enabled
			t.Enabled, t.Roles = old.Enabled && !demoted, old.Roles
		}
		roles, _ := json.Marshal(nonNilIDs(t.Roles))
		var op any
		if len(t.Operation) > 0 {
			op = string(t.Operation)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO connection_tools (asset_id, name, description, input_schema, operation, read_only, enabled, roles, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			assetID, t.Name, t.Description, string(t.InputSchema), op, t.ReadOnly, t.Enabled, string(roles), t0); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nonNilIDs(xs []int64) []int64 {
	if xs == nil {
		return []int64{}
	}
	return xs
}

// SetConnectionTool 修改一个工具的启用状态与角色限定。
func (s *Store) SetConnectionTool(ctx context.Context, assetID int64, name string, enabled bool, roles []int64) error {
	b, _ := json.Marshal(nonNilIDs(roles))
	res, err := s.db.ExecContext(ctx, "UPDATE connection_tools SET enabled = ?, roles = ?, updated_at = ? WHERE asset_id = ? AND name = ?",
		enabled, string(b), now(), assetID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type ConnectionCall struct {
	AssetID   int64
	Tool      string
	UserID    int64
	OK        bool
	Error     string
	LatencyMS int
	// 这次调用带的员工身份：空（只有共享凭据）/ ee_jwt / user_secret
	AuthMode string
	// 连接是扩展装出来的：扩展的「身份@版本」和清单的哈希
	Extension string
	Digest    string
}

func (s *Store) RecordConnectionCall(ctx context.Context, c ConnectionCall) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO connection_calls (asset_id, tool, user_id, ok, error, latency_ms, auth_mode, extension, digest, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.AssetID, c.Tool, c.UserID, c.OK, truncateRunes(c.Error, 512), c.LatencyMS, c.AuthMode, c.Extension, c.Digest, now())
	return err
}

// RecentCall 是连接调用日志的一条，管理页展示。
type RecentCall struct {
	Tool      string    `json:"tool"`
	Username  string    `json:"username"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error"`
	LatencyMS int       `json:"latency_ms"`
	AuthMode  string    `json:"auth_mode,omitempty"`
	Extension string    `json:"extension,omitempty"`
	Digest    string    `json:"digest,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) RecentConnectionCalls(ctx context.Context, assetID int64, limit int) ([]RecentCall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.tool, COALESCE(u.username, ''), c.ok, c.error, c.latency_ms, c.auth_mode, c.extension, c.digest, c.created_at
		FROM connection_calls c LEFT JOIN users u ON u.id = c.user_id
		WHERE c.asset_id = ? ORDER BY c.id DESC LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecentCall{}
	for rows.Next() {
		var c RecentCall
		if err := rows.Scan(&c.Tool, &c.Username, &c.OK, &c.Error, &c.LatencyMS, &c.AuthMode, &c.Extension, &c.Digest, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
