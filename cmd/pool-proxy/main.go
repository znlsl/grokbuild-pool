// Command pool-proxy 是大规模账号池的 Scheme B HTTP 入口。
//
// 默认监听：127.0.0.1:18080（生产 :8080 不得触碰）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/admin"
	"github.com/yshgsh1343/grokbuild2api/internal/adminui"
	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/clients"
	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
	"github.com/yshgsh1343/grokbuild2api/internal/httpserver"
	"github.com/yshgsh1343/grokbuild2api/internal/importjobs"
	"github.com/yshgsh1343/grokbuild2api/internal/lease"
	"github.com/yshgsh1343/grokbuild2api/internal/mockup"
	"github.com/yshgsh1343/grokbuild2api/internal/outbound"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/anthropic"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/executor"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/openai"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/upstream"
	"github.com/yshgsh1343/grokbuild2api/internal/refresh"
	"github.com/yshgsh1343/grokbuild2api/internal/selector"
	"github.com/yshgsh1343/grokbuild2api/internal/ssoimport"
)

// version 可在构建时用 -ldflags 覆盖。
var version = "0.1.0-m11"

func main() {
	configPath := flag.String("config", "", "path to config.yaml (optional; defaults apply)")
	showVersion := flag.Bool("version", false, "print version and exit")
	// 同时接受裸 "version" 以兼容 Makefile。
	mockUpstream := flag.Bool("mock-upstream", false, "force internal mock Grok upstream (no real network)")
	mockFailHalf := flag.Bool("mock-fail-half", false, "mock: 429 for half of tokens by hash (M11 G5)")
	mockStreamDelayMS := flag.Int("mock-stream-delay-ms", 0, "mock: delay each SSE chunk by N ms (G4 hold-open)")
	listenOverride := flag.String("listen", "", "override listen address (default 127.0.0.1:18080)")
	dbPathOverride := flag.String("db", "", "override catalog sqlite path")
	dataDirOverride := flag.String("data-dir", "", "override data_dir")
	flag.Parse()

	if *showVersion || (flag.NArg() == 1 && (flag.Arg(0) == "version" || flag.Arg(0) == "--version" || flag.Arg(0) == "-v")) {
		fmt.Printf("pool-proxy %s (scheme-B)\n", version)
		return
	}

	logger := newLogger("info")
	slog.SetDefault(logger)

	cfgPath := strings.TrimSpace(*configPath)
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}

	var cfg config.Config
	var err error
	if cfgPath != "" {
		cfg, err = config.Load(cfgPath)
		if err != nil {
			// 可选默认路径缺失 → 使用默认配置。
			if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
				logger.Info("config_missing_using_defaults", "path", cfgPath)
				cfg = config.Default()
			} else {
				fail(logger, "config_invalid", err)
			}
		} else {
			logger.Info("config_loaded", "path", cfgPath)
		}
	} else {
		cfg = config.Default()
	}

	if *mockUpstream {
		cfg.MockUpstream = true
		cfg.Upstream.BaseURL = ""
	}
	if v := strings.TrimSpace(*listenOverride); v != "" {
		cfg.Listen = v
	}
	if v := strings.TrimSpace(os.Getenv("LISTEN")); v != "" {
		cfg.Listen = v
	}
	if v := strings.TrimSpace(*dataDirOverride); v != "" {
		cfg.DataDir = v
	}
	if v := strings.TrimSpace(*dbPathOverride); v != "" {
		cfg.DBPath = v
	}
	if envTrue("ALLOW_PUBLIC_LISTEN") {
		cfg.AllowPublicListen = true
	}
	cfg.ApplyDefaultsPublic()
	if err := cfg.Validate(); err != nil {
		fail(logger, "config_invalid", err)
	}

	logger = newLogger(cfg.Logging.Level)
	slog.SetDefault(logger)

	dbPath, err := cfg.ResolveDBPath()
	if err != nil {
		fail(logger, "catalog_db_missing", err)
	}
	logger.Info("catalog_open", "path", dbPath)

	cat, err := catalog.Open(dbPath)
	if err != nil {
		fail(logger, "catalog_open_failed", err)
	}
	defer cat.Close()

	hotSize := cfg.EffectiveHotSize()
	maxInflight := int32(cfg.Selector.MaxInflightPerAccount)
	if maxInflight <= 0 {
		maxInflight = 4 // 防封号默认：单账号硬并发上限
	}
	idx := hot.New(hot.Config{HotSize: cfg.HotSize, MaxInflightPerAccount: maxInflight})
	logger.Info("antiban_limits", "max_inflight_per_account", maxInflight)
	loaded, err := idx.LoadEligible(cat)
	if err != nil {
		fail(logger, "hot_load_failed", err)
	}
	logger.Info("hot_index_loaded", "hot_size", loaded, "cap", hotSize)

	selCfg := selector.Config{
		Strategy:     cfg.Selector.Strategy,
		HotSize:      hotSize,
		StickyTTLSec: cfg.Selector.StickyTTLSec,
		StickyMax:    cfg.Selector.StickyMax,
		Pow2K:        cfg.Selector.Pow2K,
		MaxAttempts:  cfg.Selector.MaxAttempts,
		WPriority:    cfg.Selector.WPriority,
		WInflight:    cfg.Selector.WInflight,
		WFailure:     cfg.Selector.WFailure,
		JitterAmp:    cfg.Selector.JitterAmp,
	}
	sel := selector.New(idx, selCfg)

	leaseCfg := lease.Config{
		MaxAttempts:                 cfg.Lease.MaxAttempts,
		CooldownBaseSec:             cfg.Lease.CooldownBaseSec,
		CooldownCapSec:              cfg.Lease.CooldownCapSec,
		UnauthorizedCooldownSec:     cfg.Lease.UnauthorizedCooldownSec,
		PaymentRequiredCooldownSec:  cfg.Lease.PaymentRequiredCooldownSec,
		UnauthorizedQuarantineAfter: cfg.Lease.UnauthorizedQuarantineAfter,
		ForbiddenCooldownSec:        cfg.Lease.ForbiddenCooldownSec,
		ForbiddenQuarantineAfter:    cfg.Lease.ForbiddenQuarantineAfter,
		CooldownJitterPct:           cfg.Lease.CooldownJitterPct,
	}
	leaser := lease.New(cat, idx, sel, leaseCfg)

	// 上游：--mock-upstream 或空 base_url 时用 mock。
	var mockSrv *mockup.ResponsesServer
	var poster executor.UpstreamPoster
	if cfg.UseMockUpstream() {
		mockSrv = mockup.NewResponsesServer()
		defer mockSrv.Close()
		if *mockFailHalf {
			mockSrv.FailHalfByToken = true
			mockSrv.FailStatus = 429
		}
		if *mockStreamDelayMS > 0 {
			mockSrv.StreamChunkDelay = time.Duration(*mockStreamDelayMS) * time.Millisecond
			mockSrv.StreamRepeat = 3
		}
		poster = &mockup.Poster{Client: mockSrv.Client()}
		logger.Info("upstream_mock_enabled",
			"base_url", mockSrv.URL(),
			"fail_half", *mockFailHalf,
			"stream_delay_ms", *mockStreamDelayMS,
		)
	} else {
		uc := upstream.NewClient(upstream.Config{
			BaseURL:          cfg.Upstream.BaseURL,
			ClientVersion:    cfg.Upstream.ClientVersion,
			ClientIdentifier: cfg.Upstream.ClientIdentifier,
			TokenAuth:        cfg.Upstream.TokenAuth,
			UserAgent:        cfg.Upstream.UserAgent,
			RequestTimeout:   30 * time.Second,
		})
		poster = uc
		logger.Info("upstream_live", "base_url", uc.BaseURL())
	}

	// 令牌刷新（GAP-001/005）：
	// 1) mock-upstream → MockOAuth（无公网）
	// 2) POOL_OAUTH_ENABLED=1 且 STATUS UNLOCK_M12=true → HTTPRefreshClient / XaiOAuth
	// 3) 否则 DisabledOAuth 桩（绝不访问 auth.x.ai）
	var oauth refresh.OAuthClient
	switch {
	case cfg.UseMockUpstream():
		oauth = refresh.NewMockOAuthAdapter(mockup.NewMockOAuth())
		logger.Info("refresh_oauth_mock")
	case refresh.RealOAuthAllowed(cfg.OAuth.StatusPath):
		oauth = refresh.NewHTTPRefreshClient(refresh.HTTPRefreshConfig{
			RefreshURL: cfg.OAuth.RefreshURL,
			ClientID:   cfg.OAuth.ClientID,
		})
		logger.Info("refresh_oauth_http",
			"refresh_url", firstNonEmpty(cfg.OAuth.RefreshURL, refresh.DefaultXAITokenURL),
			"client_id_set", strings.TrimSpace(cfg.OAuth.ClientID) != "",
			"gate", "POOL_OAUTH_ENABLED+UNLOCK_M12",
		)
	default:
		oauth = refresh.DisabledOAuth{}
		logger.Info("refresh_oauth_disabled",
			"reason", "need POOL_OAUTH_ENABLED=1 and STATUS UNLOCK_M12=true",
			"env_enabled", refresh.OAuthEnvEnabled(),
			"unlock_m12", refresh.StatusUnlockM12(cfg.OAuth.StatusPath),
		)
	}
	refreshCfg := refresh.DefaultConfig()
	refreshSvc := refresh.New(cat, oauth, refreshCfg, nil, nil)
	refreshCtx, refreshCancel := context.WithCancel(context.Background())
	if err := refreshSvc.Start(refreshCtx); err != nil {
		refreshCancel()
		fail(logger, "refresh_start_failed", err)
	}
	defer func() {
		refreshCancel()
		refreshSvc.Stop()
	}()
	logger.Info("refresh_workers_started",
		"workers", refreshCfg.Workers,
		"qps", refreshCfg.QPS,
		"skew_sec", refreshCfg.SkewSec,
	)

	refAdapter := &refresh.ExecutorAdapter{
		Service: refreshSvc,
		Catalog: cat,
		Hot:     idx,
	}

	outboundFactory := outbound.NewFactory(upstream.Config{
		BaseURL:          cfg.Upstream.BaseURL,
		ClientVersion:    cfg.Upstream.ClientVersion,
		ClientIdentifier: cfg.Upstream.ClientIdentifier,
		UserAgent:        cfg.Upstream.UserAgent,
		TokenAuth:        cfg.Upstream.TokenAuth,
	})
	// 按租约 ProxyURL 选择出站；空则直连。P1：ClientFor 账号-代理亲和；mock 忽略代理。
	exec := &executor.Executor{
		Leaser:   leaser,
		Upstream: poster,
		UpstreamFor: func(l lease.Lease) (executor.UpstreamPoster, error) {
			if cfg.UseMockUpstream() {
				return poster, nil
			}
			return outboundFactory.ClientFor(l.AccountID, l.ProxyURL)
		},
		Refresher:   refAdapter,
		MaxAttempts: cfg.Lease.MaxAttempts,
		Logger:      logger,
		RequestID:   httpserver.RequestIDFromContext,
		// P1.5：连接错误时 ForgetAccount / Forget 代理
		OnDialError: func(accountID, proxyURL string, err error) {
			if accountID != "" {
				outboundFactory.ForgetAccount(accountID)
			}
			if proxyURL != "" {
				outboundFactory.Forget(proxyURL)
			}
			logger.Warn("outbound_forget_on_dial_error",
				"account_id", accountID,
				"proxy_set", proxyURL != "",
				"error", err,
			)
		},
	}

	oai := &openai.Handlers{
		Post:    exec.Post,
		MaxBody: cfg.Limits.MaxBodyBytes,
	}
	anth := &anthropic.Handlers{
		Post:    exec.Post,
		Cfg:     cfg.Anthropic,
		MaxBody: cfg.Limits.MaxBodyBytes,
	}

	metrics := &httpserver.Metrics{}
	// 初始化热池指标
	st := idx.Stats(0)
	metrics.SetPoolGauges(st.HotSize, st.CooldownCount)

	// 客户端令牌库（new-api 风格：额度 / 并发 / RPM）
	tokenDB := filepath.Join(cfg.DataDir, "tokens.db")
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		fail(logger, "data_dir", err)
	}
	tokenStore, err := clients.Open(tokenDB)
	if err != nil {
		fail(logger, "tokens_open", err)
	}
	defer tokenStore.Close()
	logger.Info("tokens_store_open", "path", tokenDB)

	startedAt := time.Now()
	// 运行时可热更新参数（管理台）：yaml 默认 → 文件覆盖 → Apply 落盘并写 hot/lease
	rt := admin.RuntimeSettings{
		HotSize:                     cfg.HotSize,
		MaxInflightPerAccount:       maxInflight,
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
		// 只读展示
		Listen:             cfg.Listen,
		AllowPublicListen:  cfg.AllowPublicListen,
		DataDir:            cfg.DataDir,
		DBPath:             dbPath,
		MockUpstream:       cfg.UseMockUpstream(),
		UpstreamBaseURL:    cfg.Upstream.BaseURL,
		OAuthRefreshURL:    cfg.OAuth.RefreshURL,
		APIKeyConfigured:   strings.TrimSpace(cfg.APIKey) != "",
		AdminKeyConfigured: strings.TrimSpace(cfg.AdminKey) != "",
		LoggingLevel:       cfg.Logging.Level,
	}
	settingsPath := filepath.Join(cfg.DataDir, "settings.json")
	settingsCtl := &admin.SettingsController{
		Path:     settingsPath,
		Hot:      idx,
		Lease:    leaser,
		Selector: sel,
		Refresh:  refreshSvc,
		ProcessInfo: admin.RuntimeSettings{
			Listen:             cfg.Listen,
			AllowPublicListen:  cfg.AllowPublicListen,
			DataDir:            cfg.DataDir,
			DBPath:             dbPath,
			MockUpstream:       cfg.UseMockUpstream(),
			UpstreamBaseURL:    cfg.Upstream.BaseURL,
			OAuthRefreshURL:    cfg.OAuth.RefreshURL,
			APIKeyConfigured:   strings.TrimSpace(cfg.APIKey) != "",
			AdminKeyConfigured: strings.TrimSpace(cfg.AdminKey) != "",
			LoggingLevel:       cfg.Logging.Level,
		},
	}
	// 先 Load 文件（若存在则覆盖内存），再与 yaml 默认合并：文件字段覆盖运行时
	if err := settingsCtl.Load(); err != nil {
		logger.Warn("settings_load_failed", "path", settingsPath, "error", err)
	}
	if loaded := settingsCtl.Snapshot().RuntimeSettings; loaded != (admin.RuntimeSettings{}) {
		// 文件非空时以文件为准覆盖 rt（零值字段保留 yaml 默认需逐字段；此处整包覆盖已落盘字段）
		// Load 成功后 Snapshot 即文件内容；若文件不存在则仍是零值，保持 yaml rt。
		if st, err := os.Stat(settingsPath); err == nil && st.Size() > 0 {
			rt = loaded
		}
	}
	if _, err := settingsCtl.Apply(rt); err != nil {
		logger.Warn("settings_apply_failed", "path", settingsPath, "error", err)
	} else {
		// 再次写入只读进程信息（避免旧 settings.json 缺字段/零值）
		cur := settingsCtl.Snapshot().RuntimeSettings
		cur.Listen = cfg.Listen
		cur.AllowPublicListen = cfg.AllowPublicListen
		cur.DataDir = cfg.DataDir
		cur.DBPath = dbPath
		cur.MockUpstream = cfg.UseMockUpstream()
		cur.UpstreamBaseURL = cfg.Upstream.BaseURL
		cur.OAuthRefreshURL = cfg.OAuth.RefreshURL
		cur.APIKeyConfigured = strings.TrimSpace(cfg.APIKey) != ""
		cur.AdminKeyConfigured = strings.TrimSpace(cfg.AdminKey) != ""
		cur.LoggingLevel = cfg.Logging.Level
		// 新字段零值回填 yaml 默认
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
			})
			if err != nil {
				fail(logger, "sso_converter_config_invalid", err)
			}
			ssoConverter = client
		}
		importJobs, err = importjobs.NewWithOptions(cfg.DataDir, cat, importjobs.Options{
			MaxConcurrentJobs:  cfg.Imports.MaxConcurrentJobs,
			MaxEntries:         cfg.Imports.MaxEntries,
			MaxNDJSONLineBytes: cfg.Imports.MaxNDJSONLineBytes,
			MaxSSOValueBytes:   cfg.Imports.MaxSSOValueBytes,
			JobTimeout:         time.Duration(cfg.Imports.JobTimeoutSec) * time.Second,
			StagingStaleAfter:  time.Duration(cfg.Imports.StagingStaleAfterSec) * time.Second,
			AllowServerPath:    cfg.Imports.AllowServerPath,
			Converter:          ssoConverter,
			AfterImport: func() error {
				_, err := idx.LoadEligible(cat)
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
		Hot:        httpserver.IndexStats{Index: idx},
		Metrics:    metrics,
		Version:    version,
		StartedAt:  startedAt,
		Settings:   settingsCtl,
		Catalog:    cat,
		AccountHot: idx,
		ImportJobs: importJobs,
	}

	handler := httpserver.New(httpserver.Options{
		Config:     cfg,
		OpenAI:     oai,
		Anthropic:  anth,
		Hot:        httpserver.IndexStats{Index: idx},
		Version:    version,
		Logger:     logger,
		Metrics:    metrics,
		TokenStore: tokenStore,
		StartedAt:  startedAt,
		ExtraMount: func(mux *http.ServeMux) {
			adminH.Mount(mux)
			adminui.Mount(mux)
		},
		OnMiddleware: func(mw *httpserver.Middleware) {
			settingsCtl.SetGlobalMaxConcurrent = mw.SetMaxConcurrent
			settingsCtl.SetMaxBodyBytes = mw.SetMaxBody
			settingsCtl.SetRequestTimeout = mw.SetRequestTimeout
			snap := settingsCtl.Snapshot().RuntimeSettings
			mw.SetMaxConcurrent(snap.MaxConcurrent)
			mw.SetMaxBody(snap.MaxBodyBytes)
			mw.SetRequestTimeout(time.Duration(snap.RequestTimeoutSec) * time.Second)
		},
	})

	srv := httpserver.NewServer(cfg.Listen, handler, cfg.RequestTimeout())

	// 后台刷新 /metrics 的 gauge（轻量）。
	stopGauges := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopGauges:
				return
			case <-t.C:
				s := idx.Stats(0)
				metrics.SetPoolGauges(s.HotSize, s.CooldownCount)
				ok, fail, hit := refreshSvc.Stats()
				metrics.SetRefreshStats(ok, fail, hit)
				if st, err := cat.Stats(); err == nil {
					metrics.SetQuarantineCount(st.QuarantineCount)
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("pool_proxy_listen",
			"addr", cfg.Listen,
			"max_concurrent", cfg.Limits.MaxConcurrent,
			"hot_size", loaded,
			"db", dbPath,
			"mock_upstream", cfg.UseMockUpstream(),
			"version", version,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutdown_signal", "signal", sig.String())
	case err := <-errCh:
		if err != nil {
			fail(logger, "listen_failed", err)
		}
	}

	close(stopGauges)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown_error", "error", err)
	}
	if importJobs != nil {
		jobsCtx, jobsCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := importJobs.Close(jobsCtx); err != nil {
			logger.Error("import_jobs_shutdown_error", "error", err)
		}
		jobsCancel()
	}
	_ = idx.Close()
	logger.Info("pool_proxy_stopped")
}

func defaultConfigPath() string {
	candidates := []string{
		"config.yaml",
		"config.example.yaml",
		"/etc/pool-proxy/config.yaml",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}

func fail(logger *slog.Logger, msg string, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error(msg, "error", err)
	os.Exit(1)
}

func envTrue(name string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
