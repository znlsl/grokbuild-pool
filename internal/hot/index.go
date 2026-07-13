package hot

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

const (
	// DefaultHotSize 为热集默认目标容量。
	DefaultHotSize = 3000

	// listPageSize 为 LoadEligible 调用 catalog.ListEligible 的分页大小。
	listPageSize = 500
)

// 哨兵错误。
var (
	ErrNotFound     = errors.New("hot: account not found in hot set")
	ErrInvalidInput = errors.New("hot: invalid input")
	ErrClosed       = errors.New("hot: index closed")
)

// Config 控制 Index 容量与可选参数。
type Config struct {
	// HotSize 为热集保留账号的目标上限。
	// 0 表示使用 DefaultHotSize。
	HotSize int
	// MaxInflightPerAccount 单账号 in-flight 硬上限；<=0 表示不硬限（仅靠 selector 打分）。
	// 防封号默认建议 4（对齐 grok2api 量级，略保守）。
	MaxInflightPerAccount int32
}

// Index 是并发安全的 catalog.HotMeta 内存热集（不含密钥）。
//
// Inflight 由本层持有：catalog.ListEligible 恒返回 Inflight=0；
// AddInflight/SubInflight 修改热集中的实时条目。
type Index struct {
	cfg  Config
	mu     sync.RWMutex
	hot    map[string]catalog.HotMeta
	// ids 为热键稠密列表，供选号路径 O(1) 随机采样。
	// pos 为 id -> ids 下标，供 O(1) 交换删除。
	ids    []string
	pos    map[string]int
	cap    int
	closed bool
}

// New 按配置创建空 Index。
func New(cfg Config) *Index {
	size := cfg.HotSize
	if size <= 0 {
		size = DefaultHotSize
	}
	return &Index{
		cfg: cfg,
		hot: make(map[string]catalog.HotMeta, size),
		ids: make([]string, 0, size),
		pos: make(map[string]int, size),
		cap: size,
	}
}


// addLocked 将 id 插入稠密列表。调用方须持写锁；id 必须是新的。
func (idx *Index) addLocked(id string) {
	idx.pos[id] = len(idx.ids)
	idx.ids = append(idx.ids, id)
}

// removeLocked 从稠密列表删除 id（交换删除）。调用方须持写锁。
func (idx *Index) removeLocked(id string) {
	i, ok := idx.pos[id]
	if !ok {
		return
	}
	last := len(idx.ids) - 1
	if i != last {
		moved := idx.ids[last]
		idx.ids[i] = moved
		idx.pos[moved] = i
	}
	idx.ids = idx.ids[:last]
	delete(idx.pos, id)
}

// replaceAllLocked 用 next 重建 hot+ids+pos。调用方须持写锁。
func (idx *Index) replaceAllLocked(next map[string]catalog.HotMeta) {
	idx.hot = next
	idx.ids = make([]string, 0, len(next))
	idx.pos = make(map[string]int, len(next))
	for id := range next {
		idx.pos[id] = len(idx.ids)
		idx.ids = append(idx.ids, id)
	}
}

// Cap 返回配置的热集容量 H。
func (idx *Index) Cap() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.cap
}

// Len 返回当前热集大小。
func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.hot)
}

// Close 标记索引已关闭；之后的写操作返回 ErrClosed。
// 已加载数据的读在进程退出前仍可用。
func (idx *Index) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.closed = true
	return nil
}

