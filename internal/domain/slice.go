package domain

import "time"

// Fact — одно атомарное утверждение о задаче, извлечённое из источника.
//
// Факты, как и источники, только добавляются. Реестр не правит факт, а
// добавляет новый: старая цифра остаётся видна, и по журналу можно понять,
// когда и на каком основании оценка изменилась.
type Fact struct {
	ID     string `json:"id"`
	TaskID string `json:"taskId"`

	// Field — адрес значения в срезе, например passport.deadline или
	// status.readiness. Так факт попадает точно в своё поле, а не в общий
	// текст, и срез остаётся собираемым автоматически.
	Field string `json:"field"`

	Value Value `json:"value"`

	// Confidence — уверенность извлечения, 0..1. Заполняет тот, кто извлекал:
	// модель — своей оценкой, ручной аналитик — единицей.
	Confidence float64 `json:"confidence"`

	// ObservedAt — дата события в источнике; CreatedAt — дата извлечения.
	// Различать обязательно: письмо от марта, прочитанное в августе, говорит о
	// марте.
	ObservedAt time.Time `json:"observedAt,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Passport — раздел «Паспорт задачи».
type Passport struct {
	Title    Value `json:"title"`
	Author   Value `json:"author"`
	Assignee Value `json:"assignee"`
	OpenedAt Value `json:"openedAt"`
	Deadline Value `json:"deadline"`

	// Shifts — история переносов срока. Пустой список означает, что срок не
	// двигали; длинный — что реальный срок давно живёт отдельно от плана.
	Shifts []DeadlineShift `json:"shifts,omitempty"`
}

// Goal — раздел «Цель и результат».
type Goal struct {
	AsStated   Value       `json:"asStated"`             // как сформулировано в задаче
	Clarified  Value       `json:"clarified"`            // как понято после уточнений
	Criteria   []Criterion `json:"criteria,omitempty"`   // критерии приёмки
	OutOfScope []Value     `json:"outOfScope,omitempty"` // что в объём не входит
}

// Status — раздел «Что сделано и что осталось».
type Status struct {
	Stage      Value       `json:"stage"`
	Readiness  Value       `json:"readiness"`
	Milestones []Milestone `json:"milestones,omitempty"`
	Done       []Value     `json:"done,omitempty"`
	Left       []Value     `json:"left,omitempty"`
}

// PMActions — раздел «Что нужно от PM».
type PMActions struct {
	Needed    []PMAction `json:"needed,omitempty"`
	NextCheck Value      `json:"nextCheck"`         // когда спросить в следующий раз
	Comment   string     `json:"comment,omitempty"` // пояснение PM своими словами
}

// Slice — срез по задаче: то, что PM читает вместо всей переписки.
//
// Срез не хранится как истина, а собирается из фактов на дату BuiltAt. Version
// растёт с каждой сборкой, так что два среза одной задачи можно сравнить и
// увидеть, что изменилось между проверками.
type Slice struct {
	TaskID  string    `json:"taskId"`
	Version int       `json:"version"`
	BuiltAt time.Time `json:"builtAt"`

	// Разделы идут в том же порядке, в каком их читает PM.
	Passport  Passport   `json:"passport"`
	Goal      Goal       `json:"goal"`
	Status    Status     `json:"status"`
	Blockers  []Blocker  `json:"blockers,omitempty"`
	Risks     []Risk     `json:"risks,omitempty"`
	PMActions PMActions  `json:"pmActions"`
	Questions []Question `json:"questions,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`

	// SourceIDs — источники, по которым собран этот срез. Нужны, чтобы читатель
	// видел не только выводы, но и то, на каком материале они сделаны.
	SourceIDs []string `json:"sourceIds,omitempty"`

	// Analyst — кто извлекал факты: ручная разметка или модель. Пока модели нет,
	// поле честно говорит «manual».
	Analyst string `json:"analyst,omitempty"`
}

// OpenQuestions возвращает вопросы, на которые ответа в источниках нет.
func (s Slice) OpenQuestions() []Question {
	var out []Question
	for _, q := range s.Questions {
		if !q.Answered() {
			out = append(out, q)
		}
	}
	return out
}

// MissingArtifacts возвращает файлы, которые упомянуты, но не приложены.
func (s Slice) MissingArtifacts() []Artifact {
	var out []Artifact
	for _, a := range s.Artifacts {
		if !a.Present {
			out = append(out, a)
		}
	}
	return out
}

// OldestBlocker возвращает самый давний блокер и признак того, что блокеры
// вообще есть. Именно он, а не их число, показывает запущенность задачи.
func (s Slice) OldestBlocker() (Blocker, bool) {
	var oldest Blocker
	found := false
	for _, b := range s.Blockers {
		if b.Since.IsZero() {
			continue
		}
		if !found || b.Since.Before(oldest.Since) {
			oldest, found = b, true
		}
	}
	return oldest, found
}

// Gaps считает пробелы среза: значения без ответа, открытые вопросы и
// неприложенные файлы. Число выводится в интерфейс рядом с готовностью, чтобы
// «27 % готово» читалось вместе с «и вот чего мы не знаем».
func (s Slice) Gaps() int {
	n := 0
	for _, v := range []Value{
		s.Passport.Title, s.Passport.Author, s.Passport.Assignee,
		s.Passport.OpenedAt, s.Passport.Deadline,
		s.Goal.AsStated, s.Goal.Clarified,
		s.Status.Stage, s.Status.Readiness,
		s.PMActions.NextCheck,
	} {
		if !v.Known() {
			n++
		}
	}
	n += len(s.OpenQuestions())
	n += len(s.MissingArtifacts())
	return n
}
