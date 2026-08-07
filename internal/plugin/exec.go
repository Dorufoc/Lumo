package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"lumo/internal/domain"
	"lumo/internal/sandbox"
)

// 插件沙箱执行常量（设计文档 4.24：资源限制 + 隔离）。
const (
	// Timeout 单次插件执行超时（超时整树终止，映射 SANDBOX_LIMIT）。
	Timeout = 15 * time.Second
	// MaxOutput 插件 stdout/stderr 单路输出上限（字节）。
	MaxOutput = 64 * 1024
)

// ExecResult 是插件执行结果（stdout 即插件的 JSON 输出）。
type ExecResult struct {
	ExitCode int    // 0=成功；非零=插件自身失败（非服务错误）
	Stdout   []byte // 插件 stdout，应为单个 JSON 值
	Stderr   string // 插件 stderr（失败时的诊断信息）
	TimedOut bool   // 是否因超时被终止
}

// Execute 在隔离沙箱中执行插件 entrypoint。
//
// 协议（本地优先的极简 JSON-RPC 式）：
//   - stdin = `{"method":"run","params":<payloadJSON>}`；
//   - 插件读取 stdin，执行能力后把「一个 JSON 值」写到 stdout；
//   - 非零退出码 = 插件自身失败（作为正常结果返回，由调用方决定 ok=false）；
//   - 沙箱级失败（超时/输出超限/启动失败）→ *sandbox.LimitError →
//     domain.CodeSandboxLimit；其余执行异常 → PLUGIN_ERROR。
func Execute(ctx context.Context, runner sandbox.Runner, entrypoint []string, payloadJSON []byte) (*ExecResult, error) {
	if len(entrypoint) == 0 {
		return nil, domain.PluginError("插件入口（entrypoint）为空")
	}
	params := payloadJSON
	if len(params) == 0 {
		params = []byte("{}")
	}
	req, err := json.Marshal(struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}{Method: "run", Params: params})
	if err != nil {
		return nil, domain.PluginError("构造插件请求失败: %v", err)
	}
	res, err := runner.Run(ctx, sandbox.Spec{
		Args:      entrypoint,
		Stdin:     string(req),
		Timeout:   Timeout,
		MaxOutput: MaxOutput,
	})
	if err != nil {
		var le *sandbox.LimitError
		if errors.As(err, &le) {
			return nil, domain.WrapError(domain.CodeSandboxLimit, "插件执行超限（超时或输出过大）", err)
		}
		return nil, domain.PluginError("插件执行失败: %v", err)
	}
	return &ExecResult{
		ExitCode: res.ExitCode,
		Stdout:   []byte(res.Stdout),
		Stderr:   res.Stderr,
		TimedOut: res.TimedOut,
	}, nil
}
