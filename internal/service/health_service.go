package service

import (
	"context"
	"math"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// HealthService 实现健康与专注辅助用例（API 文档 7.16 / 完整设计文档 4.17）。
// Now 可注入时钟：测试中推进时间触发久坐评估判定。
type HealthService struct {
	s   *Services
	Now func() time.Time
}

// healthRuleInterval45 是久坐提醒的默认规则（4.17 H1：默认每 45 分钟提醒，重复）。
// 间隔固定为 SedentaryThresholdMinutes（45），与 health_settings 无间隔列的设计一致
// （间隔经 reminders.rule_json 承载，见 Todo 18 决策）。
const healthRuleInterval45 = `{"type":"interval","minutes":45,"repeat":true}`

// HealthSettings 是健康设置 DTO（health_settings 表行）。
type HealthSettings struct {
	WorkspaceID      string `json:"workspace_id"`
	UserID           string `json:"user_id"`
	SedentaryEnabled bool   `json:"sedentary_enabled"`
	EyeEnabled       bool   `json:"eye_enabled"`
	NightMode        string `json:"night_mode"`
	BlueLightFilter  bool   `json:"blue_light_filter"`
	StatsEnabled     bool   `json:"stats_enabled"`
	UpdatedAt        string `json:"updated_at"`
}

// HealthSettingsUpdateReq 健康设置更新请求（命令型 RPC，必须携带 user_id 并服务端校验）。
// 不加 idempotency_key：设置类天然幂等 upsert（Todo 18 决策，同 ReminderUpsert/CheckinCreate）。
type HealthSettingsUpdateReq struct {
	WorkspaceID      string `json:"workspace_id"`
	UserID           string `json:"user_id"`
	SedentaryEnabled bool   `json:"sedentary_enabled"`
	EyeEnabled       bool   `json:"eye_enabled"`
	NightMode        string `json:"night_mode"`
	BlueLightFilter  bool   `json:"blue_light_filter"`
	StatsEnabled     bool   `json:"stats_enabled"`
}

// HealthStatsGetReq 健康统计请求（start_date/end_date 为 YYYY-MM-DD，均留空 = 全部时间）。
type HealthStatsGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
}

// HealthStats 是健康统计聚合（4.17 H6：久坐次数/休息完成率，仅开启时采集）。
type HealthStats struct {
	StatsEnabled       bool    `json:"stats_enabled"`
	SedentaryCount     int     `json:"sedentary_count"`
	RestCompletionRate float64 `json:"rest_completion_rate"`
}

// HealthSettingsUpdate 新增或更新健康设置（复合主键 upsert 语义）。
// 校验：工作区存在 + user_id 必填 + night_mode 枚举合法。
// 副作用：sedentary_enabled 开启时 upsert kind=health 提醒（interval 45，enabled），
// 关闭时禁用它；开启后若已连续久坐 ≥45 分钟则拉前 next_trigger_at 尽快触发。
func (h *HealthService) HealthSettingsUpdate(ctx context.Context, req HealthSettingsUpdateReq) (*HealthSettings, error) {
	if err := h.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidNightMode(req.NightMode) {
		return nil, domain.InvalidArg("night_mode 须为 auto|light|dark|custom")
	}
	now := h.Now().UTC().Format(time.RFC3339)
	row := &repository.HealthSettingsRow{
		WorkspaceID: req.WorkspaceID, UserID: req.UserID,
		SedentaryEnabled: boolToInt(req.SedentaryEnabled),
		EyeEnabled:       boolToInt(req.EyeEnabled),
		NightMode:        req.NightMode,
		BlueLightFilter:  boolToInt(req.BlueLightFilter),
		StatsEnabled:     boolToInt(req.StatsEnabled),
		UpdatedAt:        now,
	}
	if err := h.s.Repo.UpsertHealthSettings(ctx, row); err != nil {
		return nil, err
	}
	// 同步久坐提醒：开启 → upsert enabled；关闭 → 禁用
	if err := h.syncSedentaryReminder(ctx, req.WorkspaceID, req.UserID, req.SedentaryEnabled); err != nil {
		return nil, err
	}
	// 开启后立即评估：若已连续 ≥45 分钟则尽快触发（QA 回填入口之一）
	if req.SedentaryEnabled {
		if _, err := h.EvaluateSedentary(ctx, req.WorkspaceID, req.UserID); err != nil {
			return nil, err
		}
	}
	fresh, err := h.s.Repo.GetHealthSettings(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, domain.Conflict("健康设置写入冲突，请重试")
	}
	h.s.audit(ctx, req.WorkspaceID, "health.settings_update", "health_settings", "",
		map[string]any{"sedentary_enabled": req.SedentaryEnabled, "eye_enabled": req.EyeEnabled,
			"night_mode": req.NightMode, "blue_light_filter": req.BlueLightFilter, "stats_enabled": req.StatsEnabled})
	return healthSettingsFromRow(fresh), nil
}

