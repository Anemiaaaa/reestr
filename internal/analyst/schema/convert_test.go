package schema

import (
	"strings"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/domain"
)

// body — текст источника, на который ссылаются проверки. Взят похожим на
// настоящую переписку: с переносом строки посреди фразы и типографскими
// кавычками, потому что именно на этом ломается наивное сравнение цитат.
const body = `Заказчик прислал правки 8 июня.
Договорились перенести срок на 20 июня —
«без интеграции с 1С принимать не будем».
Доступ к тестовому контуру пока не дали.`

func input() analyst.Input {
	return analyst.Input{
		Task:    domain.Task{ID: "aura", Title: "Внедрение"},
		Sources: []domain.Source{{ID: "s-1", Body: body}},
		Now:     time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC),
	}
}

func quoted(text, quote string) Value {
	return Value{Text: text, Origin: "quoted", SourceID: "s-1", Quote: quote}
}

// find ищет отклонение по полю. Отклонения — часть результата, а не отладочный
// вывод: по ним видно, на чём именно модель не сошлась с источниками.
func find(t *testing.T, list []Rejection, field string) Rejection {
	t.Helper()

	for _, r := range list {
		if r.Field == field {
			return r
		}
	}
	t.Fatalf("отклонения по полю %q нет: %v", field, list)
	return Rejection{}
}

func TestQuoteMustBeInSource(t *testing.T) {
	t.Parallel()

	a := Answer{
		Stage:        quoted("в работе", "Доступ к тестовому контуру пока не дали"),
		GoalAsStated: quoted("интеграция с 1С", "принимать не будем без интеграции"),
	}

	out, rejected := Convert(a, input())

	// Цитата, которая действительно есть в источнике, проходит и сохраняет
	// ссылку: без неё значение нечем проверить.
	if out.Stage.Origin != domain.OriginQuoted || out.Stage.Text != "в работе" {
		t.Errorf("этап не принят: %+v", out.Stage)
	}
	if out.Stage.SourceID != "s-1" {
		t.Errorf("ссылка на источник потерялась: %+v", out.Stage)
	}

	// А переставленные слова — уже не цитата. Звучит она так же убедительно,
	// как настоящая, и отличить её от правды иначе нечем.
	if out.GoalAsStated.Origin != domain.OriginMissing {
		t.Errorf("выдуманная цитата принята: %+v", out.GoalAsStated)
	}
	r := find(t, rejected, "goal.asStated")
	if !strings.Contains(r.Reason, "дословно") {
		t.Errorf("причина отклонения: %q", r.Reason)
	}
	if r.Quote == "" || r.SourceID != "s-1" {
		t.Errorf("отклонение не называет, что именно не сошлось: %+v", r)
	}
}

// TestQuoteNormalization: «дословно» — это те же слова в том же порядке, а не
// те же байты. Модель переносит строки иначе, чем чат, и ставит типографские
// кавычки; на побайтовом сравнении отвалилась бы каждая вторая честная цитата.
func TestQuoteNormalization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		quote string
		want  bool
	}{
		{"перенос строки заменён пробелом", "Договорились перенести срок на 20 июня", true},
		{"цитата через перенос строки", "перенести срок на 20 июня — «без интеграции", true},
		{"кавычки распрямлены", `"без интеграции с 1С принимать не будем"`, true},
		{"тире заменено дефисом", "на 20 июня - «без интеграции", true},
		{"лишние пробелы", "Договорились   перенести    срок", true},
		{"неразрывный пробел", "Договорились перенести срок", true},
		{"подменено слово", "Договорились перенести срок на 30 июня", false},
		{"подменено число", "правки 9 июня", false},
		{"выдумано целиком", "заказчик всем доволен", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, _ := Convert(Answer{Stage: quoted("в работе", tc.quote)}, input())
			got := out.Stage.Origin == domain.OriginQuoted
			if got != tc.want {
				t.Errorf("цитата %q принята=%v, хотели %v", tc.quote, got, tc.want)
			}
		})
	}
}

