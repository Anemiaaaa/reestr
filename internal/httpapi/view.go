// Package httpapi отдаёт реестр по HTTP.
//
// Транспорт делает одну вещь, о которой стоит сказать отдельно: он не отдаёт
// домен наружу как есть, а собирает представление, в котором все числа уже
// посчитаны и все подписи уже подставлены. Фронтенд получает строки и рисует
// их.
//
// Это не лишний слой. Пока фронтенд считает сам, он рано или поздно посчитает
// иначе, чем бэкенд, — и на экране появятся два разных процента готовности из
// одних данных. Здесь такой возможности просто нет.
package httpapi

import (
	"fmt"
	"time"

	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
)

// mark — знак происхождения значения. Повторяет пометки из макета: читатель
// должен различать цитату, вывод и расчёт, не открывая источник.
func mark(o domain.Origin) string {
	switch o {
	case domain.OriginQuoted:
		return "↩"
	case domain.OriginDerived:
		return "→"
	case domain.OriginComputed:
		return "∑"
	case domain.OriginMissing:
		return "?"
	}
	return ""
}

// value — значение среза вместе со своим происхождением.
type value struct {
	Text        string `json:"text"`
	Origin      string `json:"origin"`
	OriginLabel string `json:"originLabel"`
	Mark        string `json:"mark"`
	Known       bool   `json:"known"`
	SourceID    string `json:"sourceId,omitempty"`
	SourceTitle string `json:"sourceTitle,omitempty"`
	Quote       string `json:"quote,omitempty"`
	Note        string `json:"note,omitempty"`
}

// sources — справочник источников для подстановки названий.
type sources map[string]domain.Source

func newSources(list []domain.Source) sources {
	m := make(sources, len(list))
	for _, s := range list {
		m[s.ID] = s
	}
	return m
}

func (s sources) title(id string) string {
	if src, ok := s[id]; ok {
		return src.Title
	}
	return ""
}

func newValue(v domain.Value, srcs sources) value {
	text := v.Text
	if text == "" {
		text = "нет данных"
	}
	return value{
		Text:        text,
		Origin:      string(v.Origin),
		OriginLabel: v.Origin.Label(),
		Mark:        mark(v.Origin),
		Known:       v.Known(),
		SourceID:    v.SourceID,
		SourceTitle: srcs.title(v.SourceID),
		Quote:       v.Quote,
		Note:        v.Note,
	}
}

func newValues(vs []domain.Value, srcs sources) []value {
	out := make([]value, 0, len(vs))
	for _, v := range vs {
		out = append(out, newValue(v, srcs))
	}
	return out
}

// task — задача в списке.
type task struct {
	ID           string `json:"id"`
	Project      string `json:"project"`
	Title        string `json:"title"`
	Author       string `json:"author,omitempty"`
	Assignee     string `json:"assignee,omitempty"`
	OpenedAt     string `json:"openedAt,omitempty"`
	Deadline     string `json:"deadline,omitempty"`
	DeadlineNote string `json:"deadlineNote,omitempty"`
	Overdue      bool   `json:"overdue"`
	Budget       string `json:"budget,omitempty"`
}

func newTask(t domain.Task, now time.Time) task {
	v := task{
		ID:       t.ID,
		Project:  t.Project,
		Title:    t.Title,
		Author:   t.Author,
		Assignee: t.Assignee,
	}
	if !t.OpenedAt.IsZero() {
		v.OpenedAt = domain.FormatDate(t.OpenedAt)
	}
	if !t.Deadline.IsZero() {
		v.Deadline = domain.FormatDate(t.Deadline)
		left := t.DaysLeft(now)
		switch {
		case left < 0:
			v.DeadlineNote, v.Overdue = "просрочено на "+ru.Days(-left), true
		case left == 0:
			v.DeadlineNote = "срок сегодня"
		default:
			v.DeadlineNote = "осталось " + ru.Days(left)
		}
	}
	if t.Budget != 0 {
		v.Budget = ru.Money(t.Budget)
	}
	return v
}

// source — загруженный материал. Тело не отдаётся списком: источники бывают в
// сотни килобайт, и список задач не должен их тащить.
type source struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	KindLabel  string `json:"kindLabel"`
	Title      string `json:"title"`
	UploadedAt string `json:"uploadedAt"`
	Size       int    `json:"size"`
	SizeText   string `json:"sizeText"`
	Body       string `json:"body,omitempty"`
}

