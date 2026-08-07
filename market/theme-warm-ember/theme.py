#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Warm Ember 主题插件（Todo 37 示例）。

协议（与 internal/plugin/exec.go 一致）：
  - stdin 读入 JSON-RPC 请求：{"method":"run","params":{}}；
  - stdout 输出单个 JSON 值：{"tokens": {"--key": "value", ...}}；
  - tokens 键匹配 ^--[a-zA-Z0-9-]+$，值不含 ; { } 与换行（后端逐键校验防 CSS 注入）。
"""
import json
import sys


def main() -> None:
    req = json.loads(sys.stdin.read() or "{}")
    assert req.get("method") == "run", f"unexpected method: {req.get('method')}"
    # 主题色板：覆盖 tokens.css 中的核心色板变量（前后端签名主题插件示例）。
    tokens = {
        "--color-primary": "#c2410c",
        "--color-primary-soft": "#fb923c",
        "--bg": "#fbf3ea",
        "--bg-elevated": "#fdf6ee",
        "--bg-card": "#fffaf3",
        "--border": "#ecd9c0",
        "--text": "#3b2417",
        "--text-secondary": "#7c5c47",
        "--text-muted": "#a0846c",
        "--success": "#3f6212",
        "--danger": "#b91c1c",
    }
    json.dump({"tokens": tokens}, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
