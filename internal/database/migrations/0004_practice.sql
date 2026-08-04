-- 0004_practice: 练习会话补充字段（跳过题目记录）
ALTER TABLE practice_sessions ADD COLUMN skipped_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(skipped_json));
