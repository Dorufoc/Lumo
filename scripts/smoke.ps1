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

# 调用方法并返回错误信封中的 error.code（成功返回 $null）
function ApiErr($method, $body) {
  try {
    if ($null -eq $body) { Invoke-RestMethod -Method Post -Uri "$BaseUrl/api/v1/$method" | Out-Null }
    else { Invoke-RestMethod -Method Post -Uri "$BaseUrl/api/v1/$method" -ContentType 'application/json' -Body ($body | ConvertTo-Json -Depth 8) | Out-Null }
    return $null
  } catch {
    $resp = $_.ErrorDetails.Message
    if ($resp) {
      try { return ($resp | ConvertFrom-Json).error.code } catch { return $resp }
    }
    return $_.Exception.Message
  }
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

# ================= P5A: 知识图谱 + 组卷考试 + 闪卡闭环 =================
$kn = Api "KnowledgeCreate" @{ workspace_id = $wsid; name = "冒烟知识点-物理" }
$tree = Api "KnowledgeTreeGet" @{ workspace_id = $wsid }
Check "知识点创建+树查询" ($kn.id -and $tree.Count -ge 1)

# 用已发布的 3 题组卷（config_json 含 duration_min 与版本题号）
$secVIDs = @($published.items | ForEach-Object { $_.current_version.id })
$paperCfg = @{ duration_min = 30; sections = @(@{ title = "冒烟卷第一部分"; order_no = 1; question_version_ids = $secVIDs; score = 10 }) }
$paper = Api "ExamPaperCreate" @{ workspace_id = $wsid; user_id = $uid; title = "冒烟组卷"; config_json = $paperCfg; idempotency_key = "smoke-paper-$([guid]::NewGuid())" }
Check "组卷创建" ($paper.id -and $paper.status -eq "draft")

# ExamPaperPublish 需要版本文本
$pubPaper = Api "ExamPaperPublish" @{ workspace_id = $wsid; paper_id = $paper.id; version = $paper.version }
$exam = Api "ExamStart" @{ workspace_id = $wsid; user_id = $uid; paper_id = $paper.id; idempotency_key = "smoke-exam-$([guid]::NewGuid())" }
Check "组卷发布+开考" ($pubPaper.status -eq "published" -and $exam.status -eq "answering" -and $exam.questions.Count -eq 3)

# 闪卡 CSV 导入（先上传再导入）→ 断言 valid_count
$fcCsv = "front,back`n光合作用,CO2+H2O→C6H12O6+O2`n牛顿第一定律,惯性定律"
$fcFile = Join-Path $env:TEMP "smoke-fc-$([guid]::NewGuid()).csv"
Set-Content -Path $fcFile -Value $fcCsv -Encoding UTF8
$fcUp = (curl.exe -s -F "file=@$fcFile" "$BaseUrl/api/v1/LibraryUpload" | ConvertFrom-Json).data
$fcBatch = Api "FlashcardImportCsv" @{ workspace_id = $wsid; user_id = $uid; file_path = $fcUp.path; idempotency_key = "smoke-fci-$([guid]::NewGuid())" }
Check "闪卡 CSV 导入 2 行" ($fcBatch.valid_count -eq 2 -and $fcBatch.status -eq "committed")

# 闪卡导出 .apkg（QA 场景 2：可下载 zip，内含 collection.anki2）
$fcExp = Api "FlashcardExportAnki" @{ workspace_id = $wsid; idempotency_key = "smoke-fce-$([guid]::NewGuid())" }
$apkgPath = Join-Path $env:TEMP "smoke-fc-$([guid]::NewGuid()).apkg"
Invoke-WebRequest -Uri "$BaseUrl/api/v1/files?path=$([uri]::EscapeDataString($fcExp.path))" -OutFile $apkgPath -UseBasicParsing
$apkgDir = Join-Path $env:TEMP "smoke-fc-$([guid]::NewGuid())"
Expand-Archive -Path $apkgPath -DestinationPath $apkgDir -Force
Check "闪卡导出 .apkg 含 collection.anki2" ($fcExp.format -eq "apkg" -and (Test-Path (Join-Path $apkgDir "collection.anki2")))
Remove-Item $apkgPath -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force $apkgDir -ErrorAction SilentlyContinue
Remove-Item $fcFile -ErrorAction SilentlyContinue

# ---------- P5A: 笔记 + 收藏 + 知识图谱 ----------
$note = Api "NoteCreate" @{ workspace_id = $wsid; user_id = $uid; kind = "free"; title = "冒烟笔记"; body_md = "## 光合作用`n暗反应固定 CO2" }
$ntf = Api "NoteToFlashcard" @{ workspace_id = $wsid; user_id = $uid; note_id = $note.id; idempotency_key = "smoke-ntf-$([guid]::NewGuid())" }
Check "笔记转闪卡" ($ntf.id -and $ntf.front)

$fav = Api "FavoriteToggle" @{ workspace_id = $wsid; user_id = $uid; ref_type = "question"; ref_id = $published.items[0].id }
$favList = Api "FavoriteList" @{ workspace_id = $wsid; user_id = $uid }
Check "收藏题目并列出" ($fav.favorited -eq $true -or $favList.items.Count -ge 1)

$graph = Api "KnowledgeGraphGet" @{ workspace_id = $wsid; user_id = $uid }
Check "知识图谱返回节点/边" ($null -ne $graph -and @($graph.nodes).Count -ge 1)

# ================= P5B: 班级 / 作业 / 评分 / 申诉 / 教师统计 =================
$teacher = Api "UserCreate" @{ workspace_id = $wsid; display_name = "冒烟教师"; role = "teacher" }
$cls = Api "ClassCreate" @{ workspace_id = $wsid; user_id = $teacher.id; name = "冒烟班级"; subject = "物理"; semester = "2026春"; idempotency_key = "smoke-cls-$([guid]::NewGuid())" }
Check "班级创建" ($cls.id -and $cls.status -eq "active")

Api "ClassMemberAdd" @{ workspace_id = $wsid; user_id = $teacher.id; class_id = $cls.id; student_user_id = $uid; idempotency_key = "smoke-cma-$([guid]::NewGuid())" } | Out-Null
$members = Api "ClassMemberList" @{ workspace_id = $wsid; user_id = $teacher.id; class_id = $cls.id }
Check "学生加入班级" ($members.Count -eq 1 -and $members[0].student_user_id -eq $uid)

$asg = Api "AssignmentCreate" @{
  workspace_id = $wsid; user_id = $teacher.id; class_id = $cls.id; paper_id = $paper.id
  title = "冒烟作业"; due_at = "2030-01-01T00:00:00Z"; grading_rule = "auto"
  idempotency_key = "smoke-asg-$([guid]::NewGuid())"
}
$asgPub = Api "AssignmentPublish" @{ workspace_id = $wsid; user_id = $teacher.id; assignment_id = $asg.id; version = $asg.version }
$answers = @($published.items | ForEach-Object { @{ question_version_id = $_.current_version.id; answer = $answerMap[$_.id] } })
$sub = Api "AssignmentSubmit" @{ workspace_id = $wsid; user_id = $uid; assignment_id = $asg.id; answers = $answers; idempotency_key = "smoke-sub-$([guid]::NewGuid())" }
Check "作业发布+学生提交" ($asgPub.status -eq "published" -and $sub.student_user_id -eq $uid)

$graded = Api "AssignmentGrade" @{ workspace_id = $wsid; user_id = $teacher.id; submission_id = $sub.id; pre_grade = $true; version = 0 }
Check "教师预评分" ($graded.graded_at -ne $null)

$appeal = Api "AppealCreate" @{ workspace_id = $wsid; user_id = $uid; grading_id = $sub.id; reason = "第1题答案应得分，请求复议" }
Check "学生申诉" ($appeal.status -eq "pending")
$appealRes = Api "AppealResolve" @{ workspace_id = $wsid; user_id = $teacher.id; appeal_id = $appeal.id; decision = "accepted"; new_score = 30 }
Check "教师复议通过" ($appealRes.status -eq "resolved")

$stats = Api "ClassStats" @{ workspace_id = $wsid; user_id = $teacher.id; class_id = $cls.id; assignment_id = $asg.id }
Check "教师统计作业提交" ($stats.student_total -ge 1 -and $stats.assignment_total -ge 1)

# ================= P6: 管理端 / 家庭 / 分享 / 插件 / Webhook / Provider / 社区 / 求题 =================
$admin = Api "UserCreate" @{ workspace_id = $wsid; display_name = "冒烟管理员"; role = "admin" }
$adminReview = Api "AdminReviewList" @{ workspace_id = $wsid; status = "" }
Check "管理端审核队列可查" ($adminReview.total -ge 0)

# 求题闭环 → 生成草稿 → 管理端审核
$req = Api "ContentRequestCreate" @{ workspace_id = $wsid; user_id = $uid; knowledge_ids = @($kn.id); description = "求 5 道浮力选择题"; idempotency_key = "smoke-cr-$([guid]::NewGuid())" }
$gen = Api "ContentRequestGenerate" @{ workspace_id = $wsid; user_id = $uid; request_id = $req.id; count = 2; idempotency_key = "smoke-crg-$([guid]::NewGuid())" }
Check "求题生成草稿(无Provider降级模板)" ($gen.question_count -ge 1)
$queue = Api "AdminReviewList" @{ workspace_id = $wsid; status = "pending" }
Check "管理端待审队列含求题" ($queue.total -ge 1)

# 家庭：家长角色需先以非家长创建用户再改角色（UserCreate 白名单无 parent）
$parent = Api "UserCreate" @{ workspace_id = $wsid; display_name = "冒烟家长"; role = "student" }
$invite = Api "FamilyInviteCreate" @{ workspace_id = $wsid; user_id = $uid; idempotency_key = "smoke-fi-$([guid]::NewGuid())" }
Check "家庭邀请码生成" ($invite.code -and $invite.status -eq "pending")

# 分享题目 → 解析 → 撤销
$share = Api "ShareCreate" @{ workspace_id = $wsid; user_id = $uid; ref_type = "question"; ref_id = $published.items[0].id; ttl_days = 7; idempotency_key = "smoke-sh-$([guid]::NewGuid())" }
$resolved = Api "ShareResolve" @{ token = $share.token }
$revoked = Api "ShareRevoke" @{ workspace_id = $wsid; user_id = $uid; share_id = $share.id }
Check "分享创建+解析+撤销" ($resolved.share.ref_id -eq $published.items[0].id -and $revoked.deleted -eq $true)

# 插件市场（bind0 无请求体）：curl 直取原始 JSON——Api 辅助函数会把空数组展开成 $null，无法区分"空列表"与"失败"
$marketRaw = curl.exe -s -X POST "$BaseUrl/api/v1/PluginMarketList" -H "Content-Type: application/json" -d '{}'
$marketEnv = $marketRaw | ConvertFrom-Json
Check "插件市场列表" ($null -eq $marketEnv.error -and $marketEnv.data -is [System.Array])

# Webhook：订阅 + 死链测试（ok=false 确定性）
$wh = Api "WebhookSubscribe" @{ workspace_id = $wsid; url = "http://127.0.0.1:9/hook"; event_types = @("report:ready", "exam:auto_submitted"); idempotency_key = "smoke-wh-$([guid]::NewGuid())" }
$whTest = Api "WebhookTestSend" @{ workspace_id = $wsid; subscription_id = $wh.id }
$whList = Api "WebhookList" @{ workspace_id = $wsid }
Check "Webhook订阅+测试(死链ok=false)+列表" ($wh.enabled -and -not $whTest.ok -and @($whList).Count -ge 1)

# Provider：mock 配置（enabled=true 否则会被当作禁用而清除）→ SettingsGet 反映 configured
$prov = Api "ProviderConfigure" @{ workspace_id = $wsid; provider = "llm"; kind = "mock"; model = "mock-1"; enabled = $true }
$settings2 = Api "SettingsGet" @{ workspace_id = $wsid }
Check "Provider mock 配置生效" ($settings2.provider_status.llm.configured)

# 社区帖子 + 点赞
$post = Api "CommunityPostCreate" @{ workspace_id = $wsid; author_user_id = $uid; title = "冒烟求助帖"; body_md = "怎么记浮力公式？" }
$postList = Api "CommunityPostList" @{ workspace_id = $wsid }
$like = Api "CommunityPostLike" @{ workspace_id = $wsid; post_id = $post.id }
Check "社区发帖+列表+点赞" ($post.id -and @($postList).Count -ge 1 -and $like.likes -ge 1)

# ================= P7: 云同步设备管理 =================
$reg2 = Api "SyncDeviceRegister" @{ workspace_id = $wsid; device_id = "smoke-device-$([guid]::NewGuid().ToString('N').Substring(0,8))"; device_name = "冒烟机B"; platform = "windows"; app_version = "2.0.0" }
$devList = Api "SyncDeviceList" @{ workspace_id = $wsid }
Check "工作区设备列表" ($devList.devices.Count -ge 1)
$cloudPush = ApiErr "SyncCloudPush" @{ workspace_id = $wsid }
Check "云同步未配置token回退(FEATURE_DISABLED)" ($cloudPush -eq "FEATURE_DISABLED")

# ================= P8: 专注 / 打卡成就 / 提醒 / 报告 / 健康 / 日历 =================
$timer = Api "TimerStart" @{ workspace_id = $wsid; user_id = $uid; mode = "pomodoro"; planned_minutes = 25; idempotency_key = "smoke-tmr-$([guid]::NewGuid())" }
$timerEnd = Api "TimerEnd" @{ workspace_id = $wsid; user_id = $uid; session_id = $timer.id; interrupt_reason = "冒烟测试结束" }
$tstats = Api "TimerStats" @{ workspace_id = $wsid; user_id = $uid }
Check "专注计时开始/中断/统计" ($timerEnd.status -eq "interrupted" -and $tstats.total_sessions -ge 1)

# 打卡：今天一次 → streak≥1；成就列表存在
$ckin = Api "CheckinCreate" @{ workspace_id = $wsid; user_id = $uid; minutes = 30; idempotency_key = "smoke-ck-$([guid]::NewGuid())" }
$streak = Api "StreakGet" @{ workspace_id = $wsid; user_id = $uid }
$ach = Api "AchievementList" @{ workspace_id = $wsid; user_id = $uid }
Check "打卡+连续天数+成就" ($ckin.date -and $streak.streak -ge 1 -and $ach.Count -ge 1)

# 提醒：Upsert(interval) + TestSend
$rem = Api "ReminderUpsert" @{ workspace_id = $wsid; user_id = $uid; kind = "review"; rule_json = '{"type":"interval","minutes":30,"repeat":true}'; enabled = $true }
$remTest = Api "ReminderTestSend" @{ workspace_id = $wsid; user_id = $uid; kind = "review" }
Check "提醒规则+测试发送" ($rem.id -and $remTest.ok)

# 报告：daily（period_start/period_end 至少其一）
$todayStr = (Get-Date).ToString("yyyy-MM-dd")
$report = Api "ReportGenerate" @{ workspace_id = $wsid; user_id = $uid; period = "daily"; period_start = $todayStr; period_end = $todayStr; idempotency_key = "smoke-rpt-$([guid]::NewGuid())" }
$reportList = Api "ReportList" @{ workspace_id = $wsid; user_id = $uid; period = "daily" }
Check "日报生成+列表" ($report.status -eq "ready" -and $reportList.items.Count -ge 1)

# 健康设置 + 统计（night_mode 必填：auto|light|dark|custom）
$health = Api "HealthSettingsUpdate" @{ workspace_id = $wsid; user_id = $uid; sedentary_enabled = $true; eye_enabled = $false; night_mode = "auto"; stats_enabled = $true }
$healthStats = Api "HealthStatsGet" @{ workspace_id = $wsid; user_id = $uid }
Check "健康设置+统计" ($health.sedentary_enabled -and $healthStats -ne $null)

# 日历
$cal = Api "CalendarGetMonth" @{ workspace_id = $wsid; user_id = $uid; month = (Get-Date).ToString("yyyy-MM") }
Check "日历月视图" ($cal -ne $null -and $null -ne $cal.entries)

Write-Output ""
if ($fail -eq 0) { Write-Output "=== 冒烟全部通过 ===" } else { Write-Output "=== 冒烟失败 $fail 项 ==="; exit 1 }
