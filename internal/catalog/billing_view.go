package catalog

import (
	"encoding/json"
	"strings"
)

// billingStore 是写入 accounts.billing_json 的结构（含测活元数据）。
// 与 upstream.BillingSnapshot 兼容，并扩展 probe 字段。
type billingStore struct {
	Monthly *struct {
		MonthlyLimit *float64 `json:"monthlyLimit"`
		Used         *float64 `json:"used"`
	} `json:"monthly"`
	Weekly *struct {
		CreditUsagePercent *float64 `json:"creditUsagePercent"`
		BillingPeriodEnd   string   `json:"billingPeriodEnd"`
	} `json:"weekly"`
	GrokBuild *struct {
		SharedWeeklyUsagePercent *float64 `json:"shared_weekly_usage_percent"`
		GrokBuildContribution    *float64 `json:"grok_build_contribution_percent"`
		PeriodEnd                string   `json:"period_end"`
	} `json:"grok_build"`
	ProbeOK     *bool  `json:"probe_ok"`
	ProbeStatus int    `json:"probe_status"`
	ProbeError  string `json:"probe_error"`
	ProbedAt    int64  `json:"probed_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ParseAccountBillingView 从 billing_json 解析管理台展示字段；空/坏 JSON 返回 nil。
func ParseAccountBillingView(raw string) *AccountBillingView {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var st billingStore
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil
	}
	v := &AccountBillingView{
		ProbeStatus: st.ProbeStatus,
		ProbeError:  st.ProbeError,
		ProbedAt:    st.ProbedAt,
		UpdatedAt:   st.UpdatedAt,
		ProbeOK:     st.ProbeOK,
	}
	if st.Monthly != nil {
		v.MonthlyUsed = st.Monthly.Used
		v.MonthlyLimit = st.Monthly.MonthlyLimit
	}
	if st.Weekly != nil {
		v.WeeklyUsagePercent = st.Weekly.CreditUsagePercent
		if st.Weekly.BillingPeriodEnd != "" {
			v.PeriodEnd = st.Weekly.BillingPeriodEnd
		}
	}
	if st.GrokBuild != nil {
		if st.GrokBuild.SharedWeeklyUsagePercent != nil {
			v.WeeklyUsagePercent = st.GrokBuild.SharedWeeklyUsagePercent
		}
		v.GrokBuildPercent = st.GrokBuild.GrokBuildContribution
		if st.GrokBuild.PeriodEnd != "" {
			v.PeriodEnd = st.GrokBuild.PeriodEnd
		}
	}
	// 全空且无测活时间则不展示
	if v.MonthlyUsed == nil && v.MonthlyLimit == nil && v.WeeklyUsagePercent == nil &&
		v.GrokBuildPercent == nil && v.ProbedAt == 0 && v.ProbeOK == nil {
		return nil
	}
	return v
}