// HealthStatsGet 聚合健康统计（日期范围含边界，均留空 = 全部时间）。
// stats_enabled=0（或未开启采集）时返回禁用态（stats_enabled=false + 零值），非报错；
// 每次读取同时触发久坐重评估（QA 回填入口，更新 health 提醒 next_trigger_at）。
func (h *HealthService) HealthStatsGet(ctx context.Context, req HealthStatsGetReq) (*HealthStats, error) {
	if err := h.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	var start, end *string
	if req.StartDate != "" {
		if !domain.ValidDate(req.StartDate) {
			return nil, domain.InvalidArg("start_date 格式须为 YYYY-MM-DD")
		}
		start = &req.StartDate
	}
	if req.EndDate != "" {
		if !domain.ValidDate(req.EndDate) {
			return nil, domain.InvalidArg("end_date 格式须为 YYYY-MM-DD")
		}
		end = &req.EndDate
	}
	st, err := h.s.Repo.GetHealthSettings(ctx, req.WorkspaceID, req.UserID)
	if err != nil {
		return nil, err
	}
	statsEnabled := true // DDL 默认 stats_enabled=1（Todo 18 决策：以 DDL 为准，非设计文档「默认关闭」）
	if st != nil {
		statsEnabled = st.StatsEnabled == 1
	}
	if !statsEnabled {
		return &HealthStats{StatsEnabled: false}, nil
	}
	// QA 回填：重评估健康提醒（副作用，失败不阻断统计返回）
	_, _ = h.EvaluateSedentary(ctx, req.WorkspaceID, req.UserID)

	windows, err := h.sedentaryWindows(ctx, req.UserID, start, end)
	if err != nil {
		return nil, err
	}
	var count, rested int
	for _, w := range windows {
		if w.Minutes >= domain.SedentaryThresholdMinutes {
			count++
			if w.Rested {
				rested++
			}
		}
	}
	rate := 0.0
	if count > 0 {
		rate = math.Round(float64(rested)/float64(count)*10000) / 10000
	}
	return &HealthStats{StatsEnabled: true, SedentaryCount: count, RestCompletionRate: rate}, nil
}

// EvaluateSedentary 是久坐评估的共享入口（TimerEnd 钩子 / HealthStatsGet / ReminderTestSend
// 三处调用同一逻辑）：读取用户已结束专注会话，计算连续久坐窗口；当最长窗口达到阈值
// （≥45 分钟）且 health 提醒存在并启用时，把 next_trigger_at 拉前到当前时间令调度尽快触发。
// 返回是否发生拉前。
func (h *HealthService) EvaluateSedentary(ctx context.Context, wsID, userID string) (bool, error) {
	rem, err := h.s.Repo.GetReminder(ctx, userID, domain.ReminderKindHealth)
	if err != nil {
		return false, err
	}
	if rem == nil || rem.Enabled != 1 {
		// 未开启久坐提醒：不评估（提醒的创建/启停由 HealthSettingsUpdate 驱动）
		return false, nil
	}
	windows, err := h.sedentaryWindows(ctx, userID, nil, nil)
	if err != nil {
		return false, err
	}
	max := 0
	for _, w := range windows {
		if w.Minutes > max {
			max = w.Minutes
		}
	}
	if max < domain.SedentaryThresholdMinutes {
		return false, nil
	}
	now := h.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	// 仅当当前 next_trigger_at 仍指向未来时拉前（已到期则由调度自然触发，避免反复拉前）
	if rem.NextTriggerAt <= nowStr {
		return false, nil
	}
	updated := &repository.ReminderRow{
		ID: rem.ID, WorkspaceID: rem.WorkspaceID, UserID: rem.UserID, Kind: rem.Kind,
		RuleJSON: rem.RuleJSON, Enabled: 1, NextTriggerAt: nowStr, UpdatedAt: nowStr,
	}
	if err := h.s.Repo.UpdateReminder(ctx, updated); err != nil {
		return false, err
	}
	h.s.audit(ctx, wsID, "health.sedentary_eval", "reminder", rem.ID,
		map[string]any{"continuous_minutes": max})
	return true, nil
}

// syncSedentaryReminder 按 sedentary_enabled 同步 kind=health 提醒：
// 开启 → ReminderUpsert(enabled=true, interval 45)；关闭 → ReminderUpsert(enabled=false)。
// 复用提醒模块保证调度器能按同一 (user_id, kind) 规则触发。
func (h *HealthService) syncSedentaryReminder(ctx context.Context, wsID, userID string, enabled bool) error {
	_, err := h.s.Reminder.ReminderUpsert(ctx, ReminderUpsertReq{
		WorkspaceID: wsID, UserID: userID, Kind: domain.ReminderKindHealth,
		RuleJSON: healthRuleInterval45, Enabled: enabled,
	})
	return err
}

// sedentaryWindows 将用户已结束专注会话转换为连续久坐窗口（仅供评估与统计复用）。
func (h *HealthService) sedentaryWindows(ctx context.Context, userID string, start, end *string) ([]domain.SedentaryWindow, error) {
	sessions, err := h.s.Repo.ListTimerSessionsInRange(ctx, userID, start, end)
	if err != nil {
		return nil, err
	}
	spans := make([]domain.SessionSpan, 0, len(sessions))
	for _, s := range sessions {
		if s.StartedAt == nil || s.EndedAt == nil {
			continue
		}
		st, err1 := time.Parse(time.RFC3339, *s.StartedAt)
		en, err2 := time.Parse(time.RFC3339, *s.EndedAt)
		if err1 != nil || err2 != nil {
			continue
		}
		spans = append(spans, domain.SessionSpan{Start: st, End: en})
	}
	return domain.SedentaryWindows(spans), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func healthSettingsFromRow(row *repository.HealthSettingsRow) *HealthSettings {
	return &HealthSettings{
		WorkspaceID:      row.WorkspaceID,
		UserID:           row.UserID,
		SedentaryEnabled: row.SedentaryEnabled == 1,
		EyeEnabled:       row.EyeEnabled == 1,
		NightMode:        row.NightMode,
		BlueLightFilter:  row.BlueLightFilter == 1,
		StatsEnabled:     row.StatsEnabled == 1,
		UpdatedAt:        row.UpdatedAt,
	}
}
