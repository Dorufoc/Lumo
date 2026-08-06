// Package sandbox 提供隔离的子进程执行沙箱（stdlib only，无新增依赖）。
//
// 设计目标（设计文档 10.12 / Todo 30 插件复用）：
//   - 用户代码在独立子进程中执行，绝不落入宿主进程；
//   - 带超时（默认 10s）与输出字节上限（默认 64KB）；
//   - 超时/输出超限/启动失败返回 *LimitError（上层映射为 domain.CodeSandboxLimit），
//     绝不 panic；
//   - 非零退出码是正常结果（程序报错即调试输入），不视为沙箱失败。
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// DefaultTimeout 默认执行超时。
	DefaultTimeout = 10 * time.Second
	// DefaultMaxOutput 默认单路（stdout/stderr）输出上限（字节）。
	DefaultMaxOutput = 64 * 1024
)

// ErrKind 是沙箱失败类别。
type ErrKind int

const (
	// KindExec 启动/等待失败（命令不存在、权限等）。
	KindExec ErrKind = iota
	// KindTimeout 执行超时（进程树已被终止）。
	KindTimeout
	// KindOutputLimit 输出超过上限（已截断）。
	KindOutputLimit
)

func (k ErrKind) String() string {
	switch k {
	case KindExec:
		return "exec"
	case KindTimeout:
		return "timeout"
	case KindOutputLimit:
		return "output-limit"
	}
	return "unknown"
}

// LimitError 表示沙箱资源/执行失败（上层映射为 SANDBOX_LIMIT 类明确错误）。
type LimitError struct {
	Kind    ErrKind
	Message string
	Err     error
}

func (e *LimitError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("sandbox %s: %s: %v", e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("sandbox %s: %s", e.Kind, e.Message)
}

func (e *LimitError) Unwrap() error { return e.Err }

// Result 是沙箱执行结果。
type Result struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	TimedOut  bool
	Truncated bool // 任一输出通道超过上限被截断
}

// Spec 是执行规范。
type Spec struct {
	Args []string // 可执行文件与参数（必须非空）
	Dir  string   // 工作目录（空 = 继承调用方）
	Env  []string // 追加环境变量（叠加 os.Environ()）
	// Stdin 标准输入内容（空 = 无输入）。
	Stdin string
	// Timeout 超时（<=0 用 DefaultTimeout）。
	Timeout time.Duration
	// MaxOutput 单路输出上限字节数（<=0 用 DefaultMaxOutput）。
	MaxOutput int
}

// Runner 抽象沙箱执行（服务层可注入桩以便测试）。
type Runner interface {
	Run(ctx context.Context, spec Spec) (*Result, error)
}

type runnerFunc func(ctx context.Context, spec Spec) (*Result, error)

func (f runnerFunc) Run(ctx context.Context, spec Spec) (*Result, error) { return f(ctx, spec) }

// DefaultRunner 是真实子进程沙箱。
var DefaultRunner Runner = runnerFunc(Run)

// Run 在子进程中执行命令并捕获 stdout/stderr（超时/超限/启动失败 → *LimitError）。
func Run(ctx context.Context, spec Spec) (*Result, error) {
	if len(spec.Args) == 0 || spec.Args[0] == "" {
		return nil, &LimitError{Kind: KindExec, Message: "empty command"}
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOut := spec.MaxOutput
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}

	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	configureProcAttr(cmd)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	var outW, errW limitedWriter
	outW.limit = maxOut
	errW.limit = maxOut
	cmd.Stdout = &outW
	cmd.Stderr = &errW
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := cmd.Start(); err != nil {
		return nil, &LimitError{Kind: KindExec, Message: "start " + spec.Args[0], Err: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx2.Done():
		// 超时：整树终止并回收子进程，避免僵尸进程。
		killTree(cmd)
		<-done
		return &Result{ExitCode: -1, TimedOut: true, Stdout: outW.String(), Stderr: errW.String(),
				Truncated: outW.truncated || errW.truncated},
			&LimitError{Kind: KindTimeout, Message: "sandbox timed out"}
	case err := <-done:
		res := &Result{Stdout: outW.String(), Stderr: errW.String(),
			Truncated: outW.truncated || errW.truncated}
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				// 非零退出码是调试输入（程序报错），不是沙箱失败。
				res.ExitCode = ee.ExitCode()
			} else {
				return nil, &LimitError{Kind: KindExec, Message: "wait", Err: err}
			}
		}
		if res.Truncated {
			return res, &LimitError{Kind: KindOutputLimit, Message: "output exceeds limit"}
		}
		return res, nil
	}
}

// limitedWriter 受上限缓冲写入器：超过 limit 后丢弃多余字节并标记 truncated。
// 返回 len(p) 使上游 io.Copy 持续消费，避免子进程写满管道死锁。
type limitedWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	room := w.limit - w.buf.Len()
	if room > 0 {
		if len(p) > room {
			_, _ = w.buf.Write(p[:room])
			w.truncated = true
			return len(p), nil
		}
		return w.buf.Write(p)
	}
	w.truncated = true
	return len(p), nil
}

func (w *limitedWriter) String() string { return w.buf.String() }
