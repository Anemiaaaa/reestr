package domain

import "strings"

// ChangeKind — что случилось со значением между версиями.
type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"   // появилось
	ChangeRemoved ChangeKind = "removed" // пропало
	ChangeEdited  ChangeKind = "edited"  // изменилось
)

// Label возвращает подпись вида изменения для интерфейса.
func (k ChangeKind) Label() string {
	switch k {
	case ChangeAdded:
		return "появилось"
	case ChangeRemoved:
		return "пропало"
	case ChangeEdited:
		return "изменилось"
	}
	return string(k)
}

// Change — одно различие между двумя версиями среза.
//
// Хранит тексты, а не значения целиком, и это осознанное сужение. Сравнение
// отвечает на вопрос «что изменилось в срезе», а не «изменилось ли
// происхождение значения»: перечитанная цитата, из которой следует то же самое,
// для читателя не изменение. Если понадобится показывать смену происхождения —
// это будет отдельный вид изменения, а не тихая правка этого.
type SliceChange struct {
	Section string     `json:"section"`
	Field   string     `json:"field"`
	Kind    ChangeKind `json:"kind"`
	Before  string     `json:"before,omitempty"`
	After   string     `json:"after,omitempty"`
}

// Compare возвращает различия между двумя версиями среза, от старой к новой.
//
// Порядок разделов тот же, в каком их читает PM: сравнение читают сверху вниз,
// как и сам срез, и перестановка разделов заставила бы искать глазами.
//
// Пустой список означает, что версии совпадают по содержанию. Это возможно и
// нормально: пересборка после нового материала могла ничего не поменять в
// выводах, и сказать об этом прямо честнее, чем показать пустой экран.
func Compare(before, after Slice) []SliceChange {
	var out []SliceChange

	add := func(section, field string, b, a Value) {
		if c, ok := changed(section, field, b, a); ok {
			out = append(out, c)
		}
	}

	add("Паспорт", "Название", before.Passport.Title, after.Passport.Title)
	add("Паспорт", "Автор постановки", before.Passport.Author, after.Passport.Author)
	add("Паспорт", "Исполнитель", before.Passport.Assignee, after.Passport.Assignee)
	add("Паспорт", "Поставлена", before.Passport.OpenedAt, after.Passport.OpenedAt)
	add("Паспорт", "Срок", before.Passport.Deadline, after.Passport.Deadline)

	add("Цель", "Как сформулировано", before.Goal.AsStated, after.Goal.AsStated)
	add("Цель", "После уточнений", before.Goal.Clarified, after.Goal.Clarified)

	add("Статус", "Этап", before.Status.Stage, after.Status.Stage)
	add("Статус", "Готовность", before.Status.Readiness, after.Status.Readiness)
	add("Что нужно от PM", "Следующая проверка", before.PMActions.NextCheck, after.PMActions.NextCheck)

	out = append(out, listChanges("Статус", "Сделано", texts(before.Status.Done), texts(after.Status.Done))...)
	out = append(out, listChanges("Статус", "Осталось", texts(before.Status.Left), texts(after.Status.Left))...)
	out = append(out, listChanges("Цель", "Вне объёма", texts(before.Goal.OutOfScope), texts(after.Goal.OutOfScope))...)

	out = append(out, listChanges("Блокеры", "Блокер",
		blockerTexts(before.Blockers), blockerTexts(after.Blockers))...)
	out = append(out, listChanges("Риски", "Риск",
		riskTexts(before.Risks), riskTexts(after.Risks))...)
	out = append(out, listChanges("Вопросы", "Вопрос",
		questionTexts(before.Questions), questionTexts(after.Questions))...)
	out = append(out, listChanges("Цель", "Критерий приёмки",
		criterionTexts(before.Goal.Criteria), criterionTexts(after.Goal.Criteria))...)
	out = append(out, listChanges("Статус", "Этап плана",
		milestoneTexts(before.Status.Milestones), milestoneTexts(after.Status.Milestones))...)
	out = append(out, listChanges("Что нужно от PM", "Действие",
		actionTexts(before.PMActions.Needed), actionTexts(after.PMActions.Needed))...)
	out = append(out, listChanges("Материалы", "Файл",
		artifactTexts(before.Artifacts), artifactTexts(after.Artifacts))...)

	return out
}

