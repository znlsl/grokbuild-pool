package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/admin"
	"github.com/yshgsh1343/grokbuild2api/internal/clients"
	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/httpserver"
	"github.com/yshgsh1343/grokbuild2api/internal/importjobs"
	"github.com/yshgsh1343/grokbuild2api/internal/ssoimport"
)

// adminStack 令牌库 + 热配置 + 导入任务 + admin handlers。
type adminStack struct {
	Tokens     *clients.Store
	Settings   *admin.SettingsController
	ImportJobs *importjobs.Manager
	Handlers   *admin.Handlers
	StartedAt  time.Time
}

// wireAdmin 管线第 4 步：tokens.db → settings 热更新 → import jobs（SSO 并行转换）→ admin API。
func wireAdmin(cfg config.Config, pool *poolStack, up *upstreamStack, metrics *httpserver.Metrics, version string, logger *slog.Logger) *adminStack {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		fail(logger, "data_dir", err)
	}
	tokenDB := filepath.Join(cfg.DataDir, "tokens.db")
	tokenStore, err := clients.Open(tokenDB)
	if err != nil {
		fail(logger, "tokens_open", err)
	}
	logger.Info("tokens_store_open", "path", tokenDB)

	startedAt := time.Now()
	rt := admin.RuntimeSettings{
		HotSize:                     cfg.HotSize,
		MaxInflightPerAccount:       pool.MaxInflight,
		StickyTTLSec:                cfg.Selector.StickyTTLSec,
		StickyMax:                   cfg.Selector.StickyMax,
		Pow2K:                       cfg.Selector.Pow2K,
		WPriority:                   cfg.Selector.WPriority,
		WInflight:                   cfg.Selector.WInflight,
		WFailure:                    cfg.Selector.WFailure,
		JitterAmp:                   cfg.Selector.JitterAmp,
		SelectorMaxAttempts:         cfg.Selector.MaxAttempts,
		MaxAttempts:                 cfg.Lease.MaxAttempts,
		CooldownBaseSec:             cfg.Lease.CooldownBaseSec,
		CooldownCapSec:              cfg.Lease.CooldownCapSec,
		UnauthorizedCooldownSec:     cfg.Lease.UnauthorizedCooldownSec,
		PaymentRequiredCooldownSec:  cfg.Lease.PaymentRequiredCooldownSec,
		UnauthorizedQuarantineAfter: cfg.Lease.UnauthorizedQuarantineAfter,
		ForbiddenCooldownSec:        cfg.Lease.ForbiddenCooldownSec,
		ForbiddenQuarantineAfter:    cfg.Lease.ForbiddenQuarantineAfter,
		CooldownJitterPct:           cfg.Lease.CooldownJitterPct,
		CooldownExpMax:              4,
		MaxConcurrent:               cfg.Limits.MaxConcurrent,
		MaxBodyBytes:                cfg.Limits.MaxBodyBytes,
		RequestTimeoutSec:           cfg.Limits.RequestTimeoutSec,
		RefreshWorkers:              3,
		RefreshQPS:                  30,
		RefreshSkewSec:              300,
		TokenDefaultRemainQuota:     1000,
		TokenDefaultMaxConcurrent:   5,
		TokenDefaultRPM:             0,
		TokenDefaultUnlimited:       false,
		Listen:                      cfg.Listen,
		AllowPublicListen:           cfg.AllowPublicListen,
		DataDir:                     cfg.DataDir,
		DBPath:                      pool.DBPath,
		MockUpstream:                cfg.UseMockUpstream(),
		UpstreamBaseURL:             cfg.Upstream.BaseURL,
		OAuthRefreshURL:             cfg.OAuth.RefreshURL,
		APIKeyConfigured:            strings.TrimSpace(cfg.APIKey) != "",
		AdminKeyConfigured:          strings.TrimSpace(cfg.AdminKey) != "",
		LoggingLevel:                cfg.Logging.Level,
	}

	settingsPath := filepath.Join(cfg.DataDir, "settings.json")
	settingsCtl := &admin.SettingsController{
		Path:     settingsPath,
		Hot:      pool.Hot,
		Lease:    pool.Lease,
		Selector: pool.Selector,
		Refresh:  up.Refresh,
		ProcessInfo: admin.RuntimeSettings{
			Listen:             cfg.Listen,
			AllowPublicListen:  cfg.AllowPublicListen,
			DataDir:            cfg.DataDir,
			DBPath:             pool.DBPath,
			MockUpstream:       cfg.UseMockUpstream(),
			UpstreamBaseURL:    cfg.Upstream.BaseURL,
			OAuthRefreshURL:    cfg.OAuth.RefreshURL,
			APIKeyConfigured:   strings.TrimSpace(cfg.APIKey) != "",
			AdminKeyConfigured: strings.TrimSpace(cfg.AdminKey) != "",
			LoggingLevel:       cfg.Logging.Level,
		},
	}
	if err := settingsCtl.Load(); err != nil {
		logger.Warn("settings_load_failed", "path", settingsPath, "error", err)
	}
	if loaded := settingsCtl.Snapshot().RuntimeSettings; loaded != (admin.RuntimeSettings{}) {
		if st, err := os.Stat(settingsPath); err == nil && st.Size() > 0 {
			rt = loaded
		}
	}
	if _, err := settingsCtl.Apply(rt); err != nil {
		logger.Warn("settings_apply_failed", "path", settingsPath, "error", err)
	} else {
		cur := settingsCtl.Snapshot().RuntimeSettings
		cur.Listen = cfg.Listen
		cur.AllowPublicListen = cfg.AllowPublicListen
		cur.DataDir = cfg.DataDir
		cur.DBPath = pool.DBPath
		cur.MockUpstream = cfg.UseMockUpstream()
		cur.UpstreamBaseURL = cfg.Upstream.BaseURL
		cur.OAuthRefreshURL = cfg.OAuth.RefreshURL
		cur.APIKeyConfigured = strings.TrimSpace(cfg.APIKey) != ""
		cur.AdminKeyConfigured = strings.TrimSpace(cfg.AdminKey) != ""
		cur.LoggingLevel = cfg.Logging.Level
		if cur.StickyMax <= 0 {
			cur.StickyMax = cfg.Selector.StickyMax
		}
		if cur.SelectorMaxAttempts <= 0 {
			cur.SelectorMaxAttempts = cfg.Selector.MaxAttempts
		}
		if cur.MaxBodyBytes <= 0 {
			cur.MaxBodyBytes = cfg.Limits.MaxBodyBytes
		}
		if cur.RequestTimeoutSec <= 0 {
			cur.RequestTimeoutSec = cfg.Limits.RequestTimeoutSec
		}
		if cur.RefreshWorkers <= 0 {
			cur.RefreshWorkers = 3
		}
		if cur.RefreshQPS <= 0 {
			cur.RefreshQPS = 30
		}
		if cur.RefreshSkewSec <= 0 {
			cur.RefreshSkewSec = 300
		}
		if cur.CooldownExpMax <= 0 {
			cur.CooldownExpMax = 4
		}
		if _, err := settingsCtl.Apply(cur); err != nil {
			logger.Warn("settings_readonly_overlay_failed", "error", err)
		}
		logger.Info("settings_ready", "path", settingsPath)
	}

	var importJobs *importjobs.Manager
	if cfg.Imports.Enabled {
		var ssoConverter *ssoimport.Client
		converterCfg := cfg.Imports.SSOConverter
		if strings.TrimSpace(converterCfg.Endpoint) != "" || strings.TrimSpace(converterCfg.APIKey) != "" {
			client, err := ssoimport.NewClient(ssoimport.Config{
				Endpoint:      converterCfg.Endpoint,
				APIKey:        converterCfg.APIKey,
				MaxBatch:      converterCfg.MaxBatch,
				Timeout:       time.Duration(converterCfg.TimeoutSec) * time.Second,
				AllowInsecure: converterCfg.AllowInsecure,
				// 后端自动拆分 batch 并行转换，再按序合并
				Workers: 4,
			})
			if err != nil {
				fail(logger, "sso_converter_config_invalid", err)
			}
			ssoConverter = client
		}
		importJobs, err = importjobs.NewWithOptions(cfg.DataDir, pool.Catalog, importjobs.Options{
			MaxConcurrentJobs:  cfg.Imports.MaxConcurrentJobs,
			MaxEntries:         cfg.Imports.MaxEntries,
			MaxNDJSONLineBytes: cfg.Imports.MaxNDJSONLineBytes,
			MaxSSOValueBytes:   cfg.Imports.MaxSSOValueBytes,
			JobTimeout:         time.Duration(cfg.Imports.JobTimeoutSec) * time.Second,
			StagingStaleAfter:  time.Duration(cfg.Imports.StagingStaleAfterSec) * time.Second,
			AllowServerPath:    cfg.Imports.AllowServerPath,
			Converter:          ssoConverter,
			AfterImport: func() error {
				_, err := pool.Hot.LoadEligible(pool.Catalog)
				return err
			},
		})
		if err != nil {
			fail(logger, "import_jobs_init_failed", err)
		}
	}

	adminH := &admin.Handlers{
		AdminKey:   cfg.AdminKey,
		Config:     cfg,
		Tokens:     tokenStore,
		Hot:        httpserver.IndexStats{Index: pool.Hot},
		Metrics:    metrics,
		Version:    version,
		StartedAt:  startedAt,
		Settings:   settingsCtl,
		Catalog:    pool.Catalog,
		AccountHot: pool.Hot,
		ImportJobs: importJobs,
	}

	return &adminStack{
		Tokens:     tokenStore,
		Settings:   settingsCtl,
		ImportJobs: importJobs,
		Handlers:   adminH,
		StartedAt:  startedAt,
	}
}
