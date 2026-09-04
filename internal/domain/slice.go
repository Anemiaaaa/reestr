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

	// SourceIDs — источники, на которые срез ссылается хотя бы в одном поле.
	//
	// Именно использованные, а не все поданные. В чате сто сообщений, для
	// вывода понадобилось семь — в версии остаются эти семь. Список
	// фиксируется в момент сборки и потом не меняется: старая версия обязана
	// объясняться тем материалом, который был у неё на руках, иначе история
	// начнёт зависеть от того, что загрузили позже.
	SourceIDs []string `json:"sourceIds,omitempty"`

	// Considered — сколько материала подали аналитику при этой сборке.
	// Вместе с len(SourceIDs) отвечает на вопрос «сто сообщений прочитано,
	// семь пригодились»: без этого числа отбор не виден.
	Considered int `json:"considered,omitempty"`

	// Analyst — кто извлекал факты: ручная разметка или модель. Пока модели нет,
	// поле честно говорит «manual».
	Analyst string `json:"analyst,omitempty"`

	// EditedBy — кто собрал эту версию правкой. Пусто у версии, собранной
	// разбором.
	//
	// Отдельно от Analyst намеренно. Поправленная версия всё равно стоит на
	// разборе — правка меняет одно поле из двадцати, — и написать здесь «правка»
	// вместо имени модели значило бы приписать человеку остальные девятнадцать.
	//
	// По этому же полю находится опора для пересборки без модели: последняя
	// версия, которую собрал разбор. Собирать поверх поправленной нельзя — в ней
	// правка уже применена, и снять её потом было бы неоткуда.
	EditedBy string `json:"editedBy,omitempty"`
}

// SliceRef — версия среза без содержимого: строка в списке истории.
type SliceRef struct {
	TaskID  string    `json:"taskId"`
	Version int       `json:"version"`
	BuiltAt time.Time `json:"builtAt"`
}

// Ref возвращает ссылку на эту версию.
func (s Slice) Ref() SliceRef {
	return SliceRef{TaskID: s.TaskID, Version: s.Version, BuiltAt: s.BuiltAt}
}

// UsedSources возвращает идентификаторы источников, на которые в срезе есть
// хотя бы одна ссылка, в порядке первого упоминания.
//
// Список выводится из самого среза, а не берётся со слов аналитика. Сказать «я
// использовал эти сообщения» дёшево; сослаться на них в конкретном поле — нет.
// Поэтому в версии остаются только те источники, которые действительно
// что-то подтверждают, и модель не может расширить список, не процитировав.
func (s Slice) UsedSources() []string {
	seen := make(map[string]bool)
	var out []string

	add := func(vs ...Value) {
		for _, v := range vs {
			if v.SourceID == "" || seen[v.SourceID] {
				continue
			}
			seen[v.SourceID] = true
			out = append(out, v.SourceID)
		}
	}

	add(s.Passport.Title, s.Passport.Author, s.Passport.Assignee,
		s.Passport.OpenedAt, s.Passport.Deadline)
	add(s.Goal.AsStated, s.Goal.Clarified)
	add(s.Goal.OutOfScope...)
	add(s.Status.Stage, s.Status.Readiness)
	add(s.Status.Done...)
	add(s.Status.Left...)
	for _, b := range s.Blockers {
		add(b.Evidence)
	}
	for _, r := range s.Risks {
		add(r.Evidence)
	}
	add(s.PMActions.NextCheck)
	for _, q := range s.Questions {
		add(q.Answer)
	}
	return out
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

