# Lumo AI 一键开发脚本：环境检测 → 依赖安装 → watchdog 启动前后端。
#  - 环境检测：自动检测 Go / Node.js / npm / air；缺失时通过 winget 自动安装并写入 PATH（用户级环境变量）
#  - 后端：air 监控 .go 文件，改动自动重新编译并重启（watchdog 模式，实时生效）
#  - 前端：npm install 安装依赖后以 npm run dev 启动 vite dev server（HMR 实时热更新）
#  - 全部服务随本脚本退出（Ctrl+C）自动清理
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\dev.ps1 -Yes   # 非交互：缺失工具自动 winget 安装，不询问
# 参数：
#   -SkipInstall   跳过依赖初始化（go mod download / npm install / air 安装，仅做环境检测）
#   -NoBrowser     不自动打开浏览器
#   -Yes           非交互模式：环境缺失时直接用 winget 安装，不弹出选项询问
#   -Port <int>    前端 dev server 端口（默认 5173，仅用于浏览器地址与占用检查）
param(
  [switch]$SkipInstall,
  [switch]$NoBrowser,
  [switch]$Yes,
  [int]$Port = 5173
)

$ErrorActionPreference = "Stop"

$root     = Split-Path -Parent $PSScriptRoot          # 项目根目录
$frontend = Join-Path $root "frontend"
$logDir   = Join-Path $env:TEMP "lumo-dev"
$backLog  = Join-Path $logDir "backend.log"
$frontLog = Join-Path $logDir "frontend.log"
$BackendUrl  = "http://127.0.0.1:8787"
# vite 默认绑定 localhost（IPv6 优先），故前端地址用 localhost
$FrontendUrl = "http://localhost:$Port"

function Log($msg) { Write-Host "[dev] $msg" -ForegroundColor Cyan }

# ---------- 环境检测 ----------

# 查找 go.exe：优先 PATH，其次常见安装目录（winget / 官方安装包）
function Find-Go {
  $cmd = Get-Command go -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  $cands = @(
    "$env:ProgramFiles\Go\bin\go.exe",
    "$env:LOCALAPPDATA\Programs\Go\bin\go.exe",
    "$env:USERPROFILE\go\bin\go.exe"
  )
  foreach ($c in $cands) { if (Test-Path $c) { return $c } }
  return $null
}

# 查找 node.exe / npm.cmd：优先 PATH，其次常见安装目录
function Find-Node {
  $cmd = Get-Command node -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  $cands = @(
    "$env:ProgramFiles\nodejs\node.exe",
    "$env:LOCALAPPDATA\Programs\nodejs\node.exe"
  )
  foreach ($c in $cands) { if (Test-Path $c) { return $c } }
  return $null
}

# 查找 air：优先 PATH，其次 GOPATH/bin 兜底
function Find-Air {
  $cmd = Get-Command air -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  $go = Find-Go
  if ($go) {
    $gopath = (& $go env GOPATH).Trim()
    $cand = Join-Path (Join-Path $gopath "bin") "air.exe"
    if (Test-Path $cand) { return $cand }
  }
  return $null
}

# 将目录写入当前会话 PATH，并（可选）持久化到用户级环境变量
function Add-ToPath($dir, [switch]$PersistUser) {
  if (-not $dir -or -not (Test-Path $dir)) { return }
  if ($env:Path -notlike "*$dir*") {
    $env:Path = $env:Path.TrimEnd(';') + ';' + $dir
    Log "已加入当前会话 PATH: $dir"
  }
  if ($PersistUser) {
    $up = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($up -notlike "*$dir*") {
      [Environment]::SetEnvironmentVariable('Path', $up.TrimEnd(';') + ';' + $dir, 'User')
      Log "已写入用户环境变量 PATH: $dir"
    }
  }
}

# 确保 winget 可用（仅当需要安装时调用）
function Ensure-Winget {
  if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
    throw "未找到 winget。请先通过 Microsoft Store 安装『应用安装程序 (App Installer)』，或手动安装 Go / Node.js 后重试。"
  }
}

# 命令行交互询问：1=自动安装  2=跳过
function Confirm-Install($tool, $hint) {
  if ($Yes) { return $true }
  Write-Host ""
  Write-Host "[检测] 未找到 $tool —— $hint" -ForegroundColor Yellow
  Write-Host "  请选择操作：" -ForegroundColor White
  Write-Host "    1) 使用 winget 自动安装（推荐）" -ForegroundColor Green
  Write-Host "    2) 跳过，我自己手动安装（后续启动可能失败）" -ForegroundColor DarkGray
  while ($true) {
    $choice = Read-Host "  请输入选项 [1/2]"
    if ($choice.Trim() -eq "1") { return $true }
    if ($choice.Trim() -eq "2") { return $false }
    Write-Host "  输入无效，请输入 1 或 2。" -ForegroundColor Yellow
  }
}

