package schema

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/domain"
)

// DateLayout — вид даты, в котором модель обязана называть даты.
const DateLayout = "2006-01-02"

// Rejection — отклонённое утверждение модели.
//
// Отклонения собираются, а не выбрасываются молча. Отброшенный факт — это не
// шум, а измерение: по нему видно, врёт модель редко или постоянно, и на каких
// именно полях. Молчаливая фильтрация выглядела бы как безупречный разбор,
// который просто мало что нашёл.
type Rejection struct {
	// Field — куда метило утверждение: «stage», «blockers[2].evidence».
	Field  string
	Reason string

	SourceID string
	Quote    string
}

func (r Rejection) String() string {
	if r.Quote == "" {
		return fmt.Sprintf("%s: %s", r.Field, r.Reason)
	}
	return fmt.Sprintf("%s: %s (источник %s, цитата %q)", r.Field, r.Reason, r.SourceID, r.Quote)
}

// checker проверяет утверждения модели против источников, которые ей давали.
type checker struct {
	// bodies — тексты источников по идентификатору. Проверка цитаты идёт по
	// нормализованному тексту, поэтому он готовится один раз: источники длинные,
	// а цитат в ответе десятки.
	bodies map[string]string

	rejected []Rejection
}

func newChecker(sources []domain.Source) *checker {
	c := &checker{bodies: make(map[string]string, len(sources))}
	for _, s := range sources {
		c.bodies[s.ID] = normalize(s.Body)
	}
	return c
}

func (c *checker) reject(field, reason string, v Value) {
	c.rejected = append(c.rejected, Rejection{
		Field: field, Reason: reason, SourceID: v.SourceID, Quote: v.Quote,
	})
}

// check переводит заявление модели в проверенное значение.
//
// Второй результат говорит, устояло ли утверждение. Отклонённое значение
// возвращается как domain.Missing с объяснением, а не пустым: поле «этап»,
// молча ставшее пустым, читается как «модель не нашла», хотя на деле она
// нашла и была уличена.
func (c *checker) check(field string, v Value) (domain.Value, bool) {
	text := strings.TrimSpace(v.Text)

	// Поле, которого в ответе нет вовсе, — это не выдумка, а пропуск, и сказать
	// о нём надо иначе. «Неизвестное происхождение «»» вместо «поле не
	// заполнено» отправило бы читателя лога искать несуществующую ошибку схемы.
	if v.Empty() {
		c.reject(field, "поле не заполнено", v)
		return domain.Missing("модель не заполнила поле"), false
	}

	switch strings.TrimSpace(v.Origin) {
	case string(domain.OriginQuoted):
		body, ok := c.bodies[v.SourceID]
		switch {
		case v.SourceID == "":
			c.reject(field, "цитата без источника", v)
		case !ok:
			c.reject(field, "ссылка на неизвестный источник", v)
		case strings.TrimSpace(v.Quote) == "":
			c.reject(field, "происхождение «цитата», а цитаты нет", v)
		case text == "":
			c.reject(field, "значение пустое", v)
		case !strings.Contains(body, normalize(v.Quote)):
			// Здесь и держится всё доверие к срезу. Цитата, которой нет в
			// источнике, — выдумка, и отличить её от правды иначе нечем:
			// звучит она ровно так же убедительно.
			c.reject(field, "цитаты нет в источнике дословно", v)
		default:
			return domain.Quoted(text, v.SourceID, strings.TrimSpace(v.Quote)), true
		}

	case string(domain.OriginDerived):
		note := strings.TrimSpace(v.Note)
		switch {
		case v.SourceID == "":
			c.reject(field, "вывод без источника", v)
		case c.bodies[v.SourceID] == "" && !c.known(v.SourceID):
			c.reject(field, "ссылка на неизвестный источник", v)
		case note == "":
			// Вывод без объяснения нечем проверить: читатель среза видит
			// утверждение и не может решить, согласен ли он с ходом мысли.
			c.reject(field, "вывод без объяснения", v)
		case text == "":
			c.reject(field, "значение пустое", v)
		default:
			return domain.Derived(text, v.SourceID, note), true
		}

	case string(domain.OriginMissing):
		note := strings.TrimSpace(v.Note)
		if note == "" {
			// «Данных нет» без вопроса — тупик: PM видит пробел и не знает, что
			// спросить, чтобы его закрыть.
			c.reject(field, "пробел без вопроса", v)
			break
		}
		return domain.Missing(note), true

	case string(domain.OriginComputed):
		// Арифметику модель не считает вообще. Проценты, суммы и разницы дат
		// считает Go: цифру, которую PM назовёт заказчику, нужно уметь повторить
		// и проверить, а не получать заново при каждом вызове модели.
		c.reject(field, "расчёт от модели не принимается", v)

	default:
		c.reject(field, "неизвестное происхождение "+quoteText(v.Origin), v)
	}

	return domain.Missing("модель дала значение, которое не прошло проверку"), false
}

