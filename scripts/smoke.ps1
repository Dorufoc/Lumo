# Lumo AI 端到端冒烟脚本：覆盖 P0-P4 核心链路。
# 前置：后端已启动（go run ./cmd/app），本脚本使用独立测试工作区。
param(
  [string]$BaseUrl = "http://127.0.0.1:8787"
)

$ErrorActionPreference = "Stop"
$wsid = $null
$uid = $null
$fail = 0

function Check($name, $cond, $detail) {
  if ($cond) { Write-Output "[PASS] $name" }
  else { Write-Output "[FAIL] $name :: $detail"; $script:fail++ }
}

function Api($method, $body) {
  if ($null -eq $body) { return (Invoke-RestMethod -Method Post -Uri "$BaseUrl/api/v1/$method").data }
  return (Invoke-RestMethod -Method Post -Uri "$BaseUrl/api/v1/$method" -ContentType 'application/json' -Body ($body | ConvertTo-Json -Depth 8)).data
}

# ---------- P0: 工作区 ----------
$ws = Api "WorkspaceCreate" @{ name = "冒烟-$([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())"; owner_type = "local"; idempotency_key = "smoke-ws-$([guid]::NewGuid())" }
$wsid = $ws.id
$users = Api "UserList" @{ workspace_id = $wsid }
$uid = $users[0].id
Check "工作区创建+默认用户" ($wsid -and $uid)

$settings = Api "SettingsGet" @{ workspace_id = $wsid }
Check "设置读取" ($settings.workspace_id -eq $wsid)

# ---------- P1: 目标与计划 ----------
$goal = Api "GoalCreate" @{
  workspace_id = $wsid; user_id = $uid; name = "冒烟目标"; daily_minutes = 60
  available_weekdays = @(1,2,3,4,5,6,7); idempotency_key = "smoke-goal-$([guid]::NewGuid())"
}
$goal = Api "GoalTransition" @{ workspace_id = $wsid; goal_id = $goal.id; version = $goal.version; action = "activate" }
$tasks = Api "PlanGenerate" @{ workspace_id = $wsid; goal_id = $goal.id; idempotency_key = "smoke-plan-$([guid]::NewGuid())" }
Check "目标激活+计划生成" ($goal.status -eq "active" -and $tasks.Count -gt 0)

# ---------- P1: 题库导入 → 练习 → 判分 → 复习 ----------
$md = @"
# 冒烟题库

## 1. 1+1=?
A. 1
B. 2
答案：B
解析：1+1=2

## 2. 太阳从西边升起。
A. 正确
B. 错误
答案：B

## 3. 中国的首都是____。
答案：北京
"@
$tmpFile = Join-Path $env:TEMP "smoke-$([guid]::NewGuid()).md"
Set-Content -Path $tmpFile -Value $md -Encoding UTF8
$upload = (curl.exe -s -F "file=@$tmpFile" "$BaseUrl/api/v1/LibraryUpload" | ConvertFrom-Json).data
$preview = Api "LibraryPreflightImport" @{ workspace_id = $wsid; file_path = $upload.path; format = "markdown"; idempotency_key = "smoke-imp-$([guid]::NewGuid())" }
$batch = Api "LibraryCommitImport" @{ workspace_id = $wsid; batch_id = $preview.batch_id; idempotency_key = "smoke-impc-$([guid]::NewGuid())" }
Check "题库导入 3 题" ($preview.valid_count -eq 3 -and $batch.status -eq "committed")

$qlist = Api "QuestionList" @{ workspace_id = $wsid; status = "draft" }
foreach ($q in $qlist.items) {
  $r1 = Api "QuestionTransition" @{ workspace_id = $wsid; question_id = $q.id; version = $q.version; action = "review" }
  $r2 = Api "QuestionTransition" @{ workspace_id = $wsid; question_id = $q.id; version = $r1.version; action = "publish" }
}
$published = Api "QuestionList" @{ workspace_id = $wsid; status = "published" }
Check "题目发布 3 题" ($published.items.Count -eq 3)

# 构造 题目id → 标准答案 映射（答题接口不返回答案，故先查题目详情）
$answerMap = @{}
foreach ($item in $published.items) {
  $detail = Api "QuestionGet" @{ workspace_id = $wsid; question_id = $item.id }
  $p = $detail.current_version.payload
  $answerMap[$item.id] = $p.answer
}

$session = Api "PracticeStart" @{
  workspace_id = $wsid; user_id = $uid; mode = "practice"
  question_ids = @($published.items | ForEach-Object { $_.id })
  idempotency_key = "smoke-ps-$([guid]::NewGuid())"
}
Check "练习会话（快照+题干不泄答案）" ($session.status -eq "answering" -and $session.questions.Count -eq 3 -and -not $session.questions[0].payload.answer)

