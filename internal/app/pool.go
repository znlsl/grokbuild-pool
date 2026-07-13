package app

import (
	"log/slog"

	"github.com/yshgsh1343/grokbuild2api/internal/catalog"
	"github.com/yshgsh1343/grokbuild2api/internal/config"
	"github.com/yshgsh1343/grokbuild2api/internal/hot"
	"github.com/yshgsh1343/grokbuild2api/internal/lease"
	"github.com/yshgsh1343/grokbuild2api/internal/selector"
)

// poolStack 冷库 + 热索引 + 选号 + 租约。
type poolStack struct {
	DBPath      string
	Catalog     *catalog.Catalog
	Hot         *hot.Index
	Selector    *selector.Selector
	Lease       *lease.Manager
	HotLoaded   int
	MaxInflight int32
}

// openPool 管线第 2 步：打开 catalog → 加载热池 → 组装 selector/lease。
func openPool(cfg config.Config, logger *slog.Logger) *poolStack {
	dbPath, err := cfg.ResolveDBPath()
	if err != nil {
		fail(logger, "catalog_db_missing", err)
	}
	logger.Info("catalog_open", "path", dbPath)

	cat, err := catalog.Open(dbPath)
	if err != nil {
		fail(logger, "catalog_open_failed", err)
	}

	// 启动热池容量以 YAML/默认值为准；data/settings.json 的 hot_size 在 wireAdmin.Apply 时
	// 按 Cap()!=目标 强制 Resize+LoadEligible（见 settings.Apply）。
	hotSize := cfg.EffectiveHotSize()
	maxInflight := int32(cfg.Selector.MaxInflightPerAccount)
	if maxInflight <= 0 {
		maxInflight = 4
	}
	idx := hot.New(hot.Config{HotSize: hotSize, MaxInflightPerAccount: maxInflight})
	logger.Info("antiban_limits", "max_inflight_per_account", maxInflight)

	loaded, err := idx.LoadEligible(cat)
	if err != nil {
		_ = cat.Close()
		fail(logger, "hot_load_failed", err)
	}
	logger.Info("hot_index_loaded", "hot_size", loaded, "cap", idx.Cap())

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

	return &poolStack{
		DBPath:      dbPath,
		Catalog:     cat,
		Hot:         idx,
		Selector:    sel,
		Lease:       leaser,
		HotLoaded:   loaded,
		MaxInflight: maxInflight,
	}
}
