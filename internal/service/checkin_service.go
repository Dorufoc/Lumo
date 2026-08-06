package service

import (
	"context"
	"encoding/json"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// Checkin 是打卡 DTO（API 文档 7.4 Checkin；checkins 表无 workspace_id，DTO 不含该字段）。
type Checkin struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Date      string `json:"date"`
	Kind      string `json:"kind"`
	Minutes   int    `json:"minutes"`
	CreatedAt string `json:"created_at"`
}

// Streak 是连续天数投影 DTO（API 文档 7.4 Streak）。
type Streak struct {
	UserID        string `json:"user_id"`
	Date          string `json:"date"`
	Streak        int    `json:"streak"`
	TotalCheckins int    `json:"total_checkins"`
}

// AchievementView 是成就视图 DTO（已解锁/未解锁，4.11 徽章墙）。
type AchievementView struct {
	ID             string  `json:"id"`
	Code           string  `json:"code"`
	TitleKey       string  `json:"title_key"`
	DescriptionKey string  `json:"description_key"`
	Icon           string  `json:"icon"`
	IsUnlocked     bool    `json:"is_unlocked"`
	AwardedAt      *string `json:"awarded_at"`
}

// CheckinCreateReq 每日打卡请求。
type CheckinCreateReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Minutes        int    `json:"minutes"`
	IdempotencyKey string `json:"idempotency_key"`
}

