package domain

// 笔记类型（4.8：kind[question|document|agent|free]）。
const (
	NoteKindQuestion = "question"
	NoteKindDocument = "document"
	NoteKindAgent    = "agent"
	NoteKindFree     = "free"
)

// ValidateNoteKind 校验笔记类型枚举。
func ValidateNoteKind(kind string) bool {
	return kind == NoteKindQuestion || kind == NoteKindDocument ||
		kind == NoteKindAgent || kind == NoteKindFree
}

// Note 是学习笔记实体（4.8 / 0005_student.sql notes 表）。
type Note struct {
	ID           string
	WorkspaceID  string
	UserID       string
	Kind         string
	Title        string
	BodyMD       string
	SourceRef    *string
	KnowledgeIDs []string
	Tags         []string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    *string
	Version      int
}

// Annotation 是资料标注值对象（4.8 / 0005_student.sql note_annotations 表）。
// 高亮偏移基于文档稳定锚点（anchor_hash）；文档重新解析后锚点失效需重新定位。
type Annotation struct {
	ID             string
	NoteID         string
	DocumentID     *string
	AnchorHash     string
	OffsetStart    int
	OffsetEnd      int
	HighlightColor string
	CreatedAt      string
}