# 调用 winget 安装包（静默 + 自动同意协议；UAC 提升弹窗仍需用户确认）
function Install-WithWinget($id, $displayName) {
  Ensure-Winget
  Log "正在使用 winget 安装 $displayName ($id) ..."
  Write-Host "  （如弹出 UAC 用户账户控制窗口，请点击『是』允许安装）" -ForegroundColor DarkGray
  & winget install --id $id -e --silent --accept-source-agreements --accept-package-agreements
  if ($LASTEXITCODE -ne 0) {
    throw "winget 安装 $displayName 失败（退出码 $LASTEXITCODE）。请手动安装后重试，或打开新终端再运行本脚本。"
  }
  Log "$displayName 安装完成"
}

# 检测并确保 Go 可用（缺失 → 询问 → winget 安装 → 写入 PATH）
function Ensure-Go {
  $go = Find-Go
  if ($go) {
    Add-ToPath (Split-Path $go) -PersistUser
    Log "Go 已就绪: $go"
    return $go
  }
  if (-not (Confirm-Install "Go (Golang)" "后端编译与运行必需（go build / go run）")) {
    throw "未安装 Go，无法启动后端。请安装 Go (https://go.dev/dl/) 后重新运行本脚本。"
  }
  Install-WithWinget "GoLang.Go" "Go"
  $go = Find-Go
  if (-not $go) { throw "Go 安装后仍未检测到，请打开新终端后重试本脚本。" }
  Add-ToPath (Split-Path $go) -PersistUser
  Log "Go 已就绪: $go"
  return $go
}

# 检测并确保 Node.js / npm 可用（缺失 → 询问 → winget 安装 → 写入 PATH）
function Ensure-Node {
  $node = Find-Node
  if ($node) {
    Add-ToPath (Split-Path $node) -PersistUser
    Log "Node.js 已就绪: $node"
  } else {
    if (-not (Confirm-Install "Node.js / npm" "前端构建与 dev server 必需（npm run dev）")) {
      throw "未安装 Node.js / npm，无法启动前端。请安装 Node.js LTS (https://nodejs.org/) 后重新运行本脚本。"
    }
    Install-WithWinget "OpenJS.NodeJS.LTS" "Node.js LTS"
    $node = Find-Node
    if (-not $node) { throw "Node.js 安装后仍未检测到，请打开新终端后重试本脚本。" }
    Add-ToPath (Split-Path $node) -PersistUser
    Log "Node.js 已就绪: $node"
  }
  $npm = Get-Command npm.cmd -ErrorAction SilentlyContinue
  if (-not $npm) {
    # PATH 已含 nodejs 目录，理论上 npm.cmd 必在；此处兜底
    $npmCand = Join-Path (Split-Path $node) "npm.cmd"
    if (Test-Path $npmCand) { $npm = Get-Item $npmCand } else { throw "未找到 npm（Node.js 已安装但 npm 不可用）" }
  }
  return @{ node = $node; npm = $npm.Source }
}

# 前端依赖是否真实就绪（node_modules 存在且关键包齐全）
function Test-FrontendDepsReady {
  $nm = Join-Path $frontend "node_modules"
  if (-not (Test-Path $nm)) { return $false }
  foreach ($pkg in @("vue", "vue-router", "pinia", "vite", "typescript", "vue-tsc", "@vitejs/plugin-vue")) {
    if (-not (Test-Path (Join-Path $nm $pkg))) { return $false }
  }
  return $true
}

