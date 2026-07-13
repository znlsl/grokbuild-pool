// Package synth 为 catalog 负载测试生成合成账号记录（M04）。
// 令牌为假数据，绝不可用于真实 Grok/OAuth 端点。
package synth

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

// DefaultExpiresSpread 为 expires_at 分布的时间窗口。
const DefaultExpiresSpread = 48 * time.Hour

// PriorityWeight 为离散优先级分布中的一个桶。
type PriorityWeight struct {
	Priority int
	Weight   int // relative weight; must be > 0
}

// DefaultPriorities 近似真实偏斜：多数低优先级。
var DefaultPriorities = []PriorityWeight{
	{Priority: 0, Weight: 50},
	{Priority: 1, Weight: 30},
	{Priority: 2, Weight: 15},
	{Priority: 3, Weight: 5},
}

// Options 控制合成账号生成。
type Options struct {
	Count      int
	Seed       int64
	Now        time.Time
	Spread     time.Duration // expires_at window; default 48h
	Priorities []PriorityWeight
	// IDPrefix 默认为 "synth-"。
	IDPrefix string
}

// Generate 生成 count 条合成 catalog.Account。
// expires_at 在 [Now, Now+Spread] 上均匀分布。
// Priority 遵循配置的离散分布。
func Generate(opts Options) ([]catalog.Account, error) {
	if opts.Count < 0 {
		return nil, fmt.Errorf("synth: count must be >= 0")
	}
	if opts.Count == 0 {
		return nil, nil
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	spread := opts.Spread
	if spread <= 0 {
		spread = DefaultExpiresSpread
	}
	prios := opts.Priorities
	if len(prios) == 0 {
		prios = DefaultPriorities
	}
	prefix := opts.IDPrefix
	if prefix == "" {
		prefix = "synth-"
	}
	seed := opts.Seed
	if seed == 0 {
		seed = now.UnixNano()
	}
	rng := rand.New(rand.NewSource(seed))
	totalW := 0
	for _, p := range prios {
		if p.Weight <= 0 {
			return nil, fmt.Errorf("synth: priority weight must be > 0 for priority %d", p.Priority)
		}
		totalW += p.Weight
	}
	if totalW <= 0 {
		return nil, fmt.Errorf("synth: empty priority distribution")
	}

	nowUnix := now.Unix()
	spreadSec := int64(spread / time.Second)
	if spreadSec < 1 {
		spreadSec = 1
	}

	out := make([]catalog.Account, 0, opts.Count)
	for i := 0; i < opts.Count; i++ {
		id := fmt.Sprintf("%s%08d", prefix, i)
		prio := pickPriority(rng, prios, totalW)
		// 过期时间在窗口内均匀分布。
		expiresAt := nowUnix + rng.Int63n(spreadSec+1)
		access := fakeToken("at", id, seed)
		refresh := fakeToken("rt", id, seed)
		email := fmt.Sprintf("user%08d@synth.local", i)
		name := fmt.Sprintf("Synth User %d", i)
		identity := fmt.Sprintf("idk-%s", id)

		out = append(out, catalog.Account{
			ID:           id,
			Revision:     1,
			IdentityKey:  identity,
			Email:        email,
			Name:         name,
			Priority:     prio,
			Enabled:      true,
			Lifecycle:    catalog.LifecycleActive,
			AccessToken:  access,
			RefreshToken: refresh,
			ExpiresAt:    expiresAt,
			CreatedAt:    nowUnix,
			UpdatedAt:    nowUnix,
		})
	}
	return out, nil
}

// AccountAt 生成下标 i 的单条账号（适合流式）。
func AccountAt(opts Options, i int) (catalog.Account, error) {
	if i < 0 {
		return catalog.Account{}, fmt.Errorf("synth: index must be >= 0")
	}
	// 由 seed+i 派生的确定性 per-index RNG，使流式与批量在 id/tokens 语义一致；
	// priority/expires 使用以 i 为键的独立流。
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	spread := opts.Spread
	if spread <= 0 {
		spread = DefaultExpiresSpread
	}
	prios := opts.Priorities
	if len(prios) == 0 {
		prios = DefaultPriorities
	}
	prefix := opts.IDPrefix
	if prefix == "" {
		prefix = "synth-"
	}
	seed := opts.Seed
	if seed == 0 {
		seed = 1
	}
	totalW := 0
	for _, p := range prios {
		if p.Weight <= 0 {
			return catalog.Account{}, fmt.Errorf("synth: priority weight must be > 0 for priority %d", p.Priority)
		}
		totalW += p.Weight
	}
	rng := rand.New(rand.NewSource(seed + int64(i)*9973))
	id := fmt.Sprintf("%s%08d", prefix, i)
	prio := pickPriority(rng, prios, totalW)
	nowUnix := now.Unix()
	spreadSec := int64(spread / time.Second)
	if spreadSec < 1 {
		spreadSec = 1
	}
	expiresAt := nowUnix + rng.Int63n(spreadSec+1)
	return catalog.Account{
		ID:           id,
		Revision:     1,
		IdentityKey:  fmt.Sprintf("idk-%s", id),
		Email:        fmt.Sprintf("user%08d@synth.local", i),
		Name:         fmt.Sprintf("Synth User %d", i),
		Priority:     prio,
		Enabled:      true,
		Lifecycle:    catalog.LifecycleActive,
		AccessToken:  fakeToken("at", id, seed),
		RefreshToken: fakeToken("rt", id, seed),
		ExpiresAt:    expiresAt,
		CreatedAt:    nowUnix,
		UpdatedAt:    nowUnix,
	}, nil
}

func pickPriority(rng *rand.Rand, prios []PriorityWeight, totalW int) int {
	r := rng.Intn(totalW)
	for _, p := range prios {
		r -= p.Weight
		if r < 0 {
			return p.Priority
		}
	}
	return prios[len(prios)-1].Priority
}

func fakeToken(kind, id string, seed int64) string {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(seed))
	h := sha256.Sum256(append(append([]byte(kind+":"+id+":"), b[:]...), []byte(":synth")...))
	return "synth_" + kind + "_" + hex.EncodeToString(h[:16])
}
