@echo off
rem Lumo AI 一键开发入口：双击本文件或命令行运行。
rem 初始化依赖 + watchdog 启动前后端（后端 air 热重载，前端 vite HMR），Ctrl+C 停止全部服务。
cd /d "%~dp0"
where pwsh >nul 2>nul
if %errorlevel%==0 (
  pwsh -NoProfile -ExecutionPolicy Bypass -File scripts\dev.ps1 %*
) else (
  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\dev.ps1 %*
)