func newSource(s domain.Source, withBody bool) source {
	v := source{
		ID:         s.ID,
		Kind:       string(s.Kind),
		KindLabel:  s.Kind.Label(),
		Title:      s.Title,
		UploadedAt: domain.FormatDate(s.UploadedAt),
		Size:       s.Size(),
		SizeText:   ru.Count(s.Size(), "знак", "знака", "знаков"),
	}
	if withBody {
		v.Body = s.Body
	}
	return v
}

// head — сводка среза: то, что PM читает первым и часто единственным.
type head struct {
	Title     string `json:"title"`
	Project   string `json:"project"`
	Stage     value  `json:"stage"`
	Readiness value  `json:"readiness"`
	// Доля 0…1 — единственное число, которое отдаётся фронтенду как число, а не
	// строкой: из него рисуется полоса, а ширину полосы словами не задать.
	// Подпись к полосе всё равно берётся из Readiness.
	ReadinessShare float64 `json:"readinessShare"`
	Deadline       string  `json:"deadline,omitempty"`
	DeadlineNote   string  `json:"deadlineNote,omitempty"`
	Overdue        bool    `json:"overdue"`
	Budget         string  `json:"budget,omitempty"`

	// Подписи-предупреждения. Пустая строка означает, что говорить не о чем, и
	// строка на экране не появится.
	GapsText     string `json:"gapsText,omitempty"`
	OverdueText  string `json:"overdueText,omitempty"`
	BlockerText  string `json:"blockerText,omitempty"`
	ShiftsText   string `json:"shiftsText,omitempty"`
	CriteriaText string `json:"criteriaText,omitempty"`

	Version int    `json:"version"`
	BuiltAt string `json:"builtAt"`
	Analyst string `json:"analyst"`
}

// milestone — этап плана.
type milestone struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Due         string   `json:"due,omitempty"`
	Progress    string   `json:"progress"`
	Share       float64  `json:"share"`
	Done        bool     `json:"done"`
	OverdueText string   `json:"overdueText,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
}

// shift — перенос срока.
type shift struct {
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	At        string `json:"at,omitempty"`
	Comment   string `json:"comment,omitempty"`
	Explained bool   `json:"explained"`
	MovedText string `json:"movedText,omitempty"`
}

// criterion — критерий приёмки.
type criterion struct {
	N    int    `json:"n"`
	Text string `json:"text"`
	Met  bool   `json:"met"`
	Note string `json:"note,omitempty"`
}

// blocker — то, что тормозит задачу.
type blocker struct {
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`
	DependsOn string `json:"dependsOn,omitempty"`
	Since     string `json:"since,omitempty"`
	AgeText   string `json:"ageText,omitempty"`
	Evidence  value  `json:"evidence"`
}

// risk — то, что может пойти не так.
type risk struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	ImpactText string `json:"impactText,omitempty"`
	Spread     string `json:"spread,omitempty"`
	Evidence   value  `json:"evidence"`
}

// action — действие PM.
type action struct {
	Kind      string `json:"kind"`
	KindLabel string `json:"kindLabel"`
	Text      string `json:"text"`
	Why       string `json:"why,omitempty"`
}

// question — вопрос специалисту.
type question struct {
	N        int    `json:"n"`
	Text     string `json:"text"`
	Unlocks  string `json:"unlocks,omitempty"`
	Answered bool   `json:"answered"`
	Answer   value  `json:"answer"`
}

// artifact — файл, упомянутый в источниках.
type artifact struct {
	Name      string `json:"name"`
	Present   bool   `json:"present"`
	SizeText  string `json:"sizeText,omitempty"`
	WouldGive string `json:"wouldGive,omitempty"`
}

