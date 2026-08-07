package repository

import (
	"context"
	"database/sql"
)

// SpeakingResultRow 是口语测评结果行（4.18 speaking_results）。
type SpeakingResultRow struct {
	ID           string
	SubmissionID string
	Transcript   string
	ScoresJSON   string // JSON 对象（json_valid CHECK）
	AudioKept    bool
	Status       string
	CreatedAt    string
	UpdatedAt    string
}

const speakingResultCols = `id, submission_id, transcript, scores_json, audio_kept, status, created_at, updated_at`

func scanSpeakingResult(row interface{ Scan(...any) error }) (*SpeakingResultRow, error) {
	var r SpeakingResultRow
	var audioKept int
	if err := row.Scan(&r.ID, &r.SubmissionID, &r.Transcript, &r.ScoresJSON, &audioKept,
		&r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, normalizeErr(err)
	}
	r.AudioKept = audioKept == 1
	return &r, nil
}

// UpsertSpeakingResult 幂等写入口语结果：id 由 submission_id 确定性派生，
// 同一提交重复提交即更新已有行（保留原 created_at）。
func (r *Repo) UpsertSpeakingResult(ctx context.Context, s *SpeakingResultRow) (*SpeakingResultRow, error) {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO speaking_results (id, submission_id, transcript, scores_json, audio_kept, status)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			transcript = excluded.transcript,
			scores_json = excluded.scores_json,
			audio_kept = excluded.audio_kept,
			status = excluded.status,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		s.ID, s.SubmissionID, s.Transcript, s.ScoresJSON, boolToInt(s.AudioKept), s.Status)
	if err != nil {
		return nil, normalizeErr(err)
	}
	return r.GetSpeakingResult(ctx, s.ID)
}

// GetSpeakingResult 按 ID 获取口语结果；不存在返回 nil,nil。
func (r *Repo) GetSpeakingResult(ctx context.Context, id string) (*SpeakingResultRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+speakingResultCols+` FROM speaking_results WHERE id = ?`, id)
	return scanSpeakingResult(row)
}

// GetSpeakingResultBySubmission 按提交获取口语结果；不存在返回 nil,nil。
func (r *Repo) GetSpeakingResultBySubmission(ctx context.Context, submissionID string) (*SpeakingResultRow, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT `+speakingResultCols+` FROM speaking_results WHERE submission_id = ?`, submissionID)
	return scanSpeakingResult(row)
}