// LoadEligible 通过 catalog.ListEligible 重建热集，最多取 Cap() 个账号，
// 顺序为 priority DESC（catalog 排序）。重建时对仍留在热集中的 ID 保留 Inflight。
//
// 返回载入热集的账号数。
func (idx *Index) LoadEligible(c *catalog.Catalog) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("%w: nil catalog", ErrInvalidInput)
	}

	// 保留上一热集中的 inflight。
	prevInflight := make(map[string]int32)
	idx.mu.RLock()
	if idx.closed {
		idx.mu.RUnlock()
		return 0, ErrClosed
	}
	capN := idx.cap
	for id, m := range idx.hot {
		if m.Inflight != 0 {
			prevInflight[id] = m.Inflight
		}
	}
	idx.mu.RUnlock()

	// 分页拉取合格行，直到填满容量或 catalog 耗尽。
	// 剩余量较小时优先大页，使 H=3000 冷启动通常只需一次 ListEligible。
	loaded := make([]catalog.HotMeta, 0, capN)
	afterID := ""
	for len(loaded) < capN {
		need := capN - len(loaded)
		page := need
		if page > listPageSize*4 {
			// 限制单次查询规模，避免 H 极大（如 10k）时一次过大。
			page = listPageSize * 4
		}
		batch, err := c.ListEligible(page, afterID)
		if err != nil {
			return 0, fmt.Errorf("hot: load eligible: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, m := range batch {
			if len(loaded) >= capN {
				break
			}
			if inf, ok := prevInflight[m.ID]; ok {
				m.Inflight = inf
			} else {
				m.Inflight = 0
			}
			loaded = append(loaded, m)
			afterID = m.ID
		}
		if len(batch) < page {
			break
		}
	}

	next := make(map[string]catalog.HotMeta, len(loaded))
	for _, m := range loaded {
		next[m.ID] = m
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return 0, ErrClosed
	}
	idx.replaceAllLocked(next)
	return len(next), nil
}

// LoadMetas 用预构建切片替换热集（测试 / 合成负载）。
// 假定调用方已按优先级排好序；超过 Cap() 的条目按切片顺序丢弃。
// 按 ID 保留上一热集的 Inflight。
func (idx *Index) LoadMetas(metas []catalog.HotMeta) (int, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return 0, ErrClosed
	}

	prevInflight := make(map[string]int32, len(idx.hot))
	for id, m := range idx.hot {
		if m.Inflight != 0 {
			prevInflight[id] = m.Inflight
		}
	}

	limit := idx.cap
	if limit > len(metas) {
		limit = len(metas)
	}
	next := make(map[string]catalog.HotMeta, limit)
	for i := 0; i < limit; i++ {
		m := metas[i]
		if m.ID == "" {
			return 0, fmt.Errorf("%w: empty id in metas[%d]", ErrInvalidInput, i)
		}
		if inf, ok := prevInflight[m.ID]; ok {
			m.Inflight = inf
		} else {
			// 仅保留调用方给出的 inflight；catalog 默认路径为 0
		}
		next[m.ID] = m
	}
	idx.replaceAllLocked(next)
	return len(next), nil
}

// SnapshotHot 返回全部热元数据的副本（顺序为 map 遍历，非确定）。
func (idx *Index) SnapshotHot() []catalog.HotMeta {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]catalog.HotMeta, 0, len(idx.hot))
	for _, m := range idx.hot {
		out = append(out, m)
	}
	return out
}

// Get 返回 id 对应热元数据的副本。
func (idx *Index) Get(id string) (catalog.HotMeta, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	m, ok := idx.hot[id]
	return m, ok
}

// AddInflight 将 id 的 Inflight 加一；不在热集中则返回 ErrNotFound。
func (idx *Index) AddInflight(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidInput)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return ErrClosed
	}
	m, ok := idx.hot[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	m.Inflight++
	idx.hot[id] = m
	return nil
}

// SubInflight 将 id 的 Inflight 减一（下限 0）；不在热集中则返回 ErrNotFound。
func (idx *Index) SubInflight(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidInput)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return ErrClosed
	}
	m, ok := idx.hot[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if m.Inflight > 0 {
		m.Inflight--
	}
	idx.hot[id] = m
	return nil
}

// SetCooldown 设置热条目的 CooldownUntil（unix 秒）。
func (idx *Index) SetCooldown(id string, until int64) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidInput)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return ErrClosed
	}
	m, ok := idx.hot[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	m.CooldownUntil = until
	idx.hot[id] = m
	return nil
}