// slice — срез задачи целиком, готовый к выводу.
type slice struct {
	TaskID string `json:"taskId"`
	Head   head   `json:"head"`

	Passport struct {
		Title    value   `json:"title"`
		Author   value   `json:"author"`
		Assignee value   `json:"assignee"`
		OpenedAt value   `json:"openedAt"`
		Deadline value   `json:"deadline"`
		Shifts   []shift `json:"shifts"`
	} `json:"passport"`

	Goal struct {
		AsStated   value       `json:"asStated"`
		Clarified  value       `json:"clarified"`
		Criteria   []criterion `json:"criteria"`
		OutOfScope []value     `json:"outOfScope"`
	} `json:"goal"`

	Status struct {
		Stage      value       `json:"stage"`
		Readiness  value       `json:"readiness"`
		Milestones []milestone `json:"milestones"`
		Done       []value     `json:"done"`
		Left       []value     `json:"left"`
	} `json:"status"`

	Blockers []blocker `json:"blockers"`
	Risks    []risk    `json:"risks"`

	PMActions struct {
		Needed    []action `json:"needed"`
		NextCheck value    `json:"nextCheck"`
		Comment   string   `json:"comment,omitempty"`
	} `json:"pmActions"`

	Questions []question `json:"questions"`
	Artifacts []artifact `json:"artifacts"`
	Sources   []source   `json:"sources"`
}

// newSlice переводит срез в представление.
//
// Ни одного вычисления после этой функции не остаётся: все проценты, все дни и
// все склонения посчитаны здесь, из домена, на дату now.
func newSlice(sl domain.Slice, t domain.Task, list []domain.Source, now time.Time) slice {
	srcs := newSources(list)
	v := slice{TaskID: sl.TaskID}

	v.Head = newHead(sl, t, now)

	v.Passport.Title = newValue(sl.Passport.Title, srcs)
	v.Passport.Author = newValue(sl.Passport.Author, srcs)
	v.Passport.Assignee = newValue(sl.Passport.Assignee, srcs)
	v.Passport.OpenedAt = newValue(sl.Passport.OpenedAt, srcs)
	v.Passport.Deadline = newValue(sl.Passport.Deadline, srcs)
	v.Passport.Shifts = newShifts(sl.Passport.Shifts)

	v.Goal.AsStated = newValue(sl.Goal.AsStated, srcs)
	v.Goal.Clarified = newValue(sl.Goal.Clarified, srcs)
	v.Goal.Criteria = newCriteria(sl.Goal.Criteria)
	v.Goal.OutOfScope = newValues(sl.Goal.OutOfScope, srcs)

	v.Status.Stage = newValue(sl.Status.Stage, srcs)
	v.Status.Readiness = newValue(sl.Status.Readiness, srcs)
	v.Status.Milestones = newMilestones(sl.Status.Milestones, now)
	v.Status.Done = newValues(sl.Status.Done, srcs)
	v.Status.Left = newValues(sl.Status.Left, srcs)

	v.Blockers = newBlockers(sl.Blockers, srcs, now)
	v.Risks = newRisks(sl.Risks, srcs)

	v.PMActions.Needed = newActions(sl.PMActions.Needed)
	v.PMActions.NextCheck = newValue(sl.PMActions.NextCheck, srcs)
	v.PMActions.Comment = sl.PMActions.Comment

	v.Questions = newQuestions(sl.Questions, srcs)
	v.Artifacts = newArtifacts(sl.Artifacts)

	v.Sources = make([]source, 0, len(sl.SourceIDs))
	for _, id := range sl.SourceIDs {
		if src, ok := srcs[id]; ok {
			v.Sources = append(v.Sources, newSource(src, false))
		}
	}
	return v
}

