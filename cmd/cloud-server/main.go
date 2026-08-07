// Lumo 云同步服务入口：独立二进制（API 设计文档第 4 章契约）。
//
// 认证来源：环境变量 CLOUD_SERVER_TOKEN（未配置时拒绝一切请求）。
// 数据目录：复用 internal/config 解析规则（LUMO_DATA_DIR 或 %APPDATA%\lumo），
// 独立 SQLite 文件默认 <DataDir>/cloud/cloud.db，可用 LUMO_CLOUD_DB 覆盖完整路径。
// 本包独立解析环境变量，不 import internal/config 避免耦合。
package main

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	// panic 兜底：记录后以非零码退出，避免静默崩溃（与 cmd/app/main.go 同款）。
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered", "panic", r)
			os.Exit(2)
		}
	}()

	if err := run(); err != nil {
		slog.Error("云同步服务退出", "error", err)
		os.Exit(1)
	}
}

// serverConfig 由环境变量加载。
type serverConfig struct {
	Token  string // CLOUD_SERVER_TOKEN
	DBPath string // LUMO_CLOUD_DB 或 <DataDir>/cloud/cloud.db
	Addr   string // LUMO_CLOUD_ADDR，默认 127.0.0.1:8788
}

func loadServerConfig() serverConfig {
	// 数据目录：LUMO_DATA_DIR 覆盖，否则 os.UserConfigDir()/lumo（与 internal/config 同规则）。
	dataDir := os.Getenv("LUMO_DATA_DIR")
	if dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dataDir = filepath.Join(base, "lumo")
	}
	cfg := serverConfig{
		Token:  os.Getenv("CLOUD_SERVER_TOKEN"),
		DBPath: filepath.Join(dataDir, "cloud", "cloud.db"),
		Addr:   "127.0.0.1:8788",
	}
	if v := os.Getenv("LUMO_CLOUD_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("LUMO_CLOUD_ADDR"); v != "" {
		cfg.Addr = v
	}
	return cfg
}

func run() error {
	cfg := loadServerConfig()
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	srv := NewServer(store, cfg.Token)
	slog.Info("云同步服务启动",
		"addr", cfg.Addr, "db", cfg.DBPath, "auth_configured", cfg.Token != "")
	return http.ListenAndServe(cfg.Addr, srv.Handler())
}