// Promote 将 meta 插入或更新到热集。
// 若已满且 id 为新账号，先 demote 一个受害者
//（最低 priority；平局时 FailureScore 最高，再 Inflight 最高，再 ID 最大）。
// 更新已在热集中的 id 时保留实时 Inflight（不直接用 meta.Inflight 覆盖）。
//
// 返回被 demote 的账号 id（无则为空）。
func (idx *Index) Promote(meta catalog.HotMeta) (demoted string, err error) {
	if meta.ID == "" {
		return "", fmt.Errorf("%w: empty id", ErrInvalidInput)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return "", ErrClosed
	}

	if existing, ok := idx.hot[meta.ID]; ok {
		// 更新字段但保留实时 inflight。
		meta.Inflight = existing.Inflight
		idx.hot[meta.ID] = meta
		return "", nil
	}

	if len(idx.hot) >= idx.cap {
		victim := pickDemoteVictim(idx.hot)
		if victim == "" {
			return "", fmt.Errorf("hot: cannot promote %s: at capacity and no victim", meta.ID)
		}
		delete(idx.hot, victim)
		idx.removeLocked(victim)
		demoted = victim
	}
	if meta.Inflight < 0 {
		meta.Inflight = 0
	}
	idx.hot[meta.ID] = meta
	idx.addLocked(meta.ID)
	return demoted, nil
}

// Demote 将 id 移出热集；本就不在则成功空操作。
func (idx *Index) Demote(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidInput)
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return ErrClosed
	}
	delete(idx.hot, id)
	idx.removeLocked(id)
	return nil
}

// DemoteMany 批量移出热集（单锁），比循环 Demote 更适合管理台批量禁用/删除。
func (idx *Index) DemoteMany(ids []string) {
	if len(ids) == 0 {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		delete(idx.hot, id)
		idx.removeLocked(id)
	}
}

// Eligible 返回在时刻 now 通过选号过滤的热元数据副本
//（unix 秒；now<=0 时用 time.Now().Unix()）：
//
//	enabled && lifecycle==active && cooldown_until <= now
func (idx *Index) Eligible(now int64) []catalog.HotMeta {
	if now <= 0 {
		now = time.Now().Unix()
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]catalog.HotMeta, 0, len(idx.hot))
	for _, m := range idx.hot {
		if idx.EligibleMeta(m, now) {
			out = append(out, m)
		}
	}
	return out
}