func newHead(sl domain.Slice, t domain.Task, now time.Time) head {
	h := head{
		Project:   t.Project,
		Stage:     newValue(sl.Status.Stage, nil),
		Readiness: newValue(sl.Status.Readiness, nil),
		Version:   sl.Version,
		BuiltAt:   sl.BuiltAt.Format("02.01.2006, 15:04"),
		Analyst:   sl.Analyst,
	}

	h.Title = sl.Passport.Title.Text
	if h.Title == "" {
		h.Title = t.Title
	}

	// Доля берётся из тех же этапов, из которых сервис собрал подпись
	// «27 %»: полоса и подпись под ней не могут разойтись, потому что
	// считаются одним и тем же кодом.
	h.ReadinessShare, _ = domain.Readiness(sl.Status.Milestones)

	if card := newTask(t, now); card.Deadline != "" {
		h.Deadline, h.DeadlineNote, h.Overdue = card.Deadline, card.DeadlineNote, card.Overdue
	}
	if t.Budget != 0 {
		h.Budget = ru.Money(t.Budget)
	}

	if n := sl.Gaps(); n > 0 {
		h.GapsText = ru.Count(n, "пробел", "пробела", "пробелов") + " в срезе"
	}
	if over := domain.OverdueMilestones(sl.Status.Milestones, now); len(over) > 0 {
		h.OverdueText = fmt.Sprintf("%s просрочено, самый старый на %s",
			ru.Count(len(over), "этап", "этапа", "этапов"),
			ru.Days(over[0].OverdueDays(now)))
	}
	if b, ok := sl.OldestBlocker(); ok {
		h.BlockerText = fmt.Sprintf("блокер висит %s: %s", ru.Days(b.AgeDays(now)), b.Kind.Label())
	}
	if n := domain.Unexplained(sl.Passport.Shifts); n > 0 {
		h.ShiftsText = fmt.Sprintf("%s срока без объяснения",
			ru.Count(n, "перенос", "переноса", "переносов"))
	}
	if cs := sl.Goal.Criteria; len(cs) > 0 {
		h.CriteriaText = fmt.Sprintf("%d из %s приёмки выполнено",
			domain.MetCriteria(cs), ru.Count(len(cs), "критерия", "критериев", "критериев"))
	}
	return h
}

func newShifts(list []domain.DeadlineShift) []shift {
	out := make([]shift, 0, len(list))
	for _, s := range list {
		v := shift{Comment: s.Comment, Explained: s.Explained()}
		if !s.At.IsZero() {
			v.At = domain.FormatDate(s.At)
		}
		if !s.From.IsZero() {
			v.From = domain.FormatDate(s.From)
		}
		if !s.To.IsZero() {
			v.To = domain.FormatDate(s.To)
		}
		if !s.From.IsZero() && !s.To.IsZero() {
			v.MovedText = "на " + ru.Days(domain.DaysBetween(s.From, s.To)) + " вперёд"
		}
		out = append(out, v)
	}
	return out
}

func newCriteria(list []domain.Criterion) []criterion {
	out := make([]criterion, 0, len(list))
	for _, c := range list {
		out = append(out, criterion{N: c.N, Text: c.Text, Met: c.Met, Note: c.Note})
	}
	return out
}

func newMilestones(list []domain.Milestone, now time.Time) []milestone {
	out := make([]milestone, 0, len(list))
	for _, m := range list {
		v := milestone{
			ID:       m.ID,
			Title:    m.Title,
			Progress: ru.Percent(m.Progress),
			Share:    m.Progress,
			Done:     m.Done(),
			Evidence: m.Evidence,
		}
		if !m.Due.IsZero() {
			v.Due = domain.FormatDate(m.Due)
		}
		if d := m.OverdueDays(now); d > 0 {
			v.OverdueText = "просрочен на " + ru.Days(d)
		}
		out = append(out, v)
	}
	return out
}

func newBlockers(list []domain.Blocker, srcs sources, now time.Time) []blocker {
	out := make([]blocker, 0, len(list))
	for _, b := range list {
		v := blocker{
			ID:        b.ID,
			Summary:   b.Summary,
			Kind:      string(b.Kind),
			KindLabel: b.Kind.Label(),
			DependsOn: b.DependsOn,
			Evidence:  newValue(b.Evidence, srcs),
		}
		if !b.Since.IsZero() {
			v.Since = domain.FormatDate(b.Since)
			v.AgeText = "висит " + ru.Days(b.AgeDays(now))
		}
		out = append(out, v)
	}
	return out
}

func newRisks(list []domain.Risk, srcs sources) []risk {
	out := make([]risk, 0, len(list))
	for _, r := range list {
		v := risk{
			ID:       r.ID,
			Summary:  r.Summary,
			Spread:   r.Spread,
			Evidence: newValue(r.Evidence, srcs),
		}
		if r.DaysImpact > 0 {
			v.ImpactText = "срок +" + ru.Days(r.DaysImpact)
		}
		out = append(out, v)
	}
	return out
}

func newActions(list []domain.PMAction) []action {
	out := make([]action, 0, len(list))
	for _, a := range list {
		out = append(out, action{
			Kind:      string(a.Kind),
			KindLabel: a.Kind.Label(),
			Text:      a.Text,
			Why:       a.Why,
		})
	}
	return out
}

