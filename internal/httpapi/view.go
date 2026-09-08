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
	"sort"
	"strconv"
	"time"

	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
	"github.com/Anemiaaaa/reestr/internal/service"
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
	case domain.OriginStated:
		return "✎"
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

	// Field — адрес значения в срезе, если его можно поправить. Пусто у всего
	// остального, и по этой пустоте интерфейс решает, показывать ли карандаш:
	// список правимых полей один, и живёт он в домене, а не в двух местах.
	Field string `json:"field,omitempty"`

	// FieldLabel — подпись поля для формы правки. Оттуда же, из домена.
	FieldLabel string `json:"fieldLabel,omitempty"`
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

// editable помечает значение адресом правимого поля.
//
// Отдельная обёртка, а не параметр newValue: значений в срезе десятки, правимых
// девять, и лишний аргумент у всех остальных вызовов означал бы «здесь тоже
// можно было бы, просто не стали».
func editable(v value, field string) value {
	f, ok := domain.EditableField(field)
	if !ok {
		return v
	}
	v.Field, v.FieldLabel = f.Field, f.Label
	return v
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

	// EditedBy — кто собрал версию правкой. Пусто у версии, собранной разбором.
	EditedBy string `json:"editedBy,omitempty"`
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

	// Days — то же влияние числом. Нужно форме правки: разобрать его обратно из
	// «срок +3 дня» значило бы читать собственный вывод как ввод.
	Days int `json:"days,omitempty"`
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
		AsStated  value       `json:"asStated"`
		Clarified value       `json:"clarified"`
		Criteria  []criterion `json:"criteria"`

		// CriteriaText — счёт у подзаголовка списка: «1 из 7». Короче, чем та же
		// сводка в шапке среза, и намеренно: над списком уже написано, что это
		// критерии приёмки, и повторять это в счёте незачем.
		CriteriaText string `json:"criteriaText,omitempty"`

		OutOfScope []value `json:"outOfScope"`
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

	// Edit — что в этом срезе можно поправить: адрес поля, подпись и вид формы.
	// Список приходит с сервера целиком, а не зашит в браузере: он закрытый и
	// живёт в домене, и второй его список означал бы, что однажды они разойдутся.
	Edit []editField `json:"edit"`

	// EditedFields — поля, которые уже правили руками. Интерфейс по ним
	// предлагает вернуть как было, и метит их в списке.
	EditedFields []string `json:"editedFields"`

	// Kinds — закрытые списки для форм правки. Вид блокера определяет, к кому
	// идти, чтобы его снять; вид действия отличает действие от его отсутствия.
	// Оба списка закрытые, и выбирать из них человек должен, а не печатать.
	Kinds struct {
		Blockers []option `json:"blockers"`
		Actions  []option `json:"actions"`
	} `json:"kinds"`
}

