# Lumo 主题插件市场示例（Todo 37）

`theme-warm-ember` 是签名主题插件示例：在沙箱中运行，输出校验后的 theme tokens，
前端将其应用为 CSS 变量（不修改 `tokens.css`）。

## 安装与使用

```powershell
# 1. 在插件页安装（路径 = 本目录；签名见 signature.txt）
#    PluginInstall { path: "<Lumo>/market/theme-warm-ember", signature: "<签名内容>" }

# 2. 在「主题插件市场」分区点击「应用主题」→ 前端调用 PluginThemeGet，
#    后端沙箱执行入口脚本 → 返回 {"tokens": {...}} → 应用为 CSS 变量。

# 3. 重新签名（manifest 改动后）：
go run ./internal/plugin/sign -manifest market/theme-warm-ember/manifest.json -out market/theme-warm-ember/signature.txt
```

## 插件协议（internal/plugin/exec.go）

- stdin 读入 JSON-RPC 请求：`{"method":"run","params":{}}`
- stdout 输出单个 JSON 值：`{"tokens": {"--key": "value", ...}}`
- tokens 键匹配 `^--[a-zA-Z0-9-]+$`、值不含 `; { }` 与换行（后端逐键校验防 CSS 注入）
- 非零退出码 = 插件自身失败（`ok:false` + stderr 诊断）；沙箱超时/输出超限 → `SANDBOX_LIMIT`
