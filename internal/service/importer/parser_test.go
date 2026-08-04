package importer

import (
	"encoding/json"
	"testing"
)

func TestParseJSON(t *testing.T) {
	content := []byte(`{
		"questions": [
			{"type": "single_choice", "stem": "1+1=?", "options": [{"key":"A","text":"2"},{"key":"B","text":"3"}], "answer": "A"},
			{"type": "fill_blank", "stem": "2+2=____", "answer": "4"}
		]
	}`)
	qs, err := Parse("json", content)
	if err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(qs))
	}
	for _, q := range qs {
		if q.Error != "" {
			t.Fatalf("unexpected error: %s", q.Error)
		}
		var p map[string]any
		if err := json.Unmarshal(q.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["stem"] == nil {
			t.Fatal("stem missing")
		}
	}

	// 裸数组
	qs2, err := Parse("json", []byte(`[{"stem":"x","answer":"a","type":"short_answer"}]`))
	if err != nil || len(qs2) != 1 {
		t.Fatalf("bare array parse failed: %v %d", err, len(qs2))
	}
	// 非法 JSON
	if _, err := Parse("json", []byte(`{bad`)); err == nil {
		t.Fatal("expected error for bad json")
	}
}

func TestParseMarkdownMixed(t *testing.T) {
	content := []byte(`# 测试题库

## 1. 单选：1+1 等于几？
- A. 1
- B. 2
- C. 3
正确答案：B
解析：1+1=2

## 2. 多选：以下哪些是偶数？
A. 2
B. 3
C. 4
D. 5
答案：AC

## 3. 判断：地球是圆的。
A. 正确
B. 错误
答案：A

## 4. 填空：水的化学式是____。
答案：H2O

## 5. 简答：简述牛顿第一定律。
答案：惯性定律

### 6. 单选（三级标题）：2*3=？
A. 5
B. 6
答案：B
`)
	qs, err := Parse("markdown", content)
	if err != nil {
		t.Fatalf("parse markdown: %v", err)
	}
	if len(qs) != 6 {
		t.Fatalf("expected 6 questions, got %d", len(qs))
	}
	for i, q := range qs {
		if q.Error != "" {
			t.Fatalf("q%d error: %s", i+1, q.Error)
		}
	}
	types := []string{"single_choice", "multiple_choice", "single_choice", "fill_blank", "short_answer", "single_choice"}
	for i, want := range types {
		var p map[string]any
		if err := json.Unmarshal(qs[i].Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["type"] != want {
			t.Errorf("q%d expected type %s, got %v", i+1, want, p["type"])
		}
	}
	// 判断题为 single_choice 且选项固定
	var j map[string]any
	json.Unmarshal(qs[2].Payload, &j)
	if j["answer"] != "A" {
		t.Errorf("judge answer should be A, got %v", j["answer"])
	}
}

func TestParseText(t *testing.T) {
	content := []byte(`1. 下列哪个是质数？
A. 4
B. 6
C. 7
答案：C

2. 填空：1 米 = ____ 厘米
答案：100
`)
	qs, err := Parse("text", content)
	if err != nil {
		t.Fatalf("parse text: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("expected 2, got %d", len(qs))
	}
	if qs[0].Error != "" {
		t.Fatalf("q1 error: %s", qs[0].Error)
	}
	var p map[string]any
	json.Unmarshal(qs[0].Payload, &p)
	if p["type"] != "single_choice" {
		t.Errorf("expected single_choice, got %v", p["type"])
	}
	if p["answer"] != "C" {
		t.Errorf("expected answer C, got %v", p["answer"])
	}
}

func TestParseMarkdownErrors(t *testing.T) {
	// 答案不在选项中 → 该题错误
	content := []byte(`## 1. 题目
A. 甲
B. 乙
答案：Z
`)
	qs, err := Parse("markdown", content)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(qs) != 1 || qs[0].Error == "" {
		t.Fatalf("expected item error, got %+v", qs)
	}
	// 空内容
	if _, err := Parse("markdown", []byte("# 只有标题\n\n")); err == nil {
		t.Fatal("expected error for no questions")
	}
}
