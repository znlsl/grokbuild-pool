package lease

// 默认配置（Scheme B / AGENT_PLAN 第 4 节 + 防封号 P0/P1.5）。
const (
	DefaultMaxAttempts                 = 6
	DefaultCooldownBaseSec              = 60
	DefaultCooldownCapSec               = 900
	DefaultUnauthorizedCooldownSec       = 120
	DefaultPaymentRequiredCooldownSec     = 300
	DefaultUnauthorizedQuarantineAfter = 3
	// DefaultForbiddenCooldownSec 403 冷却（秒），默认 15 分钟。
	DefaultForbiddenCooldownSec = 900
	// DefaultCooldownJitterPct 冷却抖动百分比（0-50），默认 20。
	DefaultCooldownJitterPct = 20
	// DefaultCooldownExpMax 429 指数退避最大位移。
	DefaultCooldownExpMax = 4
	// DefaultMaxInflightPerAccount 与 hot 对齐的文档默认。
	DefaultMaxInflightPerAccount = 4
)

// Config 控制获取失败切换与释放时的冷却策略。
type Config struct {
	// MaxAttempts 为 Acquire 失败切换预算（默认 6）。
	MaxAttempts int

	// CooldownBaseSec：429 且 Result.RetryAfter 为 0 时使用（默认 60）。
	CooldownBaseSec int64

	// CooldownCapSec 限制任意计算出的冷却上限（默认 900）。
	CooldownCapSec int64

	// UnauthorizedCooldownSec 用于 401（默认 120）。
	UnauthorizedCooldownSec int64

	// PaymentRequiredCooldownSec 用于 402（默认 300）。
	PaymentRequiredCooldownSec int64

	// UnauthorizedQuarantineAfter 次连续 401 后隔离账号（默认 3）。
	UnauthorizedQuarantineAfter int

	// ForbiddenCooldownSec 403 专用冷却（默认 900）。
	ForbiddenCooldownSec int64

	// ForbiddenQuarantineAfter 次连续 403 后隔离账号；0=关闭（默认）。
	// 用 LastError=="forbidden" 近似连续计数（见 releaseFailure）。
	ForbiddenQuarantineAfter int

	// CooldownJitterPct 为冷却时长附加的随机抖动百分比（默认 20，上限 50；0=关闭）。
	CooldownJitterPct int

	// CooldownExpMax 429 指数退避最大位移（base * 2^min(fail,ExpMax)），默认 4。
	CooldownExpMax int
}

// DefaultConfig 返回 Scheme B 默认值。
func DefaultConfig() Config {
	return Config{
		MaxAttempts:                 DefaultMaxAttempts,
		CooldownBaseSec:             DefaultCooldownBaseSec,
		CooldownCapSec:              DefaultCooldownCapSec,
		UnauthorizedCooldownSec:    DefaultUnauthorizedCooldownSec,
		PaymentRequiredCooldownSec:  DefaultPaymentRequiredCooldownSec,
		UnauthorizedQuarantineAfter: DefaultUnauthorizedQuarantineAfter,
		ForbiddenCooldownSec:        DefaultForbiddenCooldownSec,
		ForbiddenQuarantineAfter:    0, // 默认关闭
		CooldownJitterPct:           DefaultCooldownJitterPct,
		CooldownExpMax:              DefaultCooldownExpMax,
	}
}

// normalize 将零/负字段填为默认值。
func (c Config) normalize() Config {
	d := DefaultConfig()
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.CooldownBaseSec <= 0 {
		c.CooldownBaseSec = d.CooldownBaseSec
	}
	if c.CooldownCapSec <= 0 {
		c.CooldownCapSec = d.CooldownCapSec
	}
	if c.UnauthorizedCooldownSec <= 0 {
		c.UnauthorizedCooldownSec = d.UnauthorizedCooldownSec
	}
	if c.PaymentRequiredCooldownSec <= 0 {
		c.PaymentRequiredCooldownSec = d.PaymentRequiredCooldownSec
	}
	if c.UnauthorizedQuarantineAfter <= 0 {
		c.UnauthorizedQuarantineAfter = d.UnauthorizedQuarantineAfter
	}
	if c.ForbiddenCooldownSec <= 0 {
		c.ForbiddenCooldownSec = d.ForbiddenCooldownSec
	}
	// ForbiddenQuarantineAfter：0 合法（关闭），负值归零
	if c.ForbiddenQuarantineAfter < 0 {
		c.ForbiddenQuarantineAfter = 0
	}
	if c.CooldownJitterPct < 0 {
		c.CooldownJitterPct = d.CooldownJitterPct
	}
	if c.CooldownJitterPct > 50 {
		c.CooldownJitterPct = 50
	}
	if c.CooldownExpMax <= 0 {
		c.CooldownExpMax = d.CooldownExpMax
	}
	return c
}