// known отвечает, знаком ли источник. Отдельно от bodies, потому что источник с
// пустым текстом — это существующий источник, а не отсутствующий.
func (c *checker) known(id string) bool {
	_, ok := c.bodies[id]
	return ok
}

// date разбирает дату «ГГГГ-ММ-ДД». Пустая строка даёт нулевое время, и это
// значение, а не ошибка: «срок не назван» и «первое января первого года» —
// разные утверждения.
func (c *checker) date(field, s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		c.rejected = append(c.rejected, Rejection{
			Field: field, Reason: "дата не в виде ГГГГ-ММ-ДД: " + quoteText(s),
		})
		return time.Time{}, false
	}
	return t.UTC(), true
}

// normalize готовит текст к сравнению цитаты с источником.
//
// «Дословно» здесь означает «те же слова в том же порядке», а не «те же байты».
// Модель переносит строки иначе, чем чат, ставит неразрывные пробелы и заменяет
// кавычки на типографские — и на этом отвалилась бы каждая вторая честная
// цитата. А вот слова не меняются: подмена слова проверку не пройдёт.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	space := false
	for _, r := range s {
		switch r {
		case '«', '»', '„', '“', '”', '‟', '"':
			r = '"'
		case '‘', '’', '‚', '‛', '\'':
			r = '\''
		case '—', '–', '−', '‑', '‒':
			r = '-'
		case ' ', ' ', ' ':
			r = ' '
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func quoteText(s string) string { return "«" + s + "»" }

// convert переводит проверенный ответ модели в то, что ждёт сервис.
//
// Список отклонений возвращается вместе с результатом: вызывающий обязан его
// увидеть, а не гадать, почему срез вышел бедным.
func Convert(a Answer, in analyst.Input) (analyst.Output, []Rejection) {
	c := newChecker(in.Sources)
	out := analyst.Output{}

	out.Stage, _ = c.check("stage", a.Stage)
	out.GoalAsStated, _ = c.check("goal.asStated", a.GoalAsStated)
	out.GoalClarified, _ = c.check("goal.clarified", a.GoalClarified)

	out.OutOfScope = c.values("goal.outOfScope", a.OutOfScope)
	out.Done = c.values("status.done", a.Done)
	out.Left = c.values("status.left", a.Left)

	out.Criteria = criteria(a.Criteria)
	out.Milestones = c.milestones(a.Milestones)
	out.Shifts = c.shifts(a.Shifts)
	out.Blockers = c.blockers(a.Blockers)
	out.Risks = c.risks(a.Risks)
	out.Questions = c.questions(a.Questions)
	out.Artifacts = artifacts(a.Artifacts)
	out.PMActions = c.pmActions(a.PMActions)

	return out, c.rejected
}

// values проверяет список значений. Не устоявшее выбрасывается, а не
// превращается в пробел: пробел в списке «что сделано» — это строка «данных
// нет» посреди перечисления, которую читателю нечем объяснить.
func (c *checker) values(field string, list []Value) []domain.Value {
	var out []domain.Value
	for i, v := range list {
		if got, ok := c.check(fmt.Sprintf("%s[%d]", field, i), v); ok {
			out = append(out, got)
		}
	}
	return out
}

func criteria(list []criterion) []domain.Criterion {
	var out []domain.Criterion
	for i, it := range list {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		n := it.N
		if n <= 0 {
			n = i + 1
		}
		out = append(out, domain.Criterion{
			N: n, Text: text, Met: it.Met, Note: strings.TrimSpace(it.Note),
		})
	}
	return out
}

func (c *checker) milestones(list []milestone) []domain.Milestone {
	var out []domain.Milestone
	for i, it := range list {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			continue
		}
		field := fmt.Sprintf("milestones[%d]", i)
		due, ok := c.date(field+".due", it.Due)
		if !ok {
			continue
		}

		// Доля выполнения приводится к отрезку, а не отбрасывается: этап с
		// прогрессом 1.4 — это неаккуратность модели, а не выдумка, и терять
		// из-за неё сам этап было бы дороже.
		progress := it.Progress
		switch {
		case progress < 0:
			progress = 0
		case progress > 1:
			progress = 1
		}

		out = append(out, domain.Milestone{
			ID:       fmt.Sprintf("m%d", i+1),
			Title:    title,
			Due:      due,
			Progress: progress,
			Evidence: trimAll(it.Evidence),
		})
	}
	return out
}

func (c *checker) shifts(list []shift) []domain.DeadlineShift {
	var out []domain.DeadlineShift
	for i, it := range list {
		field := fmt.Sprintf("shifts[%d]", i)
		at, okAt := c.date(field+".at", it.At)
		from, okFrom := c.date(field+".from", it.From)
		to, okTo := c.date(field+".to", it.To)
		if !okAt || !okFrom || !okTo {
			continue
		}
		// Перенос без обеих дат ничего не сообщает: «срок двигали» без «с» и
		// «на» — это не наблюдение, а ощущение.
		if from.IsZero() && to.IsZero() {
			c.rejected = append(c.rejected, Rejection{
				Field: field, Reason: "перенос без дат «с» и «на»",
			})
			continue
		}
		out = append(out, domain.DeadlineShift{
			At: at, From: from, To: to, Comment: strings.TrimSpace(it.Comment),
		})
	}
	return out
}

func (c *checker) blockers(list []blocker) []domain.Blocker {
	var out []domain.Blocker
	for i, it := range list {
		field := fmt.Sprintf("blockers[%d]", i)
		summary := strings.TrimSpace(it.Summary)
		if summary == "" {
			continue
		}

		kind := domain.BlockerKind(strings.TrimSpace(it.Kind))
		if !validBlockerKind(kind) {
			// Вид блокера важнее описания: он определяет, к кому идти, чтобы
			// блокер снять. Подставить вместо незнакомого вида «технический»
			// значило бы отправить PM не к тому человеку.
			c.rejected = append(c.rejected, Rejection{
				Field: field + ".kind", Reason: "неизвестный вид блокера " + quoteText(it.Kind),
			})
			continue
		}

		since, ok := c.date(field+".since", it.Since)
		if !ok {
			continue
		}
		evidence, ok := c.check(field+".evidence", it.Evidence)
		if !ok {
			// Блокер без подтверждения — самое дорогое утверждение среза:
			// именно из-за него PM идёт к людям и двигает сроки.
			continue
		}

		out = append(out, domain.Blocker{
			ID:        fmt.Sprintf("b%d", i+1),
			Summary:   summary,
			Kind:      kind,
			DependsOn: strings.TrimSpace(it.DependsOn),
			Since:     since,
			Evidence:  evidence,
		})
	}
	return out
}

func (c *checker) risks(list []risk) []domain.Risk {
	var out []domain.Risk
	for i, it := range list {
		field := fmt.Sprintf("risks[%d]", i)
		summary := strings.TrimSpace(it.Summary)
		if summary == "" {
			continue
		}
		evidence, ok := c.check(field+".evidence", it.Evidence)
		if !ok {
			continue
		}
		days := it.DaysImpact
		if days < 0 {
			days = 0
		}
		out = append(out, domain.Risk{
			ID:         fmt.Sprintf("r%d", i+1),
			Summary:    summary,
			DaysImpact: days,
			Spread:     strings.TrimSpace(it.Spread),
			Evidence:   evidence,
		})
	}
	return out
}

func (c *checker) questions(list []question) []domain.Question {
	var out []domain.Question
	for i, it := range list {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		n := it.N
		if n <= 0 {
			n = i + 1
		}
		// Ответ проверяется, но вопрос без ответа не выбрасывается: вопрос без
		// ответа — это и есть пробел, ради которого раздел существует.
		Answer, _ := c.check(fmt.Sprintf("questions[%d].Answer", i), it.Answer)
		out = append(out, domain.Question{
			N: n, Text: text, Unlocks: strings.TrimSpace(it.Unlocks), Answer: Answer,
		})
	}
	return out
}

func artifacts(list []artifact) []domain.Artifact {
	var out []domain.Artifact
	for _, it := range list {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		bytes := it.Bytes
		if bytes < 0 {
			bytes = 0
		}
		out = append(out, domain.Artifact{
			Name:      name,
			Bytes:     bytes,
			Present:   it.Present,
			WouldGive: strings.TrimSpace(it.WouldGive),
		})
	}
	return out
}

func (c *checker) pmActions(list []pmAction) []domain.PMAction {
	var out []domain.PMAction
	for i, it := range list {
		text := strings.TrimSpace(it.Text)
		if text == "" {
			continue
		}
		kind := domain.PMActionKind(strings.TrimSpace(it.Kind))
		if !validActionKind(kind) {
			c.rejected = append(c.rejected, Rejection{
				Field:  fmt.Sprintf("pmActions[%d].kind", i),
				Reason: "неизвестный вид действия " + quoteText(it.Kind),
			})
			continue
		}
		out = append(out, domain.PMAction{
			Kind: kind, Text: text, Why: strings.TrimSpace(it.Why),
		})
	}
	return out
}

func validBlockerKind(k domain.BlockerKind) bool {
	switch k {
	case domain.BlockerNoInfo, domain.BlockerNoAccess, domain.BlockerDependency,
		domain.BlockerTechnical, domain.BlockerNoApproach, domain.BlockerNoTime:
		return true
	}
	return false
}

func validActionKind(k domain.PMActionKind) bool {
	switch k {
	case domain.ActionApprove, domain.ActionAccess, domain.ActionConnect,
		domain.ActionClarify, domain.ActionEscalate, domain.ActionNone:
		return true
	}
	return false
}

func trimAll(list []string) []string {
	var out []string
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Report пишет в лог, что из ответа не приняли.
//
// Отклонения не молчат по той же причине, по которой они вообще собираются: по
// ним видно, врёт модель редко или постоянно и на каких полях. Молчаливая
// фильтрация выглядела бы как безупречный разбор, который просто мало что нашёл.
//
// Здесь, а не в транспорте: транспорты разные, а вопрос «чему не поверили» —
// один и тот же, и отвечать на него по-разному в двух местах незачем.
func Report(log *slog.Logger, taskID string, rejected []Rejection) {
	if log == nil || len(rejected) == 0 {
		return
	}
	reasons := make([]string, 0, len(rejected))
	for _, r := range rejected {
		reasons = append(reasons, r.String())
	}
	log.Warn("часть ответа модели не принята",
		"задача", taskID, "отклонено", len(rejected),
		"причины", strings.Join(reasons, "; "))
}
