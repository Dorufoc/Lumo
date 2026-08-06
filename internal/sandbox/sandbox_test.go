package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// helperArgs 构造"以测试二进制自身作为子进程"的执行规范（跨平台确定性）。
// 子进程通过 -test.run=TestHelperProcess + GO_WANT_HELPER_PROCESS=1 触发 helper 分支。
func helperArgs(sub string, args ...string) Spec {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	all := []string{exe, "-test.run=TestHelperProcess", "--", sub}
	all = append(all, args...)
	return Spec{Args: all, Env: []string{"GO_WANT_HELPER_PROCESS=1"}}
}

// TestHelperProcess 是子进程测试桩：父进程以子命令方式调用本测试。
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "echo":
		fmt.Fprintln(os.Stdout, strings.Join(args[1:], " "))
	case "fail":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	case "sleep":
		time.Sleep(10 * time.Minute)
	case "huge":
		for i := 0; i < 20000; i++ {
			fmt.Fprint(os.Stdout, "0123456789")
		}
	default:
		os.Exit(2)
	}
}

func TestSandboxSuccess(t *testing.T) {
	res, err := Run(context.Background(), helperArgs("echo", "hello", "world"))
	if err != nil {
		t.Fatalf("run echo: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello world") {
		t.Fatalf("expected stdout hello world, got %q", res.Stdout)
	}
	if res.TimedOut || res.Truncated {
		t.Fatalf("unexpected flags: %+v", res)
	}
}

func TestSandboxNonZeroExit(t *testing.T) {
	res, err := Run(context.Background(), helperArgs("fail"))
	if err != nil {
		t.Fatalf("run fail: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("expected exit 3, got %d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Fatalf("expected stderr boom, got %q", res.Stderr)
	}
}

func TestSandboxTimeout(t *testing.T) {
	spec := helperArgs("sleep")
	spec.Timeout = 500 * time.Millisecond
	res, err := Run(context.Background(), spec)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T: %v", err, err)
	}
	if le.Kind != KindTimeout {
		t.Fatalf("expected KindTimeout, got %v", le.Kind)
	}
	if res == nil || !res.TimedOut {
		t.Fatalf("expected timed out result, got %+v", res)
	}
}

func TestSandboxOutputLimit(t *testing.T) {
	spec := helperArgs("huge")
	spec.MaxOutput = 128
	res, err := Run(context.Background(), spec)
	if err == nil {
		t.Fatal("expected output-limit error")
	}
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T: %v", err, err)
	}
	if le.Kind != KindOutputLimit {
		t.Fatalf("expected KindOutputLimit, got %v", le.Kind)
	}
	if res == nil || !res.Truncated {
		t.Fatalf("expected truncated result, got %+v", res)
	}
	if len(res.Stdout) > 128 {
		t.Fatalf("stdout not capped: %d bytes", len(res.Stdout))
	}
}

func TestSandboxBadCommand(t *testing.T) {
	_, err := Run(context.Background(), Spec{Args: []string{"no-such-binary-lumo-xyz"}})
	if err == nil {
		t.Fatal("expected exec error")
	}
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LimitError, got %T: %v", err, err)
	}
	if le.Kind != KindExec {
		t.Fatalf("expected KindExec, got %v", le.Kind)
	}
}