function Test-PortInUse($port) {
  return $null -ne (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
}

# 杀进程树（air->lumo-server、npm->vite 连带清理）
function Stop-ProcessTree($proc) {
  if ($null -eq $proc) { return }
  if (-not $proc.HasExited) {
    & taskkill /PID $proc.Id /T /F 2>$null | Out-Null
  }
}

# 增量读取日志文件并打印
function Read-LogDelta($path, [ref]$pos, $prefix) {
  if (-not (Test-Path $path)) { return }
  $fs = [System.IO.File]::Open($path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
  try {
    if ($fs.Length -lt $pos.Value) { $pos.Value = 0 }
    $fs.Position = $pos.Value
    $sr = New-Object System.IO.StreamReader($fs)
    while (-not $sr.EndOfStream) { Write-Host "$prefix $($sr.ReadLine())" }
    $pos.Value = $fs.Position
  } finally { $fs.Dispose() }
}

# ================= 1. 环境检测与安装 =================
Log "Lumo 一键开发环境初始化"
Write-Host "  [1/3] 检测运行环境：Go / Node.js / npm ..." -ForegroundColor DarkGray

# Go（后端编译运行；缺失则 winget 安装并写入 PATH）
$go = Ensure-Go
Log "Go 版本: $(& $go version)"
$goBin = Split-Path $go
Add-ToPath $goBin -PersistUser
$env:GOPATH = (& $go env GOPATH).Trim()

# Node.js / npm（前端；缺失则 winget 安装并写入 PATH）
Write-Host "  [2/3] 检测前端环境：Node.js / npm ..." -ForegroundColor DarkGray
$nodeEnv = Ensure-Node
$npmPath = $nodeEnv.npm
Log "Node 版本: $(& $nodeEnv.node --version)  npm: $(& $npmPath --version)"

# ================= 2. 依赖初始化 =================
if (-not $SkipInstall) {
  Write-Host "  [3/3] 初始化项目依赖 ..." -ForegroundColor DarkGray

  # 后端 Go 依赖（go.mod）
  Log "初始化后端依赖 (go mod download) ..."
  Push-Location $root
  try {
    & go mod download
    if ($LASTEXITCODE -ne 0) {
      # 默认代理不可达（常见于国内网络）时，切换国内镜像重试
      Log "go mod download 失败（网络问题？），改用 https://goproxy.cn 重试 ..."
      $env:GOPROXY = "https://goproxy.cn,direct"
      & go mod download
      if ($LASTEXITCODE -ne 0) { throw "go mod download 失败，请检查网络后重试" }
    }
  } finally { Pop-Location }

  # 前端 npm 依赖（frontend/node_modules）
  if (Test-FrontendDepsReady) {
    Log "前端依赖已就绪，跳过 npm install（如需强制重装，请删除 frontend\node_modules 后重跑）"
  } else {
    Log "安装前端依赖 (npm install) ...（如网络慢，将自动切换国内镜像重试）"
    Push-Location $frontend
    try {
      & $npmPath install
      if ($LASTEXITCODE -ne 0) {
        Log "npm install 失败，改用国内镜像 registry.npmmirror.com 重试 ..."
        & $npmPath install --registry=https://registry.npmmirror.com
        if ($LASTEXITCODE -ne 0) { throw "npm install 失败，请检查网络后重试" }
      }
    } finally { Pop-Location }
    if (-not (Test-FrontendDepsReady)) { throw "前端依赖安装后仍不完整，请检查 frontend/package.json 后重试" }
  }

  # 后端 watchdog 工具 air
  $air = Find-Air
  if (-not $air) {
    Log "未检测到 air，正在安装后端 watchdog 工具 (go install github.com/air-verse/air@latest) ..."
    & go install github.com/air-verse/air@latest 2>$null
    if (-not (Find-Air)) {
      Log "默认 Go 代理不可达，改用 https://goproxy.cn 重试 ..."
      $env:GOPROXY = "https://goproxy.cn,direct"
      & go install github.com/air-verse/air@latest
    }
    $air = Find-Air
    if (-not $air) { throw "air 安装失败，请手动执行: go install github.com/air-verse/air@latest（网络受限时先设置 GOPROXY=https://goproxy.cn,direct）" }
  }
} else {
  $air = Find-Air
  if (-not $air) { throw "未找到 air，且已指定 -SkipInstall，无法启动后端 watchdog" }
}
Log "后端 watchdog (air): $air"

# ================= 3. 端口检查 =================
if (Test-PortInUse 8787) { throw "端口 8787 已被占用，可能已有后端在运行。请先停止后再启动。" }
if (Test-PortInUse $Port) { throw "端口 $Port 已被占用，可能已有前端 dev server 在运行。请先停止后再启动。" }

# ================= 4. 启动前后端 =================
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
Remove-Item "$backLog*", "$frontLog*" -ErrorAction SilentlyContinue

Log "启动后端 watchdog（air 监控 .go 文件，改动自动重新编译重启）..."
$backProc = Start-Process -FilePath $air -WorkingDirectory $root -NoNewWindow `
  -RedirectStandardOutput $backLog -RedirectStandardError "$backLog.err" -PassThru

Log "启动前端 dev server（npm run dev → vite HMR，改动浏览器实时热更新）..."
$frontProc = Start-Process -FilePath $npmPath -ArgumentList @("run", "dev") `
  -WorkingDirectory $frontend -NoNewWindow `
  -RedirectStandardOutput $frontLog -RedirectStandardError "$frontLog.err" -PassThru

if (-not $NoBrowser) {
  Start-Sleep -Seconds 3
  Log "正在打开浏览器: $FrontendUrl"
  Start-Process $FrontendUrl | Out-Null
}

Log "======== 开发环境已就绪 ========"
Log "前端页面: $FrontendUrl"
Log "后端 API: $BackendUrl"
Log "日志目录: $logDir"
Log "按 Ctrl+C 停止全部服务（后端改动自动重启，前端改动自动热更新）"
Log "================================"

# ================= 5. 前台监控（实时显示两边日志，退出时统一清理） =================
$bpos = 0; $fpos = 0; $bepos = 0; $fepos = 0
try {
  while ($true) {
    Read-LogDelta $backLog   ([ref]$bpos)  "[后端]"
    Read-LogDelta $frontLog  ([ref]$fpos)  "[前端]"
    Read-LogDelta "$backLog.err"  ([ref]$bepos) "[后端!]"
    Read-LogDelta "$frontLog.err" ([ref]$fepos) "[前端!]"
    if ($backProc.HasExited)  { throw "后端进程已退出，退出码 $($backProc.ExitCode)。日志: $backLog" }
    if ($frontProc.HasExited) { throw "前端进程已退出，退出码 $($frontProc.ExitCode)。日志: $frontLog" }
    Start-Sleep -Milliseconds 500
  }
} finally {
  Write-Host ""
  Log "正在停止全部服务 ..."
  Stop-ProcessTree $backProc
  Stop-ProcessTree $frontProc
  Log "已停止。日志保留在: $logDir"
}
