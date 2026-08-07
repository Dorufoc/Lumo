// Package config 负责应用配置加载。优先级：默认值 < 配置文件 < 环境变量。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config 是应用运行配置。
type Config struct {
	// DataDir 应用数据目录：数据库、附件、备份、导出文件均位于其下。
	DataDir string `json:"data_dir"`
	// DBPath SQLite 数据库文件路径（默认 <DataDir>/lumo.db）。
	DBPath string `json:"db_path"`
	// HTTPAddr HTTP 服务监听地址（默认 127.0.0.1:8787，仅本机访问）。
	HTTPAddr string `json:"http_addr"`
	// FrontendDist 前端构建产物目录；非空时由后端托管静态文件。
	FrontendDist string `json:"frontend_dist"`
	// SecretsPath 密钥文件路径（本地文件存储，权限受限，不随数据库导出）。
	SecretsPath string `json:"secrets_path"`
	// AttachmentsDir 附件目录（相对 DataDir 的 attachments）。
	AttachmentsDir string `json:"attachments_dir"`
	// BackupsDir 备份目录（相对 DataDir 的 backups）。
	BackupsDir string `json:"backups_dir"`
	// MaxBodyBytes 请求体上限。
	MaxBodyBytes int64 `json:"max_body_bytes"`
	// ReadTimeoutSec / WriteTimeoutSec HTTP 超时。
	ReadTimeoutSec  int `json:"read_timeout_sec"`
	WriteTimeoutSec int `json:"write_timeout_sec"`
	// CloudServerToken 云同步服务端访问令牌（读环境变量 CLOUD_SERVER_TOKEN，与 cloud-server
	// 二进制同源；空表示未配置，客户端不发起 cloud 同步、回退 in-process SyncService）。
	CloudServerToken string `json:"-"`
	// CloudServerURL 云同步服务端基础地址（读环境变量 LUMO_CLOUD_URL，默认 http://127.0.0.1:8788）。
	CloudServerURL string `json:"-"`
}

// Default 返回默认配置。
func Default() Config {
	return Config{
		DataDir:         defaultDataDir(),
		HTTPAddr:        "127.0.0.1:8787",
		MaxBodyBytes:    32 << 20,
		ReadTimeoutSec:  30,
		WriteTimeoutSec: 0, // SSE 长连接需要无限写超时
	}
}

// Load 按优先级合并配置：默认值 → 配置文件（LUMO_CONFIG 或 ./lumo.json）→ 环境变量。
func Load() Config {
	cfg := Default()

	if p := os.Getenv("LUMO_CONFIG"); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &cfg)
		}
	} else if b, err := os.ReadFile("lumo.json"); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}

	if v := os.Getenv("LUMO_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("LUMO_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("LUMO_FRONTEND_DIST"); v != "" {
		cfg.FrontendDist = v
	}

	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "lumo.db")
	}
	if cfg.SecretsPath == "" {
		cfg.SecretsPath = filepath.Join(cfg.DataDir, "secrets.json")
	}
	if cfg.AttachmentsDir == "" {
		cfg.AttachmentsDir = filepath.Join(cfg.DataDir, "attachments")
	}
	if cfg.BackupsDir == "" {
		cfg.BackupsDir = filepath.Join(cfg.DataDir, "backups")
	}
	// 云同步配置只来自环境变量（不落配置文件）。
	cfg.CloudServerToken = loadCloudToken()
	if cfg.CloudServerURL == "" {
		cfg.CloudServerURL = "http://127.0.0.1:8788"
	}
	if v := os.Getenv("LUMO_CLOUD_URL"); v != "" {
		cfg.CloudServerURL = v
	}
	return cfg
}

// EnsureDirs 确保数据目录存在。
func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.AttachmentsDir, c.BackupsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func defaultDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "lumo")
}

// loadCloudToken 读取云同步访问令牌（与 cloud-server 二进制同源环境变量）。
func loadCloudToken() string {
	return os.Getenv("CLOUD_SERVER_TOKEN")
}
