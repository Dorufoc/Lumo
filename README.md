# Lumo AI V2.0

本地优先的 AI 智能刷题练题平台（Go + Vue3 + SQLite）。

- 技术栈：Go 1.26+（`modernc.org/sqlite` 纯 Go 驱动）、Vue 3 + TypeScript + Vite + Pinia、手写 CSS 设计系统
- 形态：Web（前后端分离本地运行）；桌面端将来以 WebView 独立窗口渲染同一前端
- 设计文档：[完整设计文档.md](完整设计文档.md) · [API设计文档.md](API设计文档.md)

## 功能范围（P0–P4 已完成）

| 里程碑 | 内容 |
|---|---|
| P0 骨架 | 工作区/用户/设置、SQLite 迁移、加密备份/恢复、JSON/ZIP 导出、统一错误码信封 |
| P1 练习闭环 | 三格式题库导入（md/json/text）、题目版本不可变+去重、目标与计划生成、练习会话（草稿/跳过/幂等提交）、规则判分（单选/多选/填空容差）、错题归档、简化 SM-2 间隔复习、Dashboard 统计 |
| P2 AI 辅导 | Provider 抽象（OpenAI 兼容流式 + mock）、Agent 会话（SSE 事件流/取消/记忆）、Router/Tutor/Grader/Diagnoser/Librarian、简答/代码异步评分、未配置自动降级 |
| P3 资料 RAG | 文档导入（哈希去重/分块/embedding 可选）、关键词+向量混合检索、带引用流式问答 |
| P4 同步骨架 | sync_operations 变更队列、模拟服务端（设备注册/推拉/冲突副本）、备份恢复集成 |

## 启动

```powershell
# 1. 启动后端（默认 127.0.0.1:8787，首次启动自动建库并迁移）
go run ./cmd/app

# 2. 前端开发模式（http://localhost:5173，代理 /api → 8787）
cd frontend
npm install
npm run dev

# 生产形态：构建前端并由后端托管
cd frontend && npm run build
$env:LUMO_FRONTEND_DIST = "$PWD\frontend\dist"; go run ./cmd/app
# 打开 http://127.0.0.1:8787
```

数据目录默认 `%APPDATA%\lumo`（可用 `LUMO_DATA_DIR` 覆盖）。

## 测试与冒烟

```powershell
go test ./...          # 后端单元/集成测试（迁移/服务/判分/SM-2/RAG/同步/Provider/Agent）
cd frontend; npx vue-tsc -b; npm run build   # 前端类型检查与构建
.\scripts\smoke.ps1    # 端到端冒烟（需后端已启动）：工作区→导入→练习→判分→复习→RAG→同步→备份→导出
```

## 目录结构

```
cmd/app              入口（HTTP 服务 + 静态托管）
internal/
  app                应用容器（装配与传输层注册）
  agent              Agent 编排（会话/事件总线/处理器）
  config             配置加载（默认值<配置文件<环境变量）
  crypto             备份加密（AES-256-GCM）
  database           连接与版本化迁移（migrations/*.sql）
  domain             实体、错误模型、稳定错误码
  platform/http      方法名式 RPC + SSE + 受限文件下载
  provider           LLM/Embedding 抽象（openai/mock）
  repository         SQLite 仓储
  service            用例（工作区/题库/导入/目标计划/练习/复习/统计/文档RAG/同步/Provider配置）
frontend/            Vue3 前端（views/components/stores/api/styles）
scripts/smoke.ps1    端到端冒烟脚本
```

## 关键设计决策

- **方法名式 RPC**：`POST /api/v1/{Method}`，请求体=方法参数，响应统一 `{data,error,request_id}` 信封；服务层与 API 文档绑定方法一一对应，未来 Wails 绑定零改动复用。
- **密钥本地化**：API Key 仅存 `secrets.json`（0600），任何接口不回读，只返回 `configured`。
- **AI 可降级**：未配置 Provider 时 Tutor/Grader/RAG 全部退化为确定性模板，不阻塞本地学习闭环。
- **同步可替换**：模拟服务端（`sim-server/state.json`）遵循幂等操作日志与冲突副本协议，可切换真实云端。