// SampleEligible 在稠密 id 列表上随机探测，采样最多 k 个互不相同的合格热元数据
//（O(k * attempts)，选号路径避免整集拷贝）。
// intn 行为须类似 rand.Rand.Intn（由调用方串行化）。exclude 可为 nil。
// k <= 0 返回 nil。结果顺序未定义。
//
// 保证：随机探测不足 k 个时，线性扫描补齐，小热集（测试）仍能做满 power-of-k。
func (idx *Index) SampleEligible(now int64, k int, exclude map[string]struct{}, intn func(int) int) []catalog.HotMeta {
	if k <= 0 {
		return nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if intn == nil {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	n := len(idx.ids)
	if n == 0 {
		return nil
	}
	if k > n {
		k = n
	}

	picked := make([]catalog.HotMeta, 0, k)
	seen := make(map[string]struct{}, k)

	// 快路径：n 很小时扫全量（对大 n 仍远优于整集 Eligible 拷贝）。
	if n <= k*4 {
		// 对下标做部分 Fisher–Yates，实现无放回采样顺序。
		idxs := make([]int, n)
		for i := range idxs {
			idxs[i] = i
		}
		limit := n
		for i := 0; i < limit && len(picked) < k; i++ {
			j := i + intn(n-i)
			idxs[i], idxs[j] = idxs[j], idxs[i]
			id := idx.ids[idxs[i]]
			if exclude != nil {
				if _, bad := exclude[id]; bad {
					continue
				}
			}
			m, ok := idx.hot[id]
			if !ok || !idx.EligibleMeta(m, now) {
				continue
			}
			picked = append(picked, m)
			seen[id] = struct{}{}
		}
		return picked
	}

	// 大 n：用 seen 做无放回随机探测。
	maxAttempts := k * 16
	if maxAttempts < 32 {
		maxAttempts = 32
	}
	for attempts := 0; attempts < maxAttempts && len(picked) < k; attempts++ {
		id := idx.ids[intn(n)]
		if _, ok := seen[id]; ok {
			continue
		}
		if exclude != nil {
			if _, bad := exclude[id]; bad {
				continue
			}
		}
		m, ok := idx.hot[id]
		if !ok || !idx.EligibleMeta(m, now) {
			continue
		}
		seen[id] = struct{}{}
		picked = append(picked, m)
	}

	// 仍不足时线性补齐（冷却/排除较多时）。
	if len(picked) < k {
		for _, id := range idx.ids {
			if _, ok := seen[id]; ok {
				continue
			}
			if exclude != nil {
				if _, bad := exclude[id]; bad {
					continue
				}
			}
			m, ok := idx.hot[id]
			if !ok || !idx.EligibleMeta(m, now) {
				continue
			}
			seen[id] = struct{}{}
			picked = append(picked, m)
			if len(picked) >= k {
				break
			}
		}
	}
	return picked
}

// IsEligible 报告 m 在 now（unix 秒）是否通过热选号过滤（不含 inflight 硬上限）。
func IsEligible(m catalog.HotMeta, now int64) bool {
	return EligibleWithInflightCap(m, now, 0)
}

// EligibleWithInflightCap 在 IsEligible 基础上：若 maxInflight>0 且 m.Inflight>=max 则不合格。
func EligibleWithInflightCap(m catalog.HotMeta, now int64, maxInflight int32) bool {
	if !m.Enabled {
		return false
	}
	if m.Lifecycle != "" && m.Lifecycle != catalog.LifecycleActive {
		return false
	}
	if m.CooldownUntil > now {
		return false
	}
	if maxInflight > 0 && m.Inflight >= maxInflight {
		return false
	}
	return true
}

// EligibleMeta 使用 Index 配置的 MaxInflightPerAccount 做硬过滤。
func (idx *Index) EligibleMeta(m catalog.HotMeta, now int64) bool {
	var max int32
	if idx != nil {
		max = idx.cfg.MaxInflightPerAccount
	}
	return EligibleWithInflightCap(m, now, max)
}

// MaxInflightPerAccount 返回配置的硬上限。
func (idx *Index) MaxInflightPerAccount() int32 {
	if idx == nil {
		return 0
	}
	return idx.cfg.MaxInflightPerAccount
}

// SetMaxInflightPerAccount 运行时更新单账号并发硬上限（0=不硬限）。
func (idx *Index) SetMaxInflightPerAccount(max int32) {
	if idx == nil {
		return
	}
	idx.mu.Lock()
	idx.cfg.MaxInflightPerAccount = max
	idx.mu.Unlock()
}

// Stats 为运维用的小型快照。
type Stats struct {
	HotSize       int
	Cap           int
	CooldownCount int
	InflightSum   int64
	DisabledCount int
}

// Stats 返回 now 时刻热集上的聚合计数（0 表示当前时间）。
func (idx *Index) Stats(now int64) Stats {
	if now <= 0 {
		now = time.Now().Unix()
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	var s Stats
	s.HotSize = len(idx.hot)
	s.Cap = idx.cap
	for _, m := range idx.hot {
		if m.CooldownUntil > now {
			s.CooldownCount++
		}
		if !m.Enabled {
			s.DisabledCount++
		}
		s.InflightSum += int64(m.Inflight)
	}
	return s
}

// pickDemoteVictim 在容量满时选择最差账号丢弃。
// 优先：最低 priority，再最高 FailureScore，再最高 Inflight，再最大 ID。
func pickDemoteVictim(hot map[string]catalog.HotMeta) string {
	var (
		found  bool
		bestID string
		best   catalog.HotMeta
	)
	for id, m := range hot {
		if !found {
			found = true
			bestID = id
			best = m
			continue
		}
		if worseVictim(m, best) {
			bestID = id
			best = m
		}
	}
	return bestID
}

// worseVictim 报告 a 是否比 b 更适合作为 demote 候选。
func worseVictim(a, b catalog.HotMeta) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.FailureScore != b.FailureScore {
		return a.FailureScore > b.FailureScore
	}
	if a.Inflight != b.Inflight {
		return a.Inflight > b.Inflight
	}
	return a.ID > b.ID
}