$seq = 0
foreach ($q in $session.questions) {
  $seq++
  $ans = $answerMap[$q.question_id]
  Api "PracticeSaveAnswer" @{ workspace_id = $wsid; session_id = $session.id; question_version_id = $q.question_version_id; answer = $ans; client_sequence = $seq } | Out-Null
}
$result = Api "PracticeSubmit" @{ workspace_id = $wsid; session_id = $session.id; version = $session.version; idempotency_key = "smoke-psub-$([guid]::NewGuid())" }
Check "提交判分 30/30" ($result.total_score -eq 30 -and $result.max_score -eq 30 -and $result.wrong_answers.Count -eq 0)

# 再答错一题制造复习（判断题答反）
$first = $session.questions[0]
$wrongAns = if ($answerMap[$first.question_id] -eq "A") { "B" } else { "A" }
$session2 = Api "PracticeStart" @{
  workspace_id = $wsid; user_id = $uid; mode = "practice"
  question_ids = @($first.question_id)
  idempotency_key = "smoke-ps2-$([guid]::NewGuid())"
}
Api "PracticeSaveAnswer" @{ workspace_id = $wsid; session_id = $session2.id; question_version_id = $session2.questions[0].question_version_id; answer = $wrongAns; client_sequence = 1 } | Out-Null
$result2 = Api "PracticeSubmit" @{ workspace_id = $wsid; session_id = $session2.id; version = $session2.version; idempotency_key = "smoke-psub2-$([guid]::NewGuid())" }
Check "答错归档错题" ($result2.wrong_answers.Count -eq 1)

$due = Api "ReviewListDue" @{ workspace_id = $wsid; user_id = $uid }
$card = Api "ReviewSubmit" @{ workspace_id = $wsid; review_card_id = $due[0].id; rating = "good"; idempotency_key = "smoke-rv-$([guid]::NewGuid())" }
Check "复习卡评级 SM-2" ($card.repetition -eq 1 -and $card.interval_days -eq 1)

# ---------- P3: 文档导入 + RAG ----------
$docFile = Join-Path $env:TEMP "notes-$([guid]::NewGuid()).md"
Set-Content -Path $docFile -Value "# 物理笔记`n`n## 万有引力`n万有引力 F = G m1 m2 / r²。" -Encoding UTF8
$up2 = (curl.exe -s -F "file=@$docFile" "$BaseUrl/api/v1/LibraryUpload" | ConvertFrom-Json).data
$doc = Api "DocumentImport" @{ workspace_id = $wsid; file_path = $up2.path; idempotency_key = "smoke-doc-$([guid]::NewGuid())" }
Check "文档导入并索引" ($doc.status -eq "indexed")

$rag = Api "RAGAsk" @{ workspace_id = $wsid; user_id = $uid; question = "万有引力公式是什么？" }
Check "RAGAsk 返回会话句柄" ($rag.request_id -and $rag.session_id)

# ---------- P4: 同步 ----------
$reg = Api "SyncDeviceRegister" @{ device_id = "smoke-device"; device_name = "冒烟机"; platform = "windows"; app_version = "2.0.0" }
$push = Api "SyncPush" @{ workspace_id = $wsid }
$status = Api "SyncStatusGet" @{ workspace_id = $wsid }
Check "同步设备注册+推送" ($reg.status -in @("registered","already_registered") -and $status.pending_count -eq 0)

# ---------- P0: 统计 + 备份 + 导出 ----------
$dash = Api "DashboardGet" @{ workspace_id = $wsid; user_id = $uid }
Check "Dashboard 统计" ($dash.streak_days -ge 1 -and $dash.due_reviews -ge 0 -and -not $dash.has_empty_library)

$backup = Api "BackupCreate" @{ workspace_id = $wsid; password = "smoke-pass"; idempotency_key = "smoke-bak-$([guid]::NewGuid())" }
Check "加密备份创建" ($backup.file_name -like "backup-*.sqz")

$export = Api "DataExport" @{ workspace_id = $wsid; scope = "all"; format = "json" }
$dl = Invoke-WebRequest -Uri "$BaseUrl/api/v1/files?path=$([uri]::EscapeDataString($export.path))" -UseBasicParsing
Check "数据导出+下载" ($dl.StatusCode -eq 200 -and $dl.Content -match "workspace_id")

Remove-Item $tmpFile, $docFile -ErrorAction SilentlyContinue

Write-Output ""
if ($fail -eq 0) { Write-Output "=== 冒烟全部通过 ===" } else { Write-Output "=== 冒烟失败 $fail 项 ==="; exit 1 }
