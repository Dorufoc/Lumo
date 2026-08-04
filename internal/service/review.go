package service

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"lumo/internal/domain"
	"lumo/internal/repository"
)

// WrongAnswer 是错题 DTO。
type WrongAnswer struct {
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	UserID            string          `json:"user_id"`
	SubmissionID      string          `json:"submission_id"`
	QuestionVersionID string          `json:"question_version_id"`
	Answer            json.RawMessage `json:"answer"`
	Cause             string          `json:"cause"`
	Status            string          `json:"status"`
	Question          *Question       `json:"question,omitempty"`
	Version           int             `json:"version"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

// WrongAnswerPage 是错题分页。
type WrongAnswerPage struct {
	Items      []*WrongAnswer `json:"items"`
	NextCursor string         `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

// ReviewCard 是复习卡 DTO。
type ReviewCard struct {
	ID            string       `json:"id"`
	WorkspaceID   string       `json:"workspace_id"`
	UserID        string       `json:"user_id"`
	WrongAnswerID string       `json:"wrong_answer_id"`
	Question      *Question    `json:"question,omitempty"`
	WrongAnswer   *WrongAnswer `json:"wrong_answer,omitempty"`
	Repetition    int          `json:"repetition"`
	IntervalDays  int          `json:"interval_days"`
	EaseFactor    float64      `json:"ease_factor"`
	DueAt         string       `json:"due_at"`
	Status        string       `json:"status"`
	Version       int          `json:"version"`
	CreatedAt     string       `json:"created_at"`
	UpdatedAt     string       `json:"updated_at"`
}

// ReviewEvent 是复习事件 DTO。
type ReviewEvent struct {
	ID           string          `json:"id"`
	ReviewCardID string          `json:"review_card_id"`
	Rating       string          `json:"rating"`
	Previous     json.RawMessage `json:"previous"`
	Current      json.RawMessage `json:"current"`
	CreatedAt    string          `json:"created_at"`
}

// ReviewPage 是复习事件分页。
type ReviewPage struct {
	Items      []*ReviewEvent `json:"items"`
	NextCursor string         `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

// ReviewService 实现错题与间隔复习用例。
type ReviewService struct{ s *Services }

// WrongAnswerListReq 错题列表请求。
type WrongAnswerListReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Status      string `json:"status"`
	Cause       string `json:"cause"`
	KnowledgeID string `json:"knowledge_id"`
	Cursor      string `json:"cursor"`
	Limit       int    `json:"limit"`
}

// WrongAnswerList 分页列出错题。
func (r *ReviewService) WrongAnswerList(ctx context.Context, req WrongAnswerListReq) (*WrongAnswerPage, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	rows, next, hasMore, err := r.s.Repo.ListWrongAnswers(ctx, req.WorkspaceID, req.UserID, repository.WrongFilter{
		Status: req.Status, Cause: req.Cause, KnowledgeID: req.KnowledgeID,
		Cursor: req.Cursor, Limit: req.Limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]*WrongAnswer, 0, len(rows))
	for _, row := range rows {
		wa := wrongFromRow(row)
		// 附带题目信息（供复习展示）
		q, err := r.s.Knowledge.questionByID(ctx, req.WorkspaceID, questionIDOf(ctx, r, row))
		if err == nil {
			wa.Question = q
		}
		items = append(items, wa)
	}
	return &WrongAnswerPage{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

// questionIDOf 从版本 ID 反查题目 ID。
func questionIDOf(ctx context.Context, r *ReviewService, row *repository.WrongAnswerRow) string {
	v, err := r.s.Repo.GetQuestionVersion(ctx, row.QuestionVersionID)
	if err != nil || v == nil {
		return ""
	}
	return v.QuestionID
}

// WrongAnswerUpdateCauseReq 更新错因请求。
type WrongAnswerUpdateCauseReq struct {
	WorkspaceID string `json:"workspace_id"`
	WrongID     string `json:"id"`
	Version     int    `json:"version"`
	Cause       string `json:"cause"`
}

// WrongAnswerUpdateCause 更新错因（concept/reading/calculation/memory/method/expression）。
func (r *ReviewService) WrongAnswerUpdateCause(ctx context.Context, req WrongAnswerUpdateCauseReq) (*WrongAnswer, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	valid := map[string]bool{"concept": true, "reading": true, "calculation": true,
		"memory": true, "method": true, "expression": true, "unknown": true}
	if !valid[req.Cause] {
		return nil, domain.InvalidArg("cause 仅允许 concept/reading/calculation/memory/method/expression/unknown")
	}
	row, err := r.s.Repo.UpdateWrongCause(ctx, req.WorkspaceID, req.WrongID, req.Version, req.Cause)
	if err != nil {
		return nil, err
	}
	r.s.audit(ctx, req.WorkspaceID, "wrong_answer.cause", "wrong_answer", req.WrongID,
		map[string]any{"cause": req.Cause})
	return wrongFromRow(row), nil
}

// ReviewListDueReq 到期复习请求。
type ReviewListDueReq struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	DueBefore   string `json:"due_before"` // 默认现在
	Limit       int    `json:"limit"`
}

// ReviewListDue 列出到期复习卡（含题目）。
func (r *ReviewService) ReviewListDue(ctx context.Context, req ReviewListDueReq) ([]*ReviewCard, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	dueBefore := req.DueBefore
	if dueBefore == "" {
		dueBefore = Now()
	} else if _, err := domain.ParseTime(dueBefore); err != nil {
		return nil, domain.InvalidArg("due_before 时间格式非法")
	}
	rows, err := r.s.Repo.ListDueReviewCards(ctx, req.WorkspaceID, req.UserID, dueBefore, req.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]*ReviewCard, 0, len(rows))
	for _, row := range rows {
		card, err := r.cardByID(ctx, req.WorkspaceID, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, nil
}

// ReviewSubmitReq 提交复习评级请求。
type ReviewSubmitReq struct {
	WorkspaceID    string `json:"workspace_id"`
	ReviewCardID   string `json:"review_card_id"`
	Rating         string `json:"rating"` // again | hard | good
	IdempotencyKey string `json:"idempotency_key"`
}

// ReviewSubmit 简化 SM-2 更新复习间隔（间隔只由服务端计算）。
func (r *ReviewService) ReviewSubmit(ctx context.Context, req ReviewSubmitReq) (*ReviewCard, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	if req.Rating != "again" && req.Rating != "hard" && req.Rating != "good" {
		return nil, domain.InvalidArg("rating 仅允许 again/hard/good")
	}
	if req.IdempotencyKey == "" {
		return nil, domain.InvalidArg("idempotency_key 必填")
	}
	return withIdempotency(r.s, ctx, req.WorkspaceID, req.IdempotencyKey, "ReviewSubmit", func() (*ReviewCard, error) {
		card, err := r.s.Repo.GetReviewCard(ctx, req.WorkspaceID, req.ReviewCardID)
		if err != nil {
			return nil, err
		}
		if card == nil {
			return nil, domain.NotFound("复习卡不存在")
		}
		if card.Status != "active" {
			return nil, domain.InvalidState("复习卡已结束")
		}
		prev := mustJSON(card)
		next := sm2Update(card, req.Rating)
		updated, err := r.s.Repo.UpdateReviewCard(ctx, card.ID, next.repetition, next.intervalDays,
			next.easeFactor, next.dueAt, card.Version)
		if err != nil {
			return nil, err
		}
		cur := mustJSON(updated)
		if err := r.s.Repo.CreateReviewEvent(ctx, &repository.ReviewEventRow{
			ID: NewID(), ReviewCardID: card.ID, Rating: req.Rating,
			Previous: prev, Current: cur,
		}); err != nil {
			return nil, err
		}
		r.s.audit(ctx, req.WorkspaceID, "review.submit", "review_card", card.ID,
			map[string]any{"rating": req.Rating})
		// 变更记录（同步队列）
		_ = r.s.Sync.RecordReviewSyncOp(ctx, req.WorkspaceID, card.ID, card.Version,
			map[string]any{"rating": req.Rating, "due_at": updated.DueAt, "interval_days": updated.IntervalDays})
		return r.cardByID(ctx, req.WorkspaceID, card.ID)
	})
}

// sm2Next 是 SM-2 计算结果。
type sm2Next struct {
	repetition   int
	intervalDays int
	easeFactor   float64
	dueAt        string
}

// sm2Update 简化 SM-2：again/hard/good。
func sm2Update(card *repository.ReviewCardRow, rating string) sm2Next {
	rep := card.Repetition
	interval := card.IntervalDays
	ease := card.EaseFactor
	now := time.Now().UTC()

	switch rating {
	case "again":
		rep = 0
		interval = 1
		ease = math.Max(1.3, ease-0.2)
	case "hard":
		rep++
		if rep == 1 {
			interval = 1
		} else if rep == 2 {
			interval = 3
		} else {
			interval = int(float64(interval)*ease*0.9) + 1
		}
		ease = math.Max(1.3, ease-0.15)
	case "good":
		rep++
		if rep == 1 {
			interval = 1
		} else if rep == 2 {
			interval = 3
		} else {
			interval = int(float64(interval)*ease) + 1
		}
		ease = math.Min(3.5, ease+0.1)
	}
	return sm2Next{
		repetition: rep, intervalDays: interval, easeFactor: ease,
		dueAt: now.AddDate(0, 0, interval).Format(time.RFC3339),
	}
}

// ReviewHistoryListReq 复习历史请求。
type ReviewHistoryListReq struct {
	WorkspaceID  string `json:"workspace_id"`
	ReviewCardID string `json:"review_card_id"`
	Cursor       string `json:"cursor"`
	Limit        int    `json:"limit"`
}

// ReviewHistoryList 列出复习事件（只追加历史）。
func (r *ReviewService) ReviewHistoryList(ctx context.Context, req ReviewHistoryListReq) (*ReviewPage, error) {
	if err := r.s.assertWorkspace(ctx, req.WorkspaceID); err != nil {
		return nil, err
	}
	card, err := r.s.Repo.GetReviewCard(ctx, req.WorkspaceID, req.ReviewCardID)
	if err != nil {
		return nil, err
	}
	if card == nil {
		return nil, domain.NotFound("复习卡不存在")
	}
	events, err := r.s.Repo.ListReviewEvents(ctx, req.ReviewCardID, req.Limit)
	if err != nil {
		return nil, err
	}
	items := make([]*ReviewEvent, 0, len(events))
	for _, e := range events {
		items = append(items, &ReviewEvent{
			ID: e.ID, ReviewCardID: e.ReviewCardID, Rating: e.Rating,
			Previous: e.Previous, Current: e.Current, CreatedAt: e.CreatedAt,
		})
	}
	return &ReviewPage{Items: items, HasMore: len(items) >= req.Limit && req.Limit > 0}, nil
}

// cardByID 组装复习卡 DTO（含题目与错题）。
func (r *ReviewService) cardByID(ctx context.Context, wsID, id string) (*ReviewCard, error) {
	row, err := r.s.Repo.GetReviewCard(ctx, wsID, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, domain.NotFound("复习卡不存在")
	}
	card := &ReviewCard{
		ID: row.ID, WorkspaceID: row.WorkspaceID, UserID: row.UserID,
		WrongAnswerID: row.WrongAnswerID, Repetition: row.Repetition,
		IntervalDays: row.IntervalDays, EaseFactor: row.EaseFactor,
		DueAt: row.DueAt, Status: row.Status, Version: row.Version,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	wa, err := r.s.Repo.GetWrongAnswer(ctx, wsID, row.WrongAnswerID)
	if err == nil && wa != nil {
		w := wrongFromRow(wa)
		card.WrongAnswer = w
		if qid := questionIDOf(ctx, r, wa); qid != "" {
			if q, err := r.s.Knowledge.questionByID(ctx, wsID, qid); err == nil {
				card.Question = q
			}
		}
	}
	return card, nil
}

func wrongFromRow(row *repository.WrongAnswerRow) *WrongAnswer {
	return &WrongAnswer{
		ID: row.ID, WorkspaceID: row.WorkspaceID, UserID: row.UserID,
		SubmissionID: row.SubmissionID, QuestionVersionID: row.QuestionVersionID,
		Answer: row.Answer, Cause: row.Cause, Status: row.Status,
		Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