// changed сравнивает два значения одного поля.
func changed(section, field string, b, a Value) (SliceChange, bool) {
	bt, at := text(b), text(a)
	switch {
	case bt == at:
		return SliceChange{}, false
	case bt == "":
		return SliceChange{Section: section, Field: field, Kind: ChangeAdded, After: at}, true
	case at == "":
		return SliceChange{Section: section, Field: field, Kind: ChangeRemoved, Before: bt}, true
	}
	return SliceChange{Section: section, Field: field, Kind: ChangeEdited, Before: bt, After: at}, true
}

// text — сравнимое представление значения. Незаполненное значение это пустая
// строка независимо от того, чем его объяснили: «нет данных, спросить у
// заказчика» и «нет данных, спросить у исполнителя» — один и тот же пробел, а
// не изменение среза.
func text(v Value) string {
	if !v.Known() {
		return ""
	}
	return strings.TrimSpace(v.Text)
}

// listChanges сравнивает списки по текстам.
//
// Сравнение по содержанию, а не по позиции. Списки собирает модель, и порядок в
// них не обещан: сдвиг блокера на строку вверх не изменение, а перестановка
// строк выглядела бы как полная замена списка.
func listChanges(section, field string, before, after []string) []SliceChange {
	was := make(map[string]bool, len(before))
	for _, s := range before {
		was[s] = true
	}
	now := make(map[string]bool, len(after))
	for _, s := range after {
		now[s] = true
	}

	var out []SliceChange
	// Сначала пропавшее, потом появившееся: так читается «было → стало».
	for _, s := range before {
		if !now[s] {
			out = append(out, SliceChange{Section: section, Field: field, Kind: ChangeRemoved, Before: s})
		}
	}
	for _, s := range after {
		if !was[s] {
			out = append(out, SliceChange{Section: section, Field: field, Kind: ChangeAdded, After: s})
		}
	}
	return out
}

func texts(vs []Value) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if s := text(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func blockerTexts(bs []Blocker) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		// Вид входит в текст: блокер, сменивший вид с «нет доступа» на
		// «технический», — это другой блокер, и идти с ним нужно к другому
		// человеку.
		out = append(out, b.Kind.Label()+": "+strings.TrimSpace(b.Summary))
	}
	return out
}

func riskTexts(rs []Risk) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, strings.TrimSpace(r.Summary))
	}
	return out
}

func questionTexts(qs []Question) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		// Отвеченный вопрос отличается от неотвеченного: закрытие вопроса —
		// главное, что человек ищет в сравнении версий.
		mark := "без ответа"
		if q.Answered() {
			mark = "отвечено: " + strings.TrimSpace(q.Answer.Text)
		}
		out = append(out, strings.TrimSpace(q.Text)+" — "+mark)
	}
	return out
}

func criterionTexts(cs []Criterion) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		mark := "не выполнен"
		if c.Met {
			mark = "выполнен"
		}
		out = append(out, strings.TrimSpace(c.Text)+" — "+mark)
	}
	return out
}

func milestoneTexts(ms []Milestone) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		mark := "в работе"
		switch {
		case m.Done():
			mark = "закрыт"
		case m.Progress <= 0:
			mark = "не начат"
		}
		if !m.Due.IsZero() {
			mark += ", срок " + FormatDate(m.Due)
		}
		out = append(out, strings.TrimSpace(m.Title)+" — "+mark)
	}
	return out
}

func actionTexts(as []PMAction) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Kind.Label()+": "+strings.TrimSpace(a.Text))
	}
	return out
}

func artifactTexts(as []Artifact) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		mark := "не приложен"
		if a.Present {
			mark = "приложен"
		}
		out = append(out, strings.TrimSpace(a.Name)+" — "+mark)
	}
	return out
}