// TestComputedRejected: арифметику модель не считает вообще. Проценты, суммы и
// разницы дат считает Go — цифру, которую PM назовёт заказчику, нужно уметь
// повторить и проверить, а не получать заново при каждом вызове модели.
func TestComputedRejected(t *testing.T) {
	t.Parallel()

	a := Answer{Stage: Value{Text: "готово на 60%", Origin: "computed", Note: "3 из 5 этапов"}}

	out, rejected := Convert(a, input())
	if out.Stage.Origin != domain.OriginMissing {
		t.Errorf("расчёт от модели принят: %+v", out.Stage)
	}
	if r := find(t, rejected, "stage"); !strings.Contains(r.Reason, "расчёт") {
		t.Errorf("причина отклонения: %q", r.Reason)
	}
}

func TestValueRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		v      Value
		want   domain.Origin
		reason string
	}{
		{
			name:   "цитата без источника",
			v:      Value{Text: "в работе", Origin: "quoted", Quote: "правки 8 июня"},
			want:   domain.OriginMissing,
			reason: "без источника",
		},
		{
			name:   "ссылка на чужой источник",
			v:      Value{Text: "в работе", Origin: "quoted", SourceID: "s-99", Quote: "правки 8 июня"},
			want:   domain.OriginMissing,
			reason: "неизвестный источник",
		},
		{
			name:   "происхождение «цитата», а цитаты нет",
			v:      Value{Text: "в работе", Origin: "quoted", SourceID: "s-1"},
			want:   domain.OriginMissing,
			reason: "цитаты нет",
		},
		{
			name:   "вывод без объяснения",
			v:      Value{Text: "в работе", Origin: "derived", SourceID: "s-1"},
			want:   domain.OriginMissing,
			reason: "без объяснения",
		},
		{
			name: "вывод с объяснением проходит",
			v: Value{
				Text: "в работе", Origin: "derived", SourceID: "s-1",
				Note: "о доступе спрашивают, значит работа идёт",
			},
			want: domain.OriginDerived,
		},
		{
			name:   "пробел без вопроса",
			v:      Value{Origin: "missing"},
			want:   domain.OriginMissing,
			reason: "без вопроса",
		},
		{
			name: "пробел с вопросом проходит",
			v:    Value{Origin: "missing", Note: "спросить у постановщика дату"},
			want: domain.OriginMissing,
		},
		{
			name:   "неизвестное происхождение",
			v:      Value{Text: "в работе", Origin: "guessed"},
			want:   domain.OriginMissing,
			reason: "неизвестное происхождение",
		},
		{
			name:   "цитата с пустым значением",
			v:      Value{Origin: "quoted", SourceID: "s-1", Quote: "правки 8 июня"},
			want:   domain.OriginMissing,
			reason: "пустое",
		},
		{
			// Пропуск поля и выдумка лечатся по-разному: первое — недоработка
			// схемы или промпта, второе — попытка выдать желаемое за источник.
			// Сообщение обязано их различать.
			name:   "поля нет в ответе вовсе",
			v:      Value{},
			want:   domain.OriginMissing,
			reason: "не заполнено",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, rejected := Convert(Answer{Stage: tc.v}, input())
			if out.Stage.Origin != tc.want {
				t.Errorf("происхождение = %q, хотели %q", out.Stage.Origin, tc.want)
			}

			// Смотрим только на поле «этап»: остальные поля ответа здесь пусты,
			// и отклонения по ним ожидаемы — их проверяет отдельный случай.
			var mine []Rejection
			for _, r := range rejected {
				if r.Field == "stage" {
					mine = append(mine, r)
				}
			}
			if tc.reason == "" {
				if len(mine) != 0 {
					t.Errorf("годное значение отклонено: %v", mine)
				}
				return
			}
			if r := find(t, mine, "stage"); !strings.Contains(r.Reason, tc.reason) {
				t.Errorf("причина %q не содержит %q", r.Reason, tc.reason)
			}
		})
	}
}

// TestListsDropUnproven: в списке негодное значение выбрасывается, а не
// становится пробелом. Строка «данных нет» посреди перечисления «что сделано»
// читателю нечем объяснить.
func TestListsDropUnproven(t *testing.T) {
	t.Parallel()

	a := Answer{
		Done: []Value{
			quoted("правки приняты", "Заказчик прислал правки 8 июня"),
			quoted("всё согласовано", "заказчик всем доволен"),
		},
	}

	out, rejected := Convert(a, input())
	if len(out.Done) != 1 {
		t.Fatalf("в «сделано» %d значений, хотели 1: %+v", len(out.Done), out.Done)
	}
	if out.Done[0].Text != "правки приняты" {
		t.Errorf("осталось не то значение: %+v", out.Done[0])
	}
	find(t, rejected, "status.done[1]")
}

