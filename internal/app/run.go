package app

import (
	"log/slog"
)

// Run 启动管线：
//
//	config → catalog/hot/lease → upstream/refresh/executor → admin/import/tokens → http serve
//
// 每一步只依赖上一步产物，便于替换/测试单段。
func Run(opts Options) {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	logger := newLogger("info")
	slog.SetDefault(logger)

	// 1) 配置
	cfg := loadConfig(opts, logger)
	logger = newLogger(cfg.Logging.Level)
	slog.SetDefault(logger)

	// 2) 冷热池 + 选号/租约
	pool := openPool(cfg, logger)

	// 3) 上游 + 刷新 + 执行器
	up := wireUpstream(cfg, opts, pool, logger)

	// 4) 令牌 / 设置 / 导入 / 管理 API
	// metrics 在 serve 阶段创建并回填；这里先占位 nil，serveHTTP 会补挂
	adm := wireAdmin(cfg, pool, up, nil, opts.Version, logger)

	// 5) HTTP 服务与优雅退出
	serveHTTP(cfg, pool, up, adm, opts.Version, logger)
}
