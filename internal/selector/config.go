package selector

// 策略常量。
const (
	StrategyPow2LeastLoad = "pow2_least_load"
	StrategyStableRR      = "stable_rr"
)

// 默认配置值：可用性优先（stable_rr）。
const (
	DefaultHotSize      = 3000
	DefaultStickyTTLSec = 1800
	DefaultStickyMax    = 100_000
	DefaultPow2K        = 2
	DefaultMaxAttempts  = 2
	DefaultWPriority    = 1.0
	DefaultWInflight    = 10.0
	DefaultWFailure     = 5.0
	DefaultJitterAmp    = 0.5
)

// Config 控制选号策略、粘性 LRU 与打分权重。
//
// 候选分（越高越好，仅 pot 使用）：
//
//	score = WPriority*priority - WInflight*inflight - WFailure*failureScore + U(-JitterAmp,+JitterAmp)
//
// stable_rr：最高 priority 可用层内 RoundRobin（对齐 CPA 稳模式）。
// pow2_least_load：Power-of-K 采样打分。
type Config struct {
	// Strategy: stable_rr | pow2_least_load
	Strategy string

	// HotSize 与计划配置对齐保留（实际热容量在 hot.Index 上）。
	HotSize int

	// StickyTTLSec 为粘性绑定存活秒数（默认 1800）。
	StickyTTLSec int64

	// StickyMax 为粘性 LRU 容量（默认 100_000）。
	StickyMax int

	// Pow2K 为 power-of-two-choices 采样候选数（默认 2）。
	Pow2K int

	// MaxAttempts 为调用方建议的获取失败切换预算（默认 2）。
	// Selector 自身不循环；由 lease 层使用。
	MaxAttempts int

	// WPriority 为 score 中 Priority 的权重（默认 1.0）。
	WPriority float64

	// WInflight 为 score 中 Inflight 惩罚权重（默认 10.0）。
	WInflight float64

	// WFailure 为 score 中 FailureScore 惩罚权重（默认 5.0）。
	WFailure float64

	// JitterAmp 为加在 score 上的均匀抖动半幅（默认 0.5）。
	// 置 0 可做确定性打分（测试）。
	JitterAmp float64

	// MaxInflightPerAccount 信息字段；硬过滤在 hot.Index 上生效。
	MaxInflightPerAccount int32
}

// DefaultConfig 返回可用性优先默认值。
func DefaultConfig() Config {
	return Config{
		Strategy:     StrategyStableRR,
		HotSize:      DefaultHotSize,
		StickyTTLSec: DefaultStickyTTLSec,
		StickyMax:    DefaultStickyMax,
		Pow2K:        DefaultPow2K,
		MaxAttempts:  DefaultMaxAttempts,
		WPriority:    DefaultWPriority,
		WInflight:    DefaultWInflight,
		WFailure:     DefaultWFailure,
		JitterAmp:    DefaultJitterAmp,
	}
}

// normalize 将零/负字段填为默认值。
func (c Config) normalize() Config {
	d := DefaultConfig()
	if c.Strategy == "" {
		c.Strategy = d.Strategy
	}
	if c.HotSize <= 0 {
		c.HotSize = d.HotSize
	}
	if c.StickyTTLSec <= 0 {
		c.StickyTTLSec = d.StickyTTLSec
	}
	if c.StickyMax <= 0 {
		c.StickyMax = d.StickyMax
	}
	if c.Pow2K <= 0 {
		c.Pow2K = d.Pow2K
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.WPriority == 0 {
		c.WPriority = d.WPriority
	}
	if c.WInflight == 0 {
		c.WInflight = d.WInflight
	}
	if c.WFailure == 0 {
		c.WFailure = d.WFailure
	}
	// JitterAmp：负表示默认；0 合法（确定性）。
	if c.JitterAmp < 0 {
		c.JitterAmp = d.JitterAmp
	}
	return c
}
