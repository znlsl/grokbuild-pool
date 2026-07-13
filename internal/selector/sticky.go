package selector

import (
	"container/list"
	"sync"
)

// stickyEntry 为 stickyKey → accountID 绑定，含绝对过期时间（unix 秒）。
type stickyEntry struct {
	key       string
	accountID string
	expiresAt int64
}

// stickyLRU 为带每条目 TTL 的并发安全粘性绑定 LRU。
type stickyLRU struct {
	mu      sync.Mutex
	max     int
	ttlSec  int64
	ll      *list.List               // 队首为最近使用
	items   map[string]*list.Element // stickyKey → 链表元素
	byAcct  map[string]map[string]struct{} // accountID → stickyKey 集合
}

func newStickyLRU(max int, ttlSec int64) *stickyLRU {
	if max <= 0 {
		max = DefaultStickyMax
	}
	if ttlSec <= 0 {
		ttlSec = DefaultStickyTTLSec
	}
	return &stickyLRU{
		max:    max,
		ttlSec: ttlSec,
		ll:     list.New(),
		items:  make(map[string]*list.Element, min(max, 1024)),
		byAcct: make(map[string]map[string]struct{}),
	}
}

// get 在 now 未过期时返回绑定的 accountID。
// 命中会移到 MRU 并刷新 expiresAt。
func (s *stickyLRU) get(now int64, key string) (accountID string, ok bool) {
	if key == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	el, found := s.items[key]
	if !found {
		return "", false
	}
	e := el.Value.(*stickyEntry)
	if e.expiresAt > 0 && e.expiresAt <= now {
		s.removeElement(el)
		return "", false
	}
	// 刷新 TTL 并移到 MRU
	e.expiresAt = now + s.ttlSec
	s.ll.MoveToFront(el)
	return e.accountID, true
}

// put 在 now 绑定 key → accountID（创建或更新）。
func (s *stickyLRU) put(now int64, key, accountID string) {
	if key == "" || accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, found := s.items[key]; found {
		e := el.Value.(*stickyEntry)
		if e.accountID != accountID {
			s.unindexAccount(e.accountID, key)
			e.accountID = accountID
			s.indexAccount(accountID, key)
		}
		e.expiresAt = now + s.ttlSec
		s.ll.MoveToFront(el)
		return
	}
	// 淘汰至容量内
	for s.ll.Len() >= s.max {
		oldest := s.ll.Back()
		if oldest == nil {
			break
		}
		s.removeElement(oldest)
	}
	e := &stickyEntry{
		key:       key,
		accountID: accountID,
		expiresAt: now + s.ttlSec,
	}
	el := s.ll.PushFront(e)
	s.items[key] = el
	s.indexAccount(accountID, key)
}

// deleteKey 按 key 删除粘性绑定。
func (s *stickyLRU) deleteKey(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		s.removeElement(el)
	}
}

// deleteAccount 删除所有指向 accountID 的粘性绑定。
func (s *stickyLRU) deleteAccount(accountID string) {
	if accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, ok := s.byAcct[accountID]
	if !ok {
		return
	}
	// 拷贝 keys——removeElement 会改 byAcct
	toDel := make([]string, 0, len(keys))
	for k := range keys {
		toDel = append(toDel, k)
	}
	for _, k := range toDel {
		if el, ok := s.items[k]; ok {
			s.removeElement(el)
		}
	}
}

func (s *stickyLRU) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ll.Len()
}

func (s *stickyLRU) removeElement(el *list.Element) {
	e := el.Value.(*stickyEntry)
	s.ll.Remove(el)
	delete(s.items, e.key)
	s.unindexAccount(e.accountID, e.key)
}

func (s *stickyLRU) indexAccount(accountID, key string) {
	m, ok := s.byAcct[accountID]
	if !ok {
		m = make(map[string]struct{})
		s.byAcct[accountID] = m
	}
	m[key] = struct{}{}
}

func (s *stickyLRU) unindexAccount(accountID, key string) {
	m, ok := s.byAcct[accountID]
	if !ok {
		return
	}
	delete(m, key)
	if len(m) == 0 {
		delete(s.byAcct, accountID)
	}
}