func newQuestions(list []domain.Question, srcs sources) []question {
	out := make([]question, 0, len(list))
	for _, q := range list {
		out = append(out, question{
			N:        q.N,
			Text:     q.Text,
			Unlocks:  q.Unlocks,
			Answered: q.Answered(),
			Answer:   newValue(q.Answer, srcs),
		})
	}
	return out
}

func newArtifacts(list []domain.Artifact) []artifact {
	out := make([]artifact, 0, len(list))
	for _, a := range list {
		v := artifact{Name: a.Name, Present: a.Present, WouldGive: a.WouldGive}
		if a.Bytes > 0 {
			v.SizeText = fmt.Sprintf("%s КБ", ru.Fixed(float64(a.Bytes)/1024, 1))
		}
		out = append(out, v)
	}
	return out
}

// --- чаты портала ---

// chat — строка выпадающего списка чатов и она же строка списка закреплённых.
// Обе роли обходятся одной формой: браузеру в них нужно одно и то же —
// значение для отправки и готовая подпись для показа.
type chat struct {
	DialogID string `json:"dialogId"`
	Title    string `json:"title"`

	// Label — подпись целиком, вместе с датой. Собрана здесь по той же причине,
	// что и все остальные подписи: дату в браузере пришлось бы форматировать
	// второй раз, и рано или поздно она отформатировалась бы иначе.
	Label string `json:"label"`

	// SyncText — что известно про подтяжку. Пусто у чатов из портала: они ещё
	// не закреплены, и подтягивать из них нечего.
	SyncText string `json:"syncText,omitempty"`
}

// chatOptions — ответ на запрос списка чатов портала.
//
// Configured и пустой список — разные вещи, и различать их должен браузер:
// «Bitrix24 не настроен» и «портал не отдал ни одного чата» человек исправляет
// по-разному. Поясняет разницу Note; форма при этом работает в любом случае.
type chatOptions struct {
	Configured bool   `json:"configured"`
	Note       string `json:"note"`
	Chats      []chat `json:"chats"`
}

func newChatOptions(configured bool, list []bitrix.Chat) chatOptions {
	opts := chatOptions{Configured: configured, Chats: newChats(list)}
	switch {
	case !configured:
		opts.Note = "Bitrix24 не настроен: задача создастся без чата"
	case len(list) == 0:
		opts.Note = "портал не отдал ни одного чата"
	default:
		// Ограничение источника, а не наше: im.recent.list показывает недавние
		// чаты владельца вебхука, а не все чаты портала.
		opts.Note = "недавние чаты владельца вебхука; если нужного нет — зайдите в него в портале"
	}
	return opts
}

func newChats(list []bitrix.Chat) []chat {
	out := make([]chat, 0, len(list))
	for _, c := range list {
		v := chat{DialogID: c.DialogID, Title: c.Title, Label: c.Title}
		if !c.LastActivity.IsZero() {
			v.Label += " · " + domain.FormatDate(c.LastActivity)
		}
		out = append(out, v)
	}
	return out
}

// newLinks описывает уже закреплённые чаты.
func newLinks(list []domain.ChatLink) []chat {
	out := make([]chat, 0, len(list))
	for _, l := range list {
		// Подписи может не быть: чат мог быть закреплён не из формы. Тогда
		// идентификатор диалога — единственное, что можно показать, и это лучше
		// пустой строки.
		title := l.Title
		if title == "" {
			title = l.DialogID
		}

		v := chat{DialogID: l.DialogID, Title: title, Label: title}
		switch {
		case !l.Synced():
			v.SyncText = "сообщения ещё не переносились"
		case l.LastSyncAt.IsZero():
			// Курсор есть, а времени похода нет. Значения расходятся не сами
			// собой, но Synced смотрит только на курсор, и подпись не должна
			// рассыпаться из-за этого расхождения.
			v.SyncText = fmt.Sprintf("сообщения до №%d", l.LastMessageID)
		default:
			v.SyncText = fmt.Sprintf("сообщения до №%d, читали %s",
				l.LastMessageID, domain.FormatDate(l.LastSyncAt))
		}
		out = append(out, v)
	}
	return out
}
