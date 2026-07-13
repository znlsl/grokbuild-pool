package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yshgsh1343/grokbuild2api/internal/adminui"
	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/httpserver"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/anthropic"
	"github.com/yshgsh1343/grokbuild2api/internal/protocol/openai"
)

// serveHTTP 管线第 5 步：协议 handler → middleware → listen → 优雅退出。
func serveHTTP(cfg config.Config, pool *poolStack, up *upstreamStack, adm *adminStack, version string, logger *slog.Logger) {
	oai := &openai.Handlers{
		Post:    up.Executor.Post,
		MaxBody: cfg.Limits.MaxBodyBytes,
	}
	anth := &anthropic.Handlers{
		Post:    up.Executor.Post,
		Cfg:     cfg.Anthropic,
		MaxBody: cfg.Limits.MaxBodyBytes,
	}

	metrics := &httpserver.Metrics{}
	st := pool.Hot.Stats(0)
	metrics.SetPoolGauges(st.HotSize, st.CooldownCount)

	// admin 已创建时 Metrics 可能尚未挂上：补挂
	if adm.Handlers != nil {
		adm.Handlers.Metrics = metrics
	}

	handler := httpserver.New(httpserver.Options{
		Config:     cfg,
		OpenAI:     oai,
		Anthropic:  anth,
		Hot:        httpserver.IndexStats{Index: pool.Hot},
		Version:    version,
		Logger:     logger,
		Metrics:    metrics,
		TokenStore: adm.Tokens,
		StartedAt:  adm.StartedAt,
		ExtraMount: func(mux *http.ServeMux) {
			adm.Handlers.Mount(mux)
			adminui.Mount(mux)
		},
		OnMiddleware: func(mw *httpserver.Middleware) {
			adm.Settings.SetGlobalMaxConcurrent = mw.SetMaxConcurrent
			adm.Settings.SetMaxBodyBytes = mw.SetMaxBody
			adm.Settings.SetRequestTimeout = mw.SetRequestTimeout
			snap := adm.Settings.Snapshot().RuntimeSettings
			mw.SetMaxConcurrent(snap.MaxConcurrent)
			mw.SetMaxBody(snap.MaxBodyBytes)
			mw.SetRequestTimeout(time.Duration(snap.RequestTimeoutSec) * time.Second)
		},
	})

	srv := httpserver.NewServer(cfg.Listen, handler, cfg.RequestTimeout())

	stopGauges := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopGauges:
				return
			case <-t.C:
				s := pool.Hot.Stats(0)
				metrics.SetPoolGauges(s.HotSize, s.CooldownCount)
				ok, failN, hit := up.Refresh.Stats()
				metrics.SetRefreshStats(ok, failN, hit)
				if st, err := pool.Catalog.Stats(); err == nil {
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
			"hot_size", pool.HotLoaded,
			"db", pool.DBPath,
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
	if adm.ImportJobs != nil {
		jobsCtx, jobsCancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := adm.ImportJobs.Close(jobsCtx); err != nil {
			logger.Error("import_jobs_shutdown_error", "error", err)
		}
		jobsCancel()
	}
	up.RefreshStop()
	up.Refresh.Stop()
	if up.MockServer != nil {
		up.MockServer.Close()
	}
	_ = pool.Hot.Close()
	_ = pool.Catalog.Close()
	_ = adm.Tokens.Close()
	logger.Info("pool_proxy_stopped")
}
