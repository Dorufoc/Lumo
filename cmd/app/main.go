// Lumo AI V2.0 服务入口：本地优先学习平台（Go + Vue3 + SQLite）。
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"lumo/internal/app"
	"lumo/internal/config"
	"lumo/internal/database"
	apphttp "lumo/internal/platform/http"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	// panic 兜底：记录后以非零码退出，避免静默崩溃。
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered", "panic", r)
			os.Exit(2)
		}
	}()

	if err := run(); err != nil {
		slog.Error("服务退出", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := database.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := database.Migrate(ctx, db); err != nil {
		return err
	}
	slog.Info("数据库就绪", "path", cfg.DBPath)

	a := app.New(&cfg, db)
	srv := apphttp.NewServer()
	a.RegisterHandlers(srv)

	// 提醒调度器：进程级 in-process goroutine，每 30s 扫描到期提醒。
	// 使用独立于启动上下文（30s 超时）的长生命周期 context，进程退出即终止。
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go a.Svc.Reminder.RunScheduler(ctx2)

	// 托管前端构建产物（存在时）；否则提示用 dev server。
	if cfg.FrontendDist != "" {
		if st, err := os.Stat(cfg.FrontendDist); err == nil && st.IsDir() {
			srv.Mux().Handle("/", http.FileServer(http.Dir(cfg.FrontendDist)))
			slog.Info("静态资源已挂载", "dir", cfg.FrontendDist)
		}
	}

	slog.Info("Lumo 服务启动", "addr", cfg.HTTPAddr)
	return http.ListenAndServe(cfg.HTTPAddr, srv.Mux())
}
