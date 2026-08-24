package domain

import "time"

// Stage — этап задачи из шаблона среза.
type Stage string

const (
	StageNotStarted Stage = "not_started"
	StageInProgress Stage = "in_progress"
	StageReview     Stage = "review"
	StageBlocked    Stage = "blocked"
	StageDone       Stage = "done"
)

// Label возвращает подпись этапа для интерфейса.
func (s Stage) Label() string {
	switch s {
	case StageNotStarted:
		return "не начато"
	case StageInProgress:
		return "в работе"
	case StageReview:
		return "на проверке"
	case StageBlocked:
		return "заблокировано"
	case StageDone:
		return "готово"
	}
	return string(s)
}

// Task — задача, по которой собирается срез.
//
// Здесь только то, что задаёт сам PM при постановке. Всё остальное — статус,
// готовность, блокеры — не поля задачи, а результат разбора источников, и
// живёт в срезе.
type Task struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
	Title    string    `json:"title"`
	Author   string    `json:"author"`   // постановщик
	Assignee string    `json:"assignee"` // ответственный специалист
	OpenedAt time.Time `json:"openedAt"`
	Deadline time.Time `json:"deadline"`
	Budget   int       `json:"budget,omitempty"` // согласованная смета, ₽
}

// DaysLeft — сколько дней осталось до срока на дату now. Отрицательное
// значение означает просрочку.
func (t Task) DaysLeft(now time.Time) int {
	if t.Deadline.IsZero() {
		return 0
	}
	return DaysBetween(now, t.Deadline)
}

// SourceKind — вид загруженного материала.
type SourceKind string

const (
	KindCorrespondence SourceKind = "correspondence" // лента задачи, чат, письма
	KindSpec           SourceKind = "spec"           // техническое задание
	KindAudit          SourceKind = "audit"          // аудит, обследование
	KindNote           SourceKind = "note"           // пояснение от PM
)

// Label возвращает подпись вида источника для интерфейса.
func (k SourceKind) Label() string {
	switch k {
	case KindCorrespondence:
		return "переписка"
	case KindSpec:
		return "ТЗ"
	case KindAudit:
		return "аудит"
	case KindNote:
		return "пояснение"
	}
	return string(k)
}

// Valid сообщает, известен ли такой вид источника.
func (k SourceKind) Valid() bool {
	switch k {
	case KindCorrespondence, KindSpec, KindAudit, KindNote:
		return true
	}
	return false
}

// Source — единица входа: то, что PM загрузил в реестр.
//
// Источники только добавляются. Загруженный материал не правят и не удаляют,
// иначе ссылки из среза начнут врать — а ссылка на источник здесь и есть
// единственное основание доверять срезу.
type Source struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"taskId"`
	Kind       SourceKind `json:"kind"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	UploadedAt time.Time  `json:"uploadedAt"`
}

// Size — объём материала в символах.
func (s Source) Size() int { return len([]rune(s.Body)) }