// editField — правимое поле в ответе.
type editField struct {
	Field string `json:"field"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// option — строка закрытого списка для выпадающего поля формы.
type option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func newEditFields() []editField {
	all := domain.Editable()
	out := make([]editField, 0, len(all))
	for _, f := range all {
		out = append(out, editField{Field: f.Field, Label: f.Label, Kind: string(f.Kind)})
	}
	return out
}

// newSlice переводит срез в представление.
//
// Ни одного вычисления после этой функции не остаётся: все проценты, все дни и
// все склонения посчитаны здесь, из домена, на дату now.
func newSlice(sl domain.Slice, t domain.Task, list []domain.Source, now time.Time, edited []string) slice {
	srcs := newSources(list)
	v := slice{TaskID: sl.TaskID}

	v.Head = newHead(sl, t, now)

	v.Passport.Title = editable(newValue(sl.Passport.Title, srcs), "passport.title")
	v.Passport.Author = editable(newValue(sl.Passport.Author, srcs), "passport.author")
	v.Passport.Assignee = editable(newValue(sl.Passport.Assignee, srcs), "passport.assignee")
	v.Passport.OpenedAt = editable(newValue(sl.Passport.OpenedAt, srcs), "passport.openedAt")
	v.Passport.Deadline = editable(newValue(sl.Passport.Deadline, srcs), "passport.deadline")
	v.Passport.Shifts = newShifts(sl.Passport.Shifts)

	v.Goal.AsStated = editable(newValue(sl.Goal.AsStated, srcs), "goal.asStated")
	v.Goal.Clarified = editable(newValue(sl.Goal.Clarified, srcs), "goal.clarified")
	v.Goal.Criteria = newCriteria(sl.Goal.Criteria)
	if cs := sl.Goal.Criteria; len(cs) > 0 {
		v.Goal.CriteriaText = fmt.Sprintf("%d из %d", domain.MetCriteria(cs), len(cs))
	}
	v.Goal.OutOfScope = newValues(sl.Goal.OutOfScope, srcs)

	v.Status.Stage = editable(newValue(sl.Status.Stage, srcs), "status.stage")
	v.Status.Readiness = newValue(sl.Status.Readiness, srcs)
	v.Status.Milestones = newMilestones(sl.Status.Milestones, now)
	v.Status.Done = newValues(sl.Status.Done, srcs)
	v.Status.Left = newValues(sl.Status.Left, srcs)

	v.Blockers = newBlockers(sl.Blockers, srcs, now)
	v.Risks = newRisks(sl.Risks, srcs)

	v.PMActions.Needed = newActions(sl.PMActions.Needed)
	v.PMActions.NextCheck = editable(newValue(sl.PMActions.NextCheck, srcs), "pmActions.nextCheck")
	v.PMActions.Comment = sl.PMActions.Comment

	v.Questions = newQuestions(sl.Questions, srcs)
	v.Artifacts = newArtifacts(sl.Artifacts)

	v.Sources = make([]source, 0, len(sl.SourceIDs))
	for _, id := range sl.SourceIDs {
		if src, ok := srcs[id]; ok {
			v.Sources = append(v.Sources, newSource(src, false))
		}
	}

	v.Edit = newEditFields()
	v.EditedFields = edited
	if v.EditedFields == nil {
		v.EditedFields = []string{}
	}
	for _, k := range []domain.BlockerKind{
		domain.BlockerNoInfo, domain.BlockerNoAccess, domain.BlockerDependency,
		domain.BlockerTechnical, domain.BlockerNoApproach, domain.BlockerNoTime,
	} {
		v.Kinds.Blockers = append(v.Kinds.Blockers, option{Value: string(k), Label: k.Label()})
	}
	for _, k := range []domain.PMActionKind{
		domain.ActionApprove, domain.ActionAccess, domain.ActionConnect,
		domain.ActionClarify, domain.ActionEscalate, domain.ActionNone,
	} {
		v.Kinds.Actions = append(v.Kinds.Actions, option{Value: string(k), Label: k.Label()})
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
		EditedBy:  sl.EditedBy,
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
			Days:     r.DaysImpact,
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

// portalTask — задача портала в выпадающем списке формы.
//
// Кроме номера и подписи здесь лежат поля карточки: браузер подставляет их в
// форму, чтобы PM не перепечатывал то, что уже названо в Bitrix. Правку они не
// запрещают — в портале задачу могли назвать служебно, и реестр обязан дать её
// переписать.
type portalTask struct {
	ID    string `json:"id"`
	Label string `json:"label"`

	// Title, Author, Assignee, Deadline, OpenedAt — ровно те имена, что у полей
	// формы. Совпадение намеренное: браузер раскладывает их по полям одним
	// перебором, без таблицы соответствий, которая разошлась бы с формой.
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	Deadline string `json:"deadline,omitempty"`
	OpenedAt string `json:"openedAt,omitempty"`
}

// taskOptions — ответ на запрос задач портала. Configured и пустой список
// различаются по той же причине, что и в chatOptions: человек исправляет их
// по-разному.
type taskOptions struct {
	Configured bool         `json:"configured"`
	Note       string       `json:"note"`
	Tasks      []portalTask `json:"tasks"`
}

func newTaskOptions(configured bool, list []bitrix.Task) taskOptions {
	opts := taskOptions{Configured: configured, Tasks: newPortalTasks(list)}
	switch {
	case !configured:
		opts.Note = "Bitrix24 не настроен: задача создастся без чата"
	case len(list) == 0:
		opts.Note = "портал не отдал ни одной задачи"
	case len(opts.Tasks) < len(list):
		// Задача без чата в выбор не попадает, и молчать об этом нельзя: человек
		// ищет её глазами и не находит.
		opts.Note = fmt.Sprintf("задач без чата: %d — чат заводится при первом событии по задаче",
			len(list)-len(opts.Tasks))
	default:
		opts.Note = "поля формы заполнятся из карточки задачи; их можно исправить"
	}
	return opts
}

func newPortalTasks(list []bitrix.Task) []portalTask {
	out := make([]portalTask, 0, len(list))
	for _, t := range list {
		// Задача без чата отсеивается здесь, а не в браузере: выбрать её нельзя,
		// а строка в списке, на которую нельзя нажать, — обещание, которого
		// интерфейс не сдержит.
		if t.DialogID() == "" {
			continue
		}

		v := portalTask{
			ID:       t.ID,
			Title:    t.Title,
			Author:   t.Author,
			Assignee: t.Assignee,
			Label:    "#" + t.ID + " " + t.Title,
		}
		if t.Closed {
			v.Label += " · завершена"
		}
		// Даты уходят в браузер как ГГГГ-ММ-ДД: их принимает поле input[type=date]
		// и понимает разбор запроса. Незаполненная дата остаётся пустой строкой —
		// подставлять на её место сегодняшнюю значило бы придумать срок.
		if !t.Deadline.IsZero() {
			v.Deadline = t.Deadline.Format(dateLayout)
		}
		if !t.CreatedAt.IsZero() {
			v.OpenedAt = t.CreatedAt.Format(dateLayout)
		}
		out = append(out, v)
	}
	return out
}

// --- подтяжка ---

// pullReport — итог подтяжки для человека.
//
// Готовой фразой, а не набором чисел. Собирать её в браузере значило бы
// раскладывать «1 сообщение / 2 сообщения / 5 сообщений» по падежам второй раз,
// а этим уже занимается пакет ru — на стороне сервера и в одном месте.
type pullReport struct {
	Added int    `json:"added"`
	Text  string `json:"text"`
}

func newPullReport(res []service.PullResult) pullReport {
	out := pullReport{}
	for _, r := range res {
		out.Added += r.Added
	}

	switch {
	case len(res) == 0:
		// Закреплённых чатов нет вовсе. Это не отказ: задача могла вестись без
		// портала, и человеку нужно понять, почему кнопка ничего не дала.
		out.Text = "у задачи нет закреплённого чата Bitrix24"
	case out.Added == 0:
		out.Text = "новых сообщений нет"
	default:
		out.Text = "перенесено " + ru.Count(out.Added, "сообщение", "сообщения", "сообщений")
	}
	return out
}

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

	// TaskRef — задача портала, чей это чат, готовой подписью. Пусто у чата,
	// закреплённого самого по себе.
	TaskRef string `json:"taskRef,omitempty"`

	// Kind и KindLabel — род чата: переписка с клиентом, чат задачи, групповой,
	// личный. По DialogID этого не видно, а разница существенная: в открытой
	// линии контакт-центра говорит клиент.
	Kind      string `json:"kind,omitempty"`
	KindLabel string `json:"kindLabel,omitempty"`

	// Client отмечает переписку с клиентом. Отдельным признаком, а не сравнением
	// строк в браузере: правило «чьи это слова» одно и живёт в домене.
	Client bool `json:"client,omitempty"`
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

func newChatOptions(configured bool, owner string, list []bitrix.Chat) chatOptions {
	// В списке — только переписки с клиентами. Прикрепляют к задаче ради них:
	// чат задачи приходит вместе с самой задачей портала, а внутренние
	// обсуждения в срез идут редко и по одному. Остальные виды из портала никуда
	// не делись — их берут полем «номер чата», и об этом говорит надпись.
	client := make([]bitrix.Chat, 0, len(list))
	for _, c := range list {
		if chatKindOf(c).Client() {
			client = append(client, c)
		}
	}

	opts := chatOptions{Configured: configured, Chats: newChats(client)}

	// Имя владельца вебхука в надписи — не вежливость, а диагноз. Портал
	// отдаёт реестру переписку одного человека, и переписка контакт-центра у
	// каждого оператора своя: не найдя в списке своего чата, человек ищет
	// поломку в реестре, пока не увидит, что список вообще не его.
	// Имя стоит в скобках именительным падежом, а не «чаты Александра
	// Волощука»: склонять имена сотрудников портала пришлось бы кодом, а
	// ошибиться в чужой фамилии — хуже, чем обойтись без падежа.
	whose := "того, на кого выдан вебхук"
	if owner != "" {
		whose += " (это " + owner + ")"
	}

	switch {
	case !configured:
		opts.Note = "Bitrix24 не настроен: задача создастся без чата"
	case len(list) == 0:
		opts.Note = "портал не отдал ни одного чата " + whose
	case len(client) == 0:
		opts.Note = "среди чатов " + whose + " нет ни одной переписки с клиентом. " +
			"Контакт-центр ведут операторы, и у каждого она своя"
	default:
		opts.Note = ru.Count(len(client), "переписка", "переписки", "переписок") +
			" с клиентами из " + strconv.Itoa(len(list)) + " чатов " + whose + ". " +
			"Чат другого вида — полем ниже, по номеру"
	}
	return opts
}

func newChats(list []bitrix.Chat) []chat {
	out := make([]chat, 0, len(list))
	for _, c := range list {
		kind := chatKindOf(c)
		v := chat{
			DialogID: c.DialogID, Title: c.Title, Label: c.Title,
			Kind: string(kind), KindLabel: kind.Label(), Client: kind.Client(),
		}
		// Род чата в подписи — только у тех, кто не переписка с клиентом. Список
		// закрепления состоит из них одних, и повторённое девять раз «переписка с
		// клиентом» отнимало бы ширину у названия, ничего не различая.
		if !kind.Client() {
			v.Label = kind.Label() + " · " + v.Label
		}
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

		v := chat{
			DialogID: l.DialogID, Title: title, Label: title,
			Kind: string(l.Kind), KindLabel: l.Kind.Label(), Client: l.Kind.Client(),
		}
		if l.ExternalTaskID != "" {
			v.TaskRef = "задача Bitrix24 №" + l.ExternalTaskID
		}
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

// --- история версий ---

// sliceRef — строка списка версий.
type sliceRef struct {
	Version int    `json:"version"`
	BuiltAt string `json:"builtAt"`
	Label   string `json:"label"`
}

func newSliceRefs(list []domain.SliceRef) []sliceRef {
	out := make([]sliceRef, 0, len(list))
	for _, r := range list {
		v := sliceRef{
			Version: r.Version,
			BuiltAt: r.BuiltAt.Format("02.01.2006, 15:04"),
		}
		v.Label = fmt.Sprintf("v%d · %s", r.Version, v.BuiltAt)
		out = append(out, v)
	}
	return out
}

// change — одно различие между версиями.
type sliceChange struct {
	Section string `json:"section"`
	Field   string `json:"field"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

// diff — ответ сравнения двух версий.
//
// Числа и подписи посчитаны здесь, а не в браузере: «изменений нет» — это
// содержательный ответ, а не пустой список, и решать, как его назвать, должен
// один слой.
type diff struct {
	Before  sliceRef      `json:"before"`
	After   sliceRef      `json:"after"`
	Text    string        `json:"text"`
	Changes []sliceChange `json:"changes"`
}

func newDiff(before, after domain.Slice, changes []domain.SliceChange) diff {
	d := diff{
		Before:  newSliceRefs([]domain.SliceRef{before.Ref()})[0],
		After:   newSliceRefs([]domain.SliceRef{after.Ref()})[0],
		Changes: make([]sliceChange, 0, len(changes)),
	}
	for _, c := range changes {
		d.Changes = append(d.Changes, sliceChange{
			Section: c.Section, Field: c.Field,
			Kind: string(c.Kind), Label: c.Kind.Label(),
			Before: c.Before, After: c.After,
		})
	}

	if len(changes) == 0 {
		// Совпадение версий — ответ, а не пустота: пересборка после нового
		// материала могла ничего не поменять в выводах.
		d.Text = fmt.Sprintf("версии %d и %d совпадают по содержанию",
			before.Version, after.Version)
	} else {
		d.Text = fmt.Sprintf("между версиями %d и %d — %s",
			before.Version, after.Version,
			ru.Count(len(changes), "различие", "различия", "различий"))
	}
	return d
}

// --- журнал инцидентов ---

// incident — случай в журнале.
type incident struct {
	ID         string `json:"id"`
	Employee   string `json:"employee"`
	Project    string `json:"project"`
	TaskID     string `json:"taskId,omitempty"`
	TaskTitle  string `json:"taskTitle,omitempty"`
	At         string `json:"at"`
	CreatedAt  string `json:"createdAt"`
	Block      string `json:"block"`
	BlockLabel string `json:"blockLabel"`
	Text       string `json:"text"`
	External   bool   `json:"external"`

	// ControlText — «в зоне контроля специалиста» словами, как в таблице
	// руководителя. Строка, а не признак: интерфейс ничего не решает сам, а
	// «да/нет» тут значат больше, чем галочка, — от них зависит, идёт ли случай
	// в оценку.
	ControlText string `json:"controlText"`

	// EscalatedText — «была эскалация» словами, вместе с датой, если она
	// названа: «да, 05.09.2026».
	EscalatedText string `json:"escalatedText"`

	ManagerNote string `json:"managerNote,omitempty"`
	RecordedBy  string `json:"recordedBy,omitempty"`

	// Note объясняет, почему внешняя помеха записана, но в оценку не идёт.
	// Иначе строка в журнале читается как претензия к человеку.
	Note string `json:"note,omitempty"`
}

func newIncidents(list []domain.Incident, titles map[string]string) []incident {
	out := make([]incident, 0, len(list))
	for _, in := range list {
		v := incident{
			ID: in.ID, Employee: in.Employee, Project: in.Project, TaskID: in.TaskID,
			TaskTitle:   titles[in.TaskID],
			At:          domain.FormatDate(in.At),
			CreatedAt:   domain.FormatDate(in.CreatedAt),
			Block:       string(in.Block),
			BlockLabel:  in.Block.Label(),
			Text:        in.Text,
			External:    in.External,
			ControlText: "да",
			ManagerNote: in.ManagerNote,
			RecordedBy:  in.RecordedBy,
		}
		if in.External {
			v.ControlText = "нет"
			v.Note = "вне зоны контроля специалиста — в оценку не идёт"
		}

		v.EscalatedText = "нет"
		if in.Escalated {
			v.EscalatedText = "да"
			if d := domain.FormatDate(in.EscalatedAt); d != "" {
				v.EscalatedText += ", " + d
			}
		}
		out = append(out, v)
	}
	return out
}

// kpiBlock — блок оценки для выпадающего списка формы.
type kpiBlock struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func newKPIBlocks() []kpiBlock {
	blocks := domain.KPIBlocks()
	out := make([]kpiBlock, 0, len(blocks))
	for _, b := range blocks {
		// Вес в подписи: он объясняет, почему блоки не равны между собой, и
		// избавляет от обращения к презентации.
		out = append(out, kpiBlock{
			Value: string(b),
			Label: fmt.Sprintf("%s (%.0f%%)", b.Label(), b.Weight()*100),
		})
	}
	return out
}

// blockStat — сколько случаев накопилось по блоку KPI.
//
// Ради этого журнал и ведут: одна запись — повод для разговора, а десять по
// одному блоку — повод менять работу. Увидеть это, читая записи подряд,
// нельзя: они лежат по дням, а вопрос стоит по блокам.
type blockStat struct {
	Block      string `json:"block"`
	Label      string `json:"label"`
	WeightText string `json:"weightText"`

	// CountText — сколько случаев по блоку, уже со склонением. Счёт идёт по
	// всем записям: внешняя помеха в оценку не идёт, но из журнала не исчезает.
	CountText string `json:"countText"`

	// Counted — сколько из них идут в оценку, External — сколько отведено как
	// внешняя помеха. Разделены, потому что решение по блоку принимают по
	// первому числу, а объясняют вторым.
	Counted  int `json:"counted"`
	External int `json:"external"`

	// Share — доля от самого нагруженного блока, для ширины полосы. Не доля от
	// всех случаев: сравнивают блоки между собой, а не с общим числом.
	Share float64 `json:"share"`

	// People — кто и сколько раз попал в блок, крупные первыми.
	People []string `json:"people,omitempty"`

	// Note объясняет пустой блок. Пустой блок — тоже ответ: по нему претензий
	// нет, и это стоит сказать словами, а не оставлять прочерк.
	Note string `json:"note,omitempty"`
}

// personStat — что накопилось за период у одного человека.
//
// Ради этого разреза журнал и ведут: оценку ставят человеку за месяц, а записи
// лежат по дням и по проектам. Сложить их глазами при полусотне строк нельзя.
type personStat struct {
	Employee string `json:"employee"`

	// CountText — сколько случаев всего, со склонением; Counted — сколько из них
	// идут в оценку. Разделены, потому что решение принимают по второму числу, а
	// объясняют первым.
	CountText string `json:"countText"`
	Counted   int    `json:"counted"`
	External  int    `json:"external"`

	// Escalated — сколько раз человек сообщил о проблеме сам. Довод в его
	// пользу: блок «эскалация и самостоятельность» штрафует не проблему, а
	// молчание о ней, и без этого числа разрез читался бы как обвинительный.
	Escalated int `json:"escalated"`

	// Share — доля от самого нагруженного, для ширины полосы.
	Share float64 `json:"share"`

	// Blocks — по каким блокам KPI, крупные первыми.
	Blocks []string `json:"blocks,omitempty"`

	// Projects — в каких работах это случилось.
	Projects []string `json:"projects,omitempty"`
}

// journal — ответ журнала целиком.
type journal struct {
	Incidents []incident   `json:"incidents"`
	Blocks    []kpiBlock   `json:"blocks"`
	Stats     []blockStat  `json:"stats"`
	People    []personStat `json:"people"`

	// PeriodText — за какой отрезок собран журнал, словами. Пусто, если
	// показывают всё.
	PeriodText string `json:"periodText,omitempty"`

	// Text — сводка одной строкой. Считается здесь, а не в браузере: правило
	// «внешняя помеха в оценку не идёт» одно, и применять его в двух местах
	// значило бы дать ему разойтись.
	Text string `json:"text"`

	// StatsText называет самый нагруженный блок словами. Полосы показывают
	// соотношение, но вывод из них человек делает сам, а вывод здесь ровно
	// один, и сказать его короче, чем прочитать по полосам.
	StatsText string `json:"statsText"`
}

func newJournal(list []domain.Incident, titles map[string]string, from, to time.Time) journal {
	j := journal{
		Incidents:  newIncidents(list, titles),
		Blocks:     newKPIBlocks(),
		Stats:      newBlockStats(list),
		People:     newPeopleStats(list),
		PeriodText: periodText(from, to),
	}

	counted := 0
	for _, in := range list {
		if in.Countable() {
			counted++
		}
	}
	switch {
	case len(list) == 0:
		j.Text = "журнал пуст"
	case counted == len(list):
		j.Text = ru.Count(len(list), "случай", "случая", "случаев")
	default:
		j.Text = fmt.Sprintf("%s, из них %d вне зоны контроля",
			ru.Count(len(list), "случай", "случая", "случаев"), len(list)-counted)
	}

	// Вывод называется только когда он есть. При равном счёте у двух блоков
	// «больше всего проблем» — неправда, и молчание тут честнее.
	if len(j.Stats) > 0 && j.Stats[0].Counted > 0 {
		if len(j.Stats) == 1 || j.Stats[1].Counted < j.Stats[0].Counted {
			j.StatsText = "больше всего в оценку идёт по блоку «" + j.Stats[0].Label + "»"
		} else {
			j.StatsText = "ни один блок не выделяется: случаи распределены поровну"
		}
	}
	return j
}

// newBlockStats считает случаи по блокам KPI.
//
// Блоки перечисляются все четыре, даже пустые: пустой блок — тоже ответ, по
// нему претензий нет, и на глаз это видно только когда он стоит в ряду.
func newBlockStats(list []domain.Incident) []blockStat {
	type tally struct {
		all, counted, external int
		people                 map[string]int
	}

	byBlock := make(map[domain.KPIBlock]*tally, len(domain.KPIBlocks()))
	for _, b := range domain.KPIBlocks() {
		byBlock[b] = &tally{people: map[string]int{}}
	}
	for _, in := range list {
		t, ok := byBlock[in.Block]
		if !ok {
			// Блок вне закрытого списка — запись из будущей версии схемы. В
			// сводку она не идёт, но и молча числа не портит.
			continue
		}
		t.all++
		if in.Countable() {
			t.counted++
		} else {
			t.external++
		}
		t.people[in.Employee]++
	}

	// Полосы меряются от самого нагруженного блока: сравнивают блоки между
	// собой, а не с общим числом случаев.
	top := 0
	for _, t := range byBlock {
		if t.counted > top {
			top = t.counted
		}
	}

	out := make([]blockStat, 0, len(byBlock))
	for _, b := range domain.KPIBlocks() {
		t := byBlock[b]
		s := blockStat{
			Block:      string(b),
			Label:      b.Label(),
			WeightText: fmt.Sprintf("%.0f%%", b.Weight()*100),
			CountText:  ru.Count(t.all, "случай", "случая", "случаев"),
			Counted:    t.counted,
			External:   t.external,
		}
		if top > 0 {
			s.Share = float64(t.counted) / float64(top)
		}
		if t.all == 0 {
			s.Note = "претензий нет"
		}
		s.People = topPeople(t.people)
		out = append(out, s)
	}

	// Нагруженные первыми — вопрос стоит «где больше всего». При равном счёте
	// порядок остаётся тем, в каком блоки идут в системе оплаты: вес там убывает,
	// и из двух равных блоков выше окажется более весомый.
	sort.SliceStable(out, func(i, k int) bool {
		if out[i].Counted != out[k].Counted {
			return out[i].Counted > out[k].Counted
		}
		return out[i].External > out[k].External
	})
	return out
}

// topPeople раскладывает счёт по людям, крупные первыми.
func topPeople(counts map[string]int) []string {
	if len(counts) == 0 {
		return nil
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	// Сортировка по имени вторым ключом нужна не для красоты: без неё порядок
	// равных берётся из обхода карты, а он в Go случаен — сводка меняла бы вид
	// на каждом обновлении страницы.
	sort.Slice(names, func(i, k int) bool {
		if counts[names[i]] != counts[names[k]] {
			return counts[names[i]] > counts[names[k]]
		}
		return names[i] < names[k]
	})

	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, fmt.Sprintf("%s — %d", name, counts[name]))
	}
	return out
}

