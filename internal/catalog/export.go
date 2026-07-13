package catalog

import (
	"fmt"
	"strings"
)

// ExportAccount 导出用账号（含 token，供备份/迁移；仅 admin 导出接口返回）。
type ExportAccount struct {
	ID           string `json:"id"`
	Email        string `json:"email,omitempty"`
	Name         string `json:"name,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	ProxyMode    string `json:"proxy_mode,omitempty"`
	Priority     int    `json:"priority,omitempty"`
	Enabled      bool   `json:"enabled"`
	Lifecycle    string `json:"lifecycle,omitempty"`
	CreatedAt    int64  `json:"created_at,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
}

// ListExportAccounts 按 id 升序游标分页导出完整账号（含 token）。
// afterID 空为首页；limit 上限 2000。
func (c *Catalog) ListExportAccounts(limit int, afterID string) ([]ExportAccount, error) {
	if c.db == nil {
		return nil, ErrClosed
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	afterID = strings.TrimSpace(afterID)
	const q = `
SELECT
  id,
  COALESCE(email, ''),
  COALESCE(name, ''),
  COALESCE(access_token, ''),
  COALESCE(refresh_token, ''),
  expires_at,
  COALESCE(proxy_url, ''),
  COALESCE(proxy_mode, ''),
  priority,
  enabled,
  lifecycle,
  created_at,
  updated_at
FROM accounts
WHERE (? = '' OR id > ?)
ORDER BY id ASC
LIMIT ?`
	rows, err := c.db.Query(q, afterID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("catalog: export list: %w", err)
	}
	defer rows.Close()
	out := make([]ExportAccount, 0, limit)
	for rows.Next() {
		var a ExportAccount
		var en int
		if err := rows.Scan(
			&a.ID, &a.Email, &a.Name, &a.AccessToken, &a.RefreshToken, &a.ExpiresAt,
			&a.ProxyURL, &a.ProxyMode, &a.Priority, &en, &a.Lifecycle, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("catalog: export scan: %w", err)
		}
		a.Enabled = en != 0
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CountAccounts 返回账号总数（导出进度用）。
func (c *Catalog) CountAccounts() (int, error) {
	if c.db == nil {
		return 0, ErrClosed
	}
	var n int
	err := c.db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}
