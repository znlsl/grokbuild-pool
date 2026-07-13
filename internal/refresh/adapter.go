package refresh

import (
	"context"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/mockup"
)

// MockOAuthAdapter 将 mockup.MockOAuth 适配为 OAuthClient。
type MockOAuthAdapter struct {
	Mock *mockup.MockOAuth
}

// NewMockOAuthAdapter 包装 mockup.MockOAuth。
func NewMockOAuthAdapter(m *mockup.MockOAuth) *MockOAuthAdapter {
	if m == nil {
		m = mockup.NewMockOAuth()
	}
	return &MockOAuthAdapter{Mock: m}
}

// Refresh 实现 OAuthClient。
func (a *MockOAuthAdapter) Refresh(ctx context.Context, refreshToken string) (catalog.TokenSet, error) {
	set, err := a.Mock.Refresh(ctx, refreshToken)
	if err != nil {
		return catalog.TokenSet{}, err
	}
	return catalog.TokenSet{
		AccessToken:  set.AccessToken,
		RefreshToken: set.RefreshToken,
		ExpiresAt:    set.ExpiresAt,
	}, nil
}