// CheckinMakeupReq 补签请求（date 为过去日期）。
type CheckinMakeupReq struct {
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Date           string `json:"date"`
	Minutes        int    `json:"minutes"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AchievementListReq 成就列表请求。
type AchievementListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// StreakGetReq 连续天数查询请求。
type StreakGetReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

// CheckinService 实现打卡与成就用例（API 文档 7.4 / 完整设计文档 4.11）。
// Now 可注入时钟：测试中推进日期触发 streak / 补签规则。
type CheckinService struct {
	s   *Services
	Now func() time.Time
}

// 固定成就模板（4.11 A3：徽章定义集中管理、固定模板）。title_key/description_key 即 i18n key 路径。
var achievementTemplates = []*repository.AchievementDefRow{
	{Code: "first_checkin", TitleKey: "achievement.firstCheckin.title", DescriptionKey: "achievement.firstCheckin.description", TriggerRuleJSON: `{"type":"total_checkins","threshold":1}`, Icon: "🌱"},
	{Code: "streak_3", TitleKey: "achievement.streak3.title", DescriptionKey: "achievement.streak3.description", TriggerRuleJSON: `{"type":"streak_days","threshold":3}`, Icon: "🔥"},
	{Code: "streak_7", TitleKey: "achievement.streak7.title", DescriptionKey: "achievement.streak7.description", TriggerRuleJSON: `{"type":"streak_days","threshold":7}`, Icon: "⚡"},
	{Code: "total_10", TitleKey: "achievement.total10.title", DescriptionKey: "achievement.total10.description", TriggerRuleJSON: `{"type":"total_checkins","threshold":10}`, Icon: "🎯"},
	{Code: "total_30", TitleKey: "achievement.total30.title", DescriptionKey: "achievement.total30.description", TriggerRuleJSON: `{"type":"total_checkins","threshold":30}`, Icon: "🏆"},
}

func checkinFromRow(c *repository.CheckinRow) *Checkin {
	return &Checkin{
		ID: c.ID, UserID: c.UserID, Date: c.Date,
		Kind: c.Kind, Minutes: c.Minutes, CreatedAt: c.CreatedAt,
	}
}

// CheckinCreate 每日打卡（user_id+date 幂等：同日期重复打卡返回已有记录，不重复计数）。
func (ck *CheckinService) CheckinCreate(ctx context.Context, req CheckinCreateReq) (*Checkin, error) {
	if err := ck.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if req.Minutes < 0 {
		return nil, domain.InvalidArg("minutes 不能为负")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(ck.s, ctx, req.WorkspaceID, req.IdempotencyKey, "CheckinCreate",
		func() (*Checkin, error) {
			return ck.doCheckin(ctx, req.WorkspaceID, req.UserID,
				domain.CheckinDate(ck.Now()), domain.CheckinKindNormal, req.Minutes)
		})
}

// CheckinMakeup 补签：date 必须为过去日期，每月限 3 次（4.11 A2）。
// 同 (user_id, date) 已存在（normal/makeup）→ 返回已有记录且不消耗配额。
func (ck *CheckinService) CheckinMakeup(ctx context.Context, req CheckinMakeupReq) (*Checkin, error) {
	if err := ck.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if !domain.ValidCheckinDate(req.Date) {
		return nil, domain.InvalidArg("date 格式须为 YYYY-MM-DD")
	}
	today := domain.CheckinDate(ck.Now())
	if req.Date >= today {
		return nil, domain.InvalidArg("补签仅支持过去的日期")
	}
	if req.Minutes < 0 {
		return nil, domain.InvalidArg("minutes 不能为负")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(ck.s, ctx, req.WorkspaceID, req.IdempotencyKey, "CheckinMakeup",
		func() (*Checkin, error) {
			return ck.doMakeup(ctx, req.WorkspaceID, req.UserID, req.Date, req.Minutes)
		})
}

// AchievementList 返回全部成就模板 + 当前用户解锁状态（4.11 徽章墙）。
func (ck *CheckinService) AchievementList(ctx context.Context, req AchievementListReq) ([]*AchievementView, error) {
	if err := ck.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if err := ck.ensureDefs(ctx); err != nil {
		return nil, err
	}
	defs, err := ck.s.Repo.ListAchievementDefs(ctx)
	if err != nil {
		return nil, err
	}
	unlocked, err := ck.s.Repo.ListUserAchievements(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*repository.UserAchievementRow, len(unlocked))
	for _, a := range unlocked {
		byID[a.AchievementID] = a
	}
	out := make([]*AchievementView, 0, len(defs))
	for _, d := range defs {
		v := &AchievementView{
			ID: d.ID, Code: d.Code, TitleKey: d.TitleKey,
			DescriptionKey: d.DescriptionKey, Icon: d.Icon,
		}
		if a, ok := byID[d.ID]; ok {
			v.IsUnlocked = true
			at := a.AwardedAt
			v.AwardedAt = &at
		}
		out = append(out, v)
	}
	return out, nil
}

// StreakGet 返回连续天数与总打卡数：优先读今日快照，无快照时按事实即时计算。
func (ck *CheckinService) StreakGet(ctx context.Context, req StreakGetReq) (*Streak, error) {
	if err := ck.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.UserID == "" {
		return nil, domain.InvalidArg("user_id 必填")
	}
	if err := ck.ensureDefs(ctx); err != nil {
		return nil, err
	}
	today := domain.CheckinDate(ck.Now())
	snap, err := ck.s.Repo.GetStreakSnapshot(ctx, req.UserID, today)
	if err != nil {
		return nil, err
	}
	if snap != nil {
		return &Streak{UserID: snap.UserID, Date: snap.Date, Streak: snap.Streak, TotalCheckins: snap.TotalCheckins}, nil
	}
	streak, anchor, total, err := ck.computeStreak(ctx, req.UserID, today)
	if err != nil {
		return nil, err
	}
	return &Streak{UserID: req.UserID, Date: ck.snapshotDate(anchor, today), Streak: streak, TotalCheckins: total}, nil
}

// doCheckin 打卡写事实 + 事件 + 重算投影 + 成就引擎；已存在则直接返回（幂等）。
func (ck *CheckinService) doCheckin(ctx context.Context, wsID, userID, date, kind string, minutes int) (*Checkin, error) {
	if err := ck.ensureDefs(ctx); err != nil {
		return nil, err
	}
	existing, err := ck.s.Repo.GetCheckin(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return checkinFromRow(existing), nil
	}
	row := &repository.CheckinRow{ID: NewID(), UserID: userID, Date: date, Kind: kind, Minutes: minutes}
	created, err := ck.s.Repo.CreateCheckin(ctx, row)
	if err != nil {
		return nil, err
	}
	if !created {
		return ck.returnExistingCheckin(ctx, userID, date)
	}
	fresh, err := ck.s.Repo.GetCheckin(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, domain.Conflict("打卡记录写入冲突，请重试")
	}
	if err := ck.recordUsage(ctx, wsID, userID, date, kind, minutes); err != nil {
		return nil, err
	}
	if err := ck.recomputeStreak(ctx, userID); err != nil {
		return nil, err
	}
	if err := ck.evaluateAchievements(ctx, wsID, userID, fresh.ID); err != nil {
		return nil, err
	}
	ck.s.audit(ctx, wsID, "checkin.create", "checkin", fresh.ID,
		map[string]any{"date": date, "kind": kind, "minutes": minutes})
	return checkinFromRow(fresh), nil
}

// doMakeup 补签：配额检查 + 写事实（user_id+date 幂等）。
func (ck *CheckinService) doMakeup(ctx context.Context, wsID, userID, date string, minutes int) (*Checkin, error) {
	if err := ck.ensureDefs(ctx); err != nil {
		return nil, err
	}
	existing, err := ck.s.Repo.GetCheckin(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return checkinFromRow(existing), nil
	}
	month := date[:7]
	count, err := ck.s.Repo.CountMakeupInMonth(ctx, userID, month)
	if err != nil {
		return nil, err
	}
	if count >= domain.CheckinMakeupMonthlyLimit {
		return nil, domain.QuotaExceeded("本月补签次数已达上限（%d 次）", domain.CheckinMakeupMonthlyLimit)
	}
	row := &repository.CheckinRow{
		ID: NewID(), UserID: userID, Date: date,
		Kind: domain.CheckinKindMakeup, Minutes: minutes,
	}
	created, err := ck.s.Repo.CreateCheckin(ctx, row)
	if err != nil {
		return nil, err
	}
	if !created {
		return ck.returnExistingCheckin(ctx, userID, date)
	}
	fresh, err := ck.s.Repo.GetCheckin(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, domain.Conflict("补签写入冲突，请重试")
	}
	if err := ck.recordUsage(ctx, wsID, userID, date, domain.CheckinKindMakeup, minutes); err != nil {
		return nil, err
	}
	if err := ck.recomputeStreak(ctx, userID); err != nil {
		return nil, err
	}
	if err := ck.evaluateAchievements(ctx, wsID, userID, fresh.ID); err != nil {
		return nil, err
	}
	ck.s.audit(ctx, wsID, "checkin.makeup", "checkin", fresh.ID,
		map[string]any{"date": date, "kind": domain.CheckinKindMakeup})
	return checkinFromRow(fresh), nil
}

// returnExistingCheckin 并发竞争兜底：读回已有记录；仍不存在则返回冲突。
func (ck *CheckinService) returnExistingCheckin(ctx context.Context, userID, date string) (*Checkin, error) {
	existing, err := ck.s.Repo.GetCheckin(ctx, userID, date)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return checkinFromRow(existing), nil
	}
	return nil, domain.Conflict("打卡记录写入冲突，请重试")
}

// recordUsage 打卡事实写入 usage_events（event_type='checkin'，只追加防篡改，4.11）。
func (ck *CheckinService) recordUsage(ctx context.Context, wsID, userID, date, kind string, minutes int) error {
	return ck.s.Repo.CreateUsageEvent(ctx, &repository.UsageEventRow{
		ID: NewID(), WorkspaceID: wsID, UserID: userID,
		EventType: "checkin",
		PayloadJSON: repository.MarshalJSON(map[string]any{
			"date": date, "kind": kind, "minutes": minutes,
		}),
		OccurredAt: Now(),
	})
}

// computeStreak 从打卡事实计算连续天数（streak, anchor）与总次数。
func (ck *CheckinService) computeStreak(ctx context.Context, userID, today string) (streak int, anchor string, total int, err error) {
	dates, err := ck.s.Repo.ListCheckinDates(ctx, userID)
	if err != nil {
		return 0, "", 0, err
	}
	streak, anchor, err = domain.ComputeStreak(dates, today)
	if err != nil {
		return 0, "", 0, err
	}
	return streak, anchor, len(dates), nil
}

// recomputeStreak 重算连续天数投影并 UPSERT 快照（streak 只来自事实聚合，禁止手工篡改）。
func (ck *CheckinService) recomputeStreak(ctx context.Context, userID string) error {
	today := domain.CheckinDate(ck.Now())
	streak, anchor, total, err := ck.computeStreak(ctx, userID, today)
	if err != nil {
		return err
	}
	return ck.s.Repo.UpsertStreakSnapshot(ctx, &repository.StreakSnapshotRow{
		UserID: userID, Date: ck.snapshotDate(anchor, today),
		Streak: streak, TotalCheckins: total, ComputedAt: Now(),
	})
}

// snapshotDate 快照日期取 streak 锚点（今天/昨天）；streak=0 时落今天，供前端区分"今日是否已打卡"。
func (ck *CheckinService) snapshotDate(anchor, today string) string {
	if anchor != "" {
		return anchor
	}
	return today
}

// ensureDefs 惰性幂等种子：固定成就模板按 code 检查后写入（0005 无种子行且迁移冻结）。
func (ck *CheckinService) ensureDefs(ctx context.Context) error {
	defs := make([]*repository.AchievementDefRow, 0, len(achievementTemplates))
	for _, t := range achievementTemplates {
		defs = append(defs, &repository.AchievementDefRow{
			ID: NewID(), Code: t.Code, TitleKey: t.TitleKey, DescriptionKey: t.DescriptionKey,
			TriggerRuleJSON: t.TriggerRuleJSON, Icon: t.Icon, Version: 1,
		})
	}
	return ck.s.Repo.SeedAchievementDefs(ctx, defs)
}

// evaluateAchievements 成就规则引擎：解析 trigger_rule_json，命中即发放；
// (user_id, achievement_id) 已解锁则跳过，不重复发奖（4.11 幂等键去重）。
func (ck *CheckinService) evaluateAchievements(ctx context.Context, wsID, userID, eventRef string) error {
	today := domain.CheckinDate(ck.Now())
	streak, _, total, err := ck.computeStreak(ctx, userID, today)
	if err != nil {
		return err
	}
	defs, err := ck.s.Repo.ListAchievementDefs(ctx)
	if err != nil {
		return err
	}
	for _, d := range defs {
		rule, err := domain.ParseCheckinRule(json.RawMessage(d.TriggerRuleJSON))
		if err != nil {
			// 非法规则模板：跳过该成就，不阻断打卡主流程
			continue
		}
		var fired bool
		switch rule.Type {
		case domain.AchievementRuleStreakDays:
			fired = streak >= rule.Threshold
		case domain.AchievementRuleTotalCheckins:
			fired = total >= rule.Threshold
		}
		if !fired {
			continue
		}
		key := "ach-" + userID + "-" + d.ID
		created, err := ck.s.Repo.AwardAchievement(ctx, &repository.UserAchievementRow{
			ID: NewID(), UserID: userID, AchievementID: d.ID,
			AwardedAt: Now(), EventRef: &eventRef, IdempotencyKey: &key,
		})
		if err != nil {
			return err
		}
		if created {
			ck.s.audit(ctx, wsID, "achievement.unlock", "achievement", d.ID,
				map[string]any{"code": d.Code, "user_id": userID})
		}
	}
	return nil
}