// newPeopleStats складывает случаи по людям, нагруженные первыми.
//
// Считается по зачтённым, а не по всем: решение об оценке принимают по ним.
// Отведённые случаи и эскалации идут рядом отдельными числами — без них разрез
// читался бы как обвинительный список, а половина записей в журнале объясняет
// срыв, а не обвиняет.
func newPeopleStats(list []domain.Incident) []personStat {
	type tally struct {
		all, counted, external, escalated int
		blocks                            map[domain.KPIBlock]int
		projects                          map[string]bool
	}

	byName := map[string]*tally{}
	for _, in := range list {
		t, ok := byName[in.Employee]
		if !ok {
			t = &tally{blocks: map[domain.KPIBlock]int{}, projects: map[string]bool{}}
			byName[in.Employee] = t
		}
		t.all++
		if in.Countable() {
			t.counted++
		} else {
			t.external++
		}
		if in.Escalated {
			t.escalated++
		}
		t.blocks[in.Block]++
		if in.Project != "" {
			t.projects[in.Project] = true
		}
	}

	top := 0
	for _, t := range byName {
		if t.counted > top {
			top = t.counted
		}
	}

	out := make([]personStat, 0, len(byName))
	for name, t := range byName {
		s := personStat{
			Employee:  name,
			CountText: ru.Count(t.all, "случай", "случая", "случаев"),
			Counted:   t.counted,
			External:  t.external,
			Escalated: t.escalated,
		}
		if top > 0 {
			s.Share = float64(t.counted) / float64(top)
		}
		// Блоки идут в порядке системы оплаты, а не по счёту: он у них общий с
		// весами, и «дисциплина 2, сроки 1» человек читает вместе с тем, что
		// сроки весят тридцать процентов, а дисциплина двадцать.
		for _, b := range domain.KPIBlocks() {
			if n := t.blocks[b]; n > 0 {
				s.Blocks = append(s.Blocks, fmt.Sprintf("%s — %d", b.Label(), n))
			}
		}
		for p := range t.projects {
			s.Projects = append(s.Projects, p)
		}
		sort.Strings(s.Projects)
		out = append(out, s)
	}

	// Нагруженные первыми; при равном счёте — по имени, иначе порядок брался бы
	// из обхода карты и менялся на каждом обновлении страницы.
	sort.Slice(out, func(i, k int) bool {
		if out[i].Counted != out[k].Counted {
			return out[i].Counted > out[k].Counted
		}
		return out[i].Employee < out[k].Employee
	})
	return out
}

// periodText называет отрезок словами. Пусто означает «показываем всё» — тогда
// и говорить не о чем.
func periodText(from, to time.Time) string {
	switch {
	case from.IsZero() && to.IsZero():
		return ""
	case from.IsZero():
		return "по " + domain.FormatDate(to)
	case to.IsZero():
		return "с " + domain.FormatDate(from)
	}
	return domain.FormatDate(from) + " — " + domain.FormatDate(to)
}

// chatKindOf переводит род чата портала в род реестра.
//
// Тот же перевод, что и в сервисе, но здесь он нужен для списка выбора: там
// чат ещё не закреплён и связи с родом у него нет. Общей функции у них нет
// намеренно — сервис переводит, чтобы сохранить, а транспорт, чтобы показать, и
// сводить это в один вызов значило бы связать слои ради экономии четырёх строк.
func chatKindOf(c bitrix.Chat) domain.ChatKind {
	switch c.Kinded() {
	case bitrix.HintLines:
		return domain.ChatLines
	case bitrix.HintTask:
		return domain.ChatTask
	case bitrix.HintGroup:
		return domain.ChatGroup
	}
	return domain.ChatPrivate
}
