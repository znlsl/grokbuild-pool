package synth

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

// NDJSONAccount 为 gen/import 的磁盘 NDJSON 形态。
// 字段名对工具稳定；令牌仅为合成数据。
type NDJSONAccount struct {
	ID           string `json:"id"`
	Revision     int64  `json:"revision,omitempty"`
	IdentityKey  string `json:"identity_key,omitempty"`
	Email        string `json:"email,omitempty"`
	Name         string `json:"name,omitempty"`
	Priority     int    `json:"priority"`
	Enabled      *bool  `json:"enabled,omitempty"`
	Lifecycle    string `json:"lifecycle,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	ProxyMode    string `json:"proxy_mode,omitempty"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	CreatedAt    int64  `json:"created_at,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
}

// ToAccount 将 NDJSON 转为带默认值的 catalog.Account。
func (n NDJSONAccount) ToAccount() (catalog.Account, error) {
	if n.ID == "" {
		return catalog.Account{}, fmt.Errorf("synth: ndjson missing id")
	}
	if n.AccessToken == "" || n.RefreshToken == "" {
		return catalog.Account{}, fmt.Errorf("synth: ndjson %q missing tokens", n.ID)
	}
	enabled := true
	if n.Enabled != nil {
		enabled = *n.Enabled
	}
	lifecycle := n.Lifecycle
	if lifecycle == "" {
		lifecycle = catalog.LifecycleActive
	}
	now := time.Now().Unix()
	created := n.CreatedAt
	if created == 0 {
		created = now
	}
	updated := n.UpdatedAt
	if updated == 0 {
		updated = now
	}
	rev := n.Revision
	if rev <= 0 {
		rev = 1
	}
	return catalog.Account{
		ID:           n.ID,
		Revision:     rev,
		IdentityKey:  n.IdentityKey,
		Email:        n.Email,
		Name:         n.Name,
		Priority:     n.Priority,
		Enabled:      enabled,
		Lifecycle:    lifecycle,
		AccessToken:  n.AccessToken,
		RefreshToken: n.RefreshToken,
		ExpiresAt:    n.ExpiresAt,
		ProxyMode:    n.ProxyMode,
		ProxyURL:     n.ProxyURL,
		CreatedAt:    created,
		UpdatedAt:    updated,
	}, nil
}

// FromAccount 将 catalog 行映射为 NDJSON（不含健康字段）。
func FromAccount(a catalog.Account) NDJSONAccount {
	en := a.Enabled
	return NDJSONAccount{
		ID:           a.ID,
		Revision:     a.Revision,
		IdentityKey:  a.IdentityKey,
		Email:        a.Email,
		Name:         a.Name,
		Priority:     a.Priority,
		Enabled:      &en,
		Lifecycle:    a.Lifecycle,
		AccessToken:  a.AccessToken,
		RefreshToken: a.RefreshToken,
		ExpiresAt:    a.ExpiresAt,
		ProxyMode:    a.ProxyMode,
		ProxyURL:     a.ProxyURL,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// WriteNDJSON 按每行一个 JSON 对象写入账号。
func WriteNDJSON(w io.Writer, accounts []catalog.Account) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, a := range accounts {
		if err := enc.Encode(FromAccount(a)); err != nil {
			return err
		}
	}
	return nil
}

// WriteNDJSONFile 生成 count 条账号并以 NDJSON 写入 path（模式 0600）。
// 使用流式 AccountAt，避免 count 很大时持有全部行。
func WriteNDJSONFile(path string, opts Options) (int, error) {
	if opts.Count < 0 {
		return 0, fmt.Errorf("synth: count must be >= 0")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	bw := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	// 稳定 Now/Seed 以便确定性流式。
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Seed == 0 {
		opts.Seed = 42
	}
	for i := 0; i < opts.Count; i++ {
		a, err := AccountAt(opts, i)
		if err != nil {
			return i, err
		}
		if err := enc.Encode(FromAccount(a)); err != nil {
			return i, err
		}
	}
	if err := bw.Flush(); err != nil {
		return opts.Count, err
	}
	return opts.Count, f.Close()
}

// ReadNDJSONStream 对每行账号调用 fn；fn 或解码首错即停。
func ReadNDJSONStream(r io.Reader, fn func(catalog.Account) error) (int, error) {
	return ReadNDJSONStreamLimit(r, 1024*1024, 0, fn)
}

// ReadNDJSONStreamLimit 增加单行和总条目上限；maxEntries <= 0 表示无界。
func ReadNDJSONStreamLimit(r io.Reader, maxLineBytes, maxEntries int, fn func(catalog.Account) error) (int, error) {
	if maxLineBytes <= 0 {
		maxLineBytes = 1024 * 1024
	}
	sc := bufio.NewScanner(r)
	maxTokenBytes := maxLineBytes + 1
	bufCap := 64 * 1024
	if maxTokenBytes < bufCap {
		bufCap = maxTokenBytes
	}
	sc.Buffer(make([]byte, 0, bufCap), maxTokenBytes)
	n := 0
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if len(line) > maxLineBytes {
			return n, fmt.Errorf("synth: ndjson line %d exceeds %d bytes", lineNo, maxLineBytes)
		}
		if maxEntries > 0 && n >= maxEntries {
			return n, fmt.Errorf("synth: ndjson entry limit exceeded (max %d)", maxEntries)
		}
		var row NDJSONAccount
		if err := json.Unmarshal(line, &row); err != nil {
			return n, fmt.Errorf("synth: ndjson line %d: invalid JSON", lineNo)
		}
		a, err := row.ToAccount()
		if err != nil {
			return n, fmt.Errorf("synth: ndjson line %d: invalid account", lineNo)
		}
		if err := fn(a); err != nil {
			return n, err
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return n, fmt.Errorf("synth: ndjson line exceeds %d bytes or cannot be read", maxLineBytes)
	}
	return n, nil
}
