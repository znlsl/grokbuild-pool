package refresh

import (
	"context"
	"fmt"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
)

// FailurePolicy 在刷新失败后施加健康侧效应。
// DefaultPolicy 通过 catalog.PatchHealth 写入冷却/隔离。
type FailurePolicy interface {
	OnRefreshFailure(ctx context.Context, cat *catalog.Catalog, accountID string, err error) error
}

// SuccessPolicy 为刷新成功落库后的可选钩子。
type SuccessPolicy interface {
	OnRefreshSuccess(ctx context.Context, cat *catalog.Catalog, accountID string) error
}

// DefaultPolicy 实现冷却 + 连续失败隔离。
type DefaultPolicy struct {
	CooldownSec      int64
	CooldownCapSec    int64
	QuarantineAfter  int
}

// NewDefaultPolicy 从 refresh Config 构建 DefaultPolicy。
func NewDefaultPolicy(cfg Config) *DefaultPolicy {
	cfg = cfg.normalize()
	return &DefaultPolicy{
		CooldownSec:     cfg.CooldownOnFailSec,
		CooldownCapSec:   cfg.CooldownCapSec,
		QuarantineAfter: cfg.QuarantineAfter,
	}
}

// OnRefreshFailure 加载账号、累加失败计数、设置冷却，
// 并在连续失败达 QuarantineAfter 次后隔离
//（M08 复用 ConsecutiveUnauthorized 记连续鉴权/刷新失败）。
func (p *DefaultPolicy) OnRefreshFailure(ctx context.Context, cat *catalog.Catalog, accountID string, err error) error {
	if cat == nil || accountID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cur, getErr := cat.Get(accountID)
	if getErr != nil {
		return getErr
	}

	now := time.Now().Unix()
	cool := p.CooldownSec
	if cool <= 0 {
		cool = DefaultCooldownOnFailSec
	}
	if p.CooldownCapSec > 0 && cool > p.CooldownCapSec {
		cool = p.CooldownCapSec
	}
	until := now + cool
	fc := cur.FailureCount + 1
	cu := cur.ConsecutiveUnauthorized + 1
	msg := "refresh failed"
	if err != nil {
		msg = fmt.Sprintf("refresh failed: %v", err)
		// 截断以控制 DB 行大小。
		if len(msg) > 240 {
			msg = msg[:240]
		}
	}
	patch := catalog.HealthPatch{
		FailureCount:            &fc,
		CooldownUntil:           &until,
		LastError:               &msg,
		ConsecutiveUnauthorized: &cu,
	}
	threshold := p.QuarantineAfter
	if threshold <= 0 {
		threshold = DefaultQuarantineAfter
	}
	if cu >= threshold {
		lc := catalog.LifecycleQuarantined
		en := false
		patch.Lifecycle = &lc
		patch.Enabled = &en
	}
	return cat.PatchHealth(accountID, patch)
}

// OnRefreshSuccess 清零连续鉴权失败计数与 last_error。
func (p *DefaultPolicy) OnRefreshSuccess(ctx context.Context, cat *catalog.Catalog, accountID string) error {
	if cat == nil || accountID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	zero := 0
	return cat.PatchHealth(accountID, catalog.HealthPatch{
		ConsecutiveUnauthorized: &zero,
		ClearLastError:          true,
	})
}

// FuncPolicy 将函数适配为 FailurePolicy / SuccessPolicy。
type FuncPolicy struct {
	Fail    func(ctx context.Context, cat *catalog.Catalog, accountID string, err error) error
	Success func(ctx context.Context, cat *catalog.Catalog, accountID string) error
}

// OnRefreshFailure 实现 FailurePolicy。
func (f FuncPolicy) OnRefreshFailure(ctx context.Context, cat *catalog.Catalog, accountID string, err error) error {
	if f.Fail == nil {
		return nil
	}
	return f.Fail(ctx, cat, accountID, err)
}

// OnRefreshSuccess 实现 SuccessPolicy。
func (f FuncPolicy) OnRefreshSuccess(ctx context.Context, cat *catalog.Catalog, accountID string) error {
	if f.Success == nil {
		return nil
	}
	return f.Success(ctx, cat, accountID)
}