func TestBlockers(t *testing.T) {
	t.Parallel()

	a := Answer{Blockers: []blocker{
		{
			Summary: "нет доступа к тестовому контуру",
			Kind:    "no_access",
			Since:   "2026-06-08",
			Evidence: quoted("доступ не дали",
				"Доступ к тестовому контуру пока не дали"),
		},
		{
			Summary:  "заказчик недоволен",
			Kind:     "не нравится",
			Evidence: quoted("недоволен", "Заказчик прислал правки 8 июня"),
		},
		{
			Summary:  "что-то мешает",
			Kind:     "technical",
			Evidence: quoted("мешает", "разработчик заболел"),
		},
	}}

	out, rejected := Convert(a, input())
	if len(out.Blockers) != 1 {
		t.Fatalf("блокеров %d, хотели 1: %+v", len(out.Blockers), out.Blockers)
	}

	b := out.Blockers[0]
	if b.Kind != domain.BlockerNoAccess {
		t.Errorf("вид блокера = %q", b.Kind)
	}
	if !b.Since.Equal(time.Date(2026, time.June, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("дата блокера = %v", b.Since)
	}

	// Незнакомый вид не подменяется знакомым: вид определяет, к кому идти,
	// чтобы блокер снять, и «технический» вместо «нет доступа» отправил бы PM
	// не к тому человеку.
	if r := find(t, rejected, "blockers[1].kind"); !strings.Contains(r.Reason, "неизвестный вид") {
		t.Errorf("причина: %q", r.Reason)
	}
	// Блокер без подтверждения — самое дорогое утверждение среза: из-за него PM
	// идёт к людям и двигает сроки.
	find(t, rejected, "blockers[2].evidence")
}

func TestShifts(t *testing.T) {
	t.Parallel()

	a := Answer{Shifts: []shift{
		{At: "2026-06-08", From: "2026-06-08", To: "2026-06-20", Comment: "правки заказчика"},
		{At: "2026-06-08"},
		{At: "8 июня 2026", From: "2026-06-08", To: "2026-06-20"},
	}}

	out, rejected := Convert(a, input())
	if len(out.Shifts) != 1 {
		t.Fatalf("переносов %d, хотели 1: %+v", len(out.Shifts), out.Shifts)
	}
	if !out.Shifts[0].To.Equal(time.Date(2026, time.June, 20, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("дата «на» = %v", out.Shifts[0].To)
	}

	// «Срок двигали» без «с» и «на» — не наблюдение, а ощущение.
	find(t, rejected, "shifts[1]")
	// Дата словами не принимается: разбирать «8 июня» пришлось бы догадками, а
	// догадка о дате в срезе — это выдуманное число в отчёте заказчику.
	find(t, rejected, "shifts[2].at")
}

func TestMilestones(t *testing.T) {
	t.Parallel()

	a := Answer{Milestones: []milestone{
		{Title: "Сбор требований", Due: "2026-06-08", Progress: 1},
		{Title: "Интеграция", Progress: 1.4},
		{Title: "Приёмка", Due: "июнь"},
		{Title: "   "},
	}}

	out, rejected := Convert(a, input())
	if len(out.Milestones) != 2 {
		t.Fatalf("этапов %d, хотели 2: %+v", len(out.Milestones), out.Milestones)
	}

	// Незаполненный срок остаётся нулевым: «срок этапа не назван» и «первое
	// января первого года» — разные утверждения.
	if !out.Milestones[1].Due.IsZero() {
		t.Errorf("пустой срок заполнился: %v", out.Milestones[1].Due)
	}
	// Доля приводится к отрезку, а сам этап не теряется: 1.4 — неаккуратность
	// модели, а не выдумка.
	if out.Milestones[1].Progress != 1 {
		t.Errorf("доля выполнения = %v, хотели 1", out.Milestones[1].Progress)
	}
	find(t, rejected, "milestones[2].due")
}

func TestPMActions(t *testing.T) {
	t.Parallel()

	a := Answer{PMActions: []pmAction{
		{Kind: "access", Text: "достать доступ к тестовому контуру", Why: "снимет блокер"},
		{Kind: "разобраться", Text: "разобраться с задачей"},
	}}

	out, rejected := Convert(a, input())
	if len(out.PMActions) != 1 {
		t.Fatalf("действий %d, хотели 1: %+v", len(out.PMActions), out.PMActions)
	}
	if out.PMActions[0].Kind != domain.ActionAccess {
		t.Errorf("вид действия = %q", out.PMActions[0].Kind)
	}
	// Список видов закрытый намеренно: «разобраться с задачей» — не действие, а
	// его отсутствие.
	find(t, rejected, "pmActions[1].kind")
}

// TestQuestionsKeepUnanswered: вопрос без ответа не выбрасывается. Вопрос без
// ответа — это и есть пробел, ради которого раздел существует.
func TestQuestionsKeepUnanswered(t *testing.T) {
	t.Parallel()

	a := Answer{Questions: []question{
		{N: 1, Text: "Когда дадут доступ?", Answer: Value{Origin: "missing", Note: "спросить у заказчика"}},
		{N: 2, Text: "Кто принимает работу?", Answer: quoted("заказчик", "выдумка")},
	}}

	out, rejected := Convert(a, input())
	if len(out.Questions) != 2 {
		t.Fatalf("вопросов %d, хотели 2", len(out.Questions))
	}
	if out.Questions[0].Answered() {
		t.Error("вопрос без ответа посчитан отвеченным")
	}
	// Ответ с выдуманной цитатой не превращает вопрос в отвеченный: иначе
	// пробел исчез бы из среза, а PM решил бы, что спрашивать нечего.
	if out.Questions[1].Answered() {
		t.Errorf("выдуманный ответ принят: %+v", out.Questions[1].Answer)
	}
	find(t, rejected, "questions[1].Answer")
}

// TestCleanAnswerHasNoRejections: годный ответ не должен давать отклонений.
// Без этой проверки строгость легко довести до того, что не проходит ничего.
func TestCleanAnswerHasNoRejections(t *testing.T) {
	t.Parallel()

	a := Answer{
		Stage:        quoted("в работе", "Доступ к тестовому контуру пока не дали"),
		GoalAsStated: quoted("интеграция с 1С", "без интеграции с 1С принимать не будем"),
		GoalClarified: Value{
			Text: "принять можно только с интеграцией", Origin: "derived", SourceID: "s-1",
			Note: "из условия приёмки",
		},
		Done: []Value{quoted("правки собраны", "Заказчик прислал правки 8 июня")},
		Left: []Value{{Origin: "missing", Note: "спросить, что осталось после правок"}},
		Criteria: []criterion{
			{N: 1, Text: "интеграция с 1С работает", Met: false, Note: "доступа нет"},
		},
		Artifacts: []artifact{{Name: "ТЗ.docx", Present: false, WouldGive: "объём работ"}},
		Shifts: []shift{
			{At: "2026-06-08", From: "2026-06-08", To: "2026-06-20", Comment: "правки"},
		},
	}

	out, rejected := Convert(a, input())
	if len(rejected) != 0 {
		t.Fatalf("годный ответ отклонён: %v", rejected)
	}
	switch {
	case out.Stage.Origin != domain.OriginQuoted:
		t.Errorf("этап: %+v", out.Stage)
	case out.GoalClarified.Origin != domain.OriginDerived:
		t.Errorf("уточнённая цель: %+v", out.GoalClarified)
	case len(out.Done) != 1 || len(out.Left) != 1:
		t.Errorf("сделано %d, осталось %d", len(out.Done), len(out.Left))
	case len(out.Criteria) != 1 || out.Criteria[0].N != 1:
		t.Errorf("критерии: %+v", out.Criteria)
	case len(out.Artifacts) != 1 || out.Artifacts[0].Present:
		t.Errorf("артефакты: %+v", out.Artifacts)
	case len(out.Shifts) != 1:
		t.Errorf("переносы: %+v", out.Shifts)
	}
}
