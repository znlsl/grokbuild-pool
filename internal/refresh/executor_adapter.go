package refresh

import (
	"context"
	"fmt"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
)

// ExecutorAdapter 将 Service 适配为 protocol executor.Refresher 接口
//（accessToken + revision）。成功刷新后可选地修补 hot.Index.ExpiresAt，
// 使 selector 资格判断看到新过期时间（GAP-005）。
type ExecutorAdapter struct {
	Service *Service
	Catalog *catalog.Catalog
	// Hot 可选；设置后刷新成功时尽力修补 ExpiresAt。
	Hot *hot.Index
}

// EnsureFresh 实现 executor.Refresher。
func (a *ExecutorAdapter) EnsureFresh(ctx context.Context, accountID string) (accessToken string, rev uint64, err error) {
	if a == nil || a.Service == nil {
		return "", 0, fmt.Errorf("%w: nil adapter/service", ErrInvalidInput)
	}
	set, err := a.Service.EnsureFresh(ctx, accountID)
	if err != nil {
		return "", 0, err
	}
	return a.tokenAndRev(accountID, set)
}

// ForceRefresh 实现 executor.Refresher。
func (a *ExecutorAdapter) ForceRefresh(ctx context.Context, accountID string) (accessToken string, rev uint64, err error) {
	if a == nil || a.Service == nil {
		return "", 0, fmt.Errorf("%w: nil adapter/service", ErrInvalidInput)
	}
	set, err := a.Service.ForceRefresh(ctx, accountID)
	if err != nil {
		return "", 0, err
	}
	return a.tokenAndRev(accountID, set)
}

func (a *ExecutorAdapter) tokenAndRev(accountID string, set catalog.TokenSet) (string, uint64, error) {
	var rev uint64
	if a.Catalog != nil {
		if acct, err := a.Catalog.Get(accountID); err == nil {
			if acct.Revision > 0 {
				rev = uint64(acct.Revision)
			}
			// 落库后优先使用 catalog 的 ExpiresAt。
			if acct.ExpiresAt > 0 {
				set.ExpiresAt = acct.ExpiresAt
			}
			if acct.AccessToken != "" {
				set.AccessToken = acct.AccessToken
			}
		}
	}
	a.patchHotExpires(accountID, set.ExpiresAt)
	if set.AccessToken == "" {
		return "", rev, fmt.Errorf("%w: empty access token after refresh", ErrInvalidInput)
	}
	return set.AccessToken, rev, nil
}

func (a *ExecutorAdapter) patchHotExpires(accountID string, expiresAt int64) {
	if a == nil || a.Hot == nil || accountID == "" || expiresAt <= 0 {
		return
	}
	meta, ok := a.Hot.Get(accountID)
	if !ok {
		return
	}
	if meta.ExpiresAt == expiresAt {
		return
	}
	meta.ExpiresAt = expiresAt
	_, _ = a.Hot.Promote(meta)
}
