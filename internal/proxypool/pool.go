// Package proxypool manages a file-backed SOCKS5/HTTP proxy node list and
// stable account→proxy assignment for Build anti-ban (account sticky = egress sticky).
package proxypool

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AssignMode controls how empty-proxy accounts pick a node.
const (
	AssignHash         = "hash"          // stable by accountID
	AssignLeastAccounts = "least_accounts"
)

// Node is one egress endpoint.
type Node struct {
	ID               string `json:"id"`
	URL              string `json:"url"`
	Enabled          bool   `json:"enabled"`
	Weight           int    `json:"weight,omitempty"`
	FailCount        int    `json:"fail_count,omitempty"`
	CooldownUntil    int64  `json:"cooldown_until,omitempty"` // unix sec
	LastError        string `json:"last_error,omitempty"`
	AssignedAccounts int    `json:"assigned_accounts,omitempty"` // soft counter
}

// File is the on-disk document.
type File struct {
	Nodes []Node `json:"nodes"`
}

// Pool is a concurrency-safe proxy pool.
type Pool struct {
	mu   sync.Mutex
	path string
	doc  File
}

// Open loads path (missing file → empty pool).
func Open(path string) (*Pool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("proxypool: empty path")
	}
	p := &Pool{path: path}
	if err := p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Pool) load() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			p.doc = File{Nodes: []Node{}}
			return nil
		}
		return fmt.Errorf("proxypool: read %s: %w", p.path, err)
	}
	var doc File
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("proxypool: parse %s: %w", p.path, err)
	}
	if doc.Nodes == nil {
		doc.Nodes = []Node{}
	}
	for i := range doc.Nodes {
		normalizeNode(&doc.Nodes[i])
	}
	p.doc = doc
	return nil
}

func normalizeNode(n *Node) {
	n.ID = strings.TrimSpace(n.ID)
	n.URL = strings.TrimSpace(n.URL)
	if n.Weight <= 0 {
		n.Weight = 1
	}
	if n.ID == "" && n.URL != "" {
		n.ID = shortID(n.URL)
	}
}

func shortID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("p_%x", sum[:4])
}

// Path returns the JSON path.
func (p *Pool) Path() string {
	if p == nil {
		return ""
	}
	return p.path
}

// Snapshot returns a copy of nodes.
func (p *Pool) Snapshot() []Node {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Node, len(p.doc.Nodes))
	copy(out, p.doc.Nodes)
	return out
}

// ReplaceAll validates and atomically replaces the node list.
func (p *Pool) ReplaceAll(nodes []Node) error {
	if p == nil {
		return fmt.Errorf("proxypool: nil")
	}
	clean := make([]Node, 0, len(nodes))
	seen := map[string]struct{}{}
	for _, n := range nodes {
		normalizeNode(&n)
		if n.URL == "" {
			continue
		}
		if err := ValidateURL(n.URL); err != nil {
			return err
		}
		if n.ID == "" {
			n.ID = shortID(n.URL)
		}
		if _, ok := seen[n.ID]; ok {
			return fmt.Errorf("proxypool: duplicate id %q", n.ID)
		}
		seen[n.ID] = struct{}{}
		// preserve runtime health if same id+url
		p.mu.Lock()
		for _, old := range p.doc.Nodes {
			if old.ID == n.ID && old.URL == n.URL {
				n.FailCount = old.FailCount
				n.CooldownUntil = old.CooldownUntil
				n.LastError = old.LastError
				n.AssignedAccounts = old.AssignedAccounts
				break
			}
		}
		p.mu.Unlock()
		clean = append(clean, n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.doc.Nodes = clean
	return p.persistLocked()
}

// ValidateURL accepts http/https/socks5/socks5h.
func ValidateURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("proxypool: bad url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("proxypool: unsupported scheme %q (use http/https/socks5/socks5h)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("proxypool: url missing host")
	}
	return nil
}

// ModeFromURL derives proxy_mode for catalog.
func ModeFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		return "socks5"
	case "https":
		return "https"
	default:
		return "http"
	}
}

func (p *Pool) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p.doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".proxy-pool-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, p.path); err != nil {
		return err
	}
	ok = true
	return nil
}

func (p *Pool) healthyLocked(now int64) []int {
	idxs := make([]int, 0, len(p.doc.Nodes))
	for i, n := range p.doc.Nodes {
		if !n.Enabled || n.URL == "" {
			continue
		}
		if n.CooldownUntil > now {
			continue
		}
		idxs = append(idxs, i)
	}
	return idxs
}

// Pick returns a proxy URL for accountID (empty if none healthy).
func (p *Pool) Pick(accountID, mode string) (proxyURL, proxyMode string, ok bool) {
	if p == nil {
		return "", "", false
	}
	accountID = strings.TrimSpace(accountID)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = AssignHash
	}
	now := time.Now().Unix()
	p.mu.Lock()
	defer p.mu.Unlock()
	idxs := p.healthyLocked(now)
	if len(idxs) == 0 {
		return "", "", false
	}
	var pick int
	switch mode {
	case AssignLeastAccounts:
		pick = idxs[0]
		best := p.doc.Nodes[pick].AssignedAccounts
		for _, i := range idxs[1:] {
			if p.doc.Nodes[i].AssignedAccounts < best {
				best = p.doc.Nodes[i].AssignedAccounts
				pick = i
			}
		}
	default: // hash
		h := sha256.Sum256([]byte(accountID))
		n := binary.BigEndian.Uint64(h[:8])
		pick = idxs[int(n%uint64(len(idxs)))]
	}
	node := &p.doc.Nodes[pick]
	node.AssignedAccounts++
	_ = p.persistLocked()
	return node.URL, ModeFromURL(node.URL), true
}

// MarkFail cools down the node matching proxyURL.
func (p *Pool) MarkFail(proxyURL, errMsg string) {
	if p == nil {
		return
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return
	}
	now := time.Now().Unix()
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.doc.Nodes {
		if p.doc.Nodes[i].URL != proxyURL {
			continue
		}
		p.doc.Nodes[i].FailCount++
		p.doc.Nodes[i].LastError = strings.TrimSpace(errMsg)
		// 30s * 2^min(fail-1, 4), cap 15m
		exp := p.doc.Nodes[i].FailCount - 1
		if exp < 0 {
			exp = 0
		}
		if exp > 4 {
			exp = 4
		}
		sec := int64(30) << exp
		if sec > 900 {
			sec = 900
		}
		p.doc.Nodes[i].CooldownUntil = now + sec
		_ = p.persistLocked()
		return
	}
}

// MarkOK decays failure state for a node.
func (p *Pool) MarkOK(proxyURL string) {
	if p == nil {
		return
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.doc.Nodes {
		if p.doc.Nodes[i].URL != proxyURL {
			continue
		}
		if p.doc.Nodes[i].FailCount > 0 {
			p.doc.Nodes[i].FailCount--
		}
		p.doc.Nodes[i].LastError = ""
		if p.doc.Nodes[i].FailCount == 0 {
			p.doc.Nodes[i].CooldownUntil = 0
		}
		_ = p.persistLocked()
		return
	}
}

// HealthyCount returns currently assignable nodes.
func (p *Pool) HealthyCount() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.healthyLocked(time.Now().Unix()))
}
