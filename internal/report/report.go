// Package report превращает срез в текст, который можно вынести из реестра.
//
// Пакет существует потому, что срез делают, чтобы показать: заказчику, на
// планёрке, в переписке. До сих пор он жил только во вкладке браузера — ни
// скопировать, ни приложить к письму.
//
// Текст собирается здесь, а не в браузере, по тому же правилу, по которому там
// не считаются числа: у одного среза должно быть одно изложение. Собранное по
// месту, оно разошлось бы с экраном на первой же правке разметки.
//
// Пометки происхождения из текста не убираются. Ради них реестр и заведён: без
// них выгрузка становится обычным отчётом, который нечем проверить, — а весь
// смысл в том, что каждую строку можно возвести к источнику.
package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
)

// Markdown отдаёт срез разметкой Markdown.
//
// Markdown, а не голый текст: он читается и без обработки — в чате Bitrix,
// в письме, в блокноте, — но при этом сохраняет заголовки и списки, если его
// вставят туда, где разметку понимают.
func Markdown(sl domain.Slice, t domain.Task, srcs []domain.Source, now time.Time) string {
	w := &writer{srcs: index(srcs)}

	w.line("# " + text(sl.Passport.Title, t.Title))
	if t.Project != "" {
		w.line("_" + t.Project + "_")
	}
	w.blank()

	w.head("Где мы сейчас")
	w.pair("Этап", sl.Status.Stage)
	w.pair("Готовность", sl.Status.Readiness)
	if !t.Deadline.IsZero() {
		// Срок и остаток дней — из задачи, а не из среза: в срезе он значение с
		// происхождением, а здесь нужна ещё и дистанция до него.
		w.line(fmt.Sprintf("- **Срок:** %s (%s)", domain.FormatDate(t.Deadline), left(t, now)))
	}
	if t.Budget != 0 {
		w.line("- **Бюджет:** " + ru.Money(t.Budget))
	}
	if n := sl.Gaps(); n > 0 {
		w.line("- **Пробелов в срезе:** " + fmt.Sprint(n))
	}
	w.blank()

	w.head("Паспорт задачи")
	w.pair("Автор постановки", sl.Passport.Author)
	w.pair("Исполнитель", sl.Passport.Assignee)
	w.pair("Поставлена", sl.Passport.OpenedAt)
	w.pair("Срок по срезу", sl.Passport.Deadline)
	w.blank()

	if len(sl.Passport.Shifts) > 0 {
		w.sub("Переносы срока")
		for _, s := range sl.Passport.Shifts {
			moved := strings.TrimSpace(domain.FormatDate(s.From) + " → " + domain.FormatDate(s.To))
			note := s.Comment
			if !s.Explained() {
				note = "без объяснения"
			}
			w.line("- " + moved + " — " + note)
		}
		w.blank()
	}

	w.head("Цель и границы")
	w.para("Как поставлено", sl.Goal.AsStated)
	w.para("Что имелось в виду", sl.Goal.Clarified)

	if len(sl.Goal.Criteria) > 0 {
		w.sub(fmt.Sprintf("Критерии приёмки (%d из %d)",
			domain.MetCriteria(sl.Goal.Criteria), len(sl.Goal.Criteria)))
		for _, c := range sl.Goal.Criteria {
			w.line(fmt.Sprintf("- [%s] %d. %s", box(c.Met), c.N, c.Text))
			if c.Note != "" {
				w.line("      " + c.Note)
			}
		}
		w.blank()
	}
	w.values("Вне задачи", sl.Goal.OutOfScope)

	w.head("Статус и план")
	if len(sl.Status.Milestones) > 0 {
		for _, m := range sl.Status.Milestones {
			row := fmt.Sprintf("- %s — %s", m.Title, ru.Percent(m.Progress))
			if !m.Due.IsZero() {
				row += ", срок " + domain.FormatDate(m.Due)
			}
			if d := m.OverdueDays(now); d > 0 {
				row += ", просрочен на " + ru.Days(d)
			}
			w.line(row)
		}
		w.blank()
	}
	w.values("Сделано", sl.Status.Done)
	w.values("Осталось", sl.Status.Left)

	if len(sl.Blockers) > 0 {
		w.head("Блокеры")
		for _, b := range sl.Blockers {
			row := "- **" + b.Summary + "** — " + b.Kind.Label()
			if d := b.AgeDays(now); d > 0 {
				row += ", висит " + ru.Days(d)
			}
			w.line(row)
			if b.DependsOn != "" {
				w.line("      ждёт: " + b.DependsOn)
			}
			w.evidence(b.Evidence)
		}
		w.blank()
	}

	if len(sl.Risks) > 0 {
		w.head("Риски")
		for _, r := range sl.Risks {
			row := "- **" + r.Summary + "**"
			if r.DaysImpact > 0 {
				row += " — срок +" + ru.Days(r.DaysImpact)
			}
			if r.Spread != "" {
				row += ", " + r.Spread
			}
			w.line(row)
			w.evidence(r.Evidence)
		}
		w.blank()
	}

	if len(sl.PMActions.Needed) > 0 || sl.PMActions.NextCheck.Known() {
		w.head("Что делать PM")
		for _, a := range sl.PMActions.Needed {
			w.line("- **" + a.Kind.Label() + ":** " + a.Text)
			if a.Why != "" {
				w.line("      " + a.Why)
			}
		}
		w.pair("Следующая проверка", sl.PMActions.NextCheck)
		if sl.PMActions.Comment != "" {
			w.line("")
			w.line("> " + sl.PMActions.Comment)
		}
		w.blank()
	}

	if len(sl.Questions) > 0 {
		w.head(fmt.Sprintf("Вопросы специалисту (без ответа: %d)", len(sl.OpenQuestions())))
		for _, q := range sl.Questions {
			w.line(fmt.Sprintf("- [%s] %d. %s", box(q.Answered()), q.N, q.Text))
			if q.Answered() {
				w.line("      ответ: " + w.value(q.Answer))
			} else if q.Unlocks != "" {
				w.line("      разблокирует: " + q.Unlocks)
			}
		}
		w.blank()
	}

	if len(sl.Artifacts) > 0 {
		w.head("Файлы")
		for _, a := range sl.Artifacts {
			row := fmt.Sprintf("- [%s] %s", box(a.Present), a.Name)
			if !a.Present && a.WouldGive != "" {
				row += " — дал бы: " + a.WouldGive
			}
			w.line(row)
		}
		w.blank()
	}

	w.head("Откуда это")
	w.line(fmt.Sprintf("Срез v%d, собран %s.", sl.Version, domain.FormatDate(sl.BuiltAt)))
	w.line("Разбор: " + sl.Analyst + ".")
	if sl.EditedBy != "" {
		w.line("Правка: " + sl.EditedBy + ".")
	}
	if len(sl.SourceIDs) > 0 {
		w.line("")
		w.line("Источники:")
		for _, id := range sl.SourceIDs {
			if src, ok := w.srcs[id]; ok {
				w.line("- " + src.Title + " (" + src.Kind.Label() + ", " +
					domain.FormatDate(src.UploadedAt) + ")")
			}
		}
	}
	w.line("")
	w.line("Пометки: ↩ из источника · → выведено · ∑ посчитано · ✎ сказал руководитель · ? нужно спросить")

	return strings.TrimRight(w.b.String(), "\n") + "\n"
}

// writer собирает текст по строкам и держит справочник источников.
type writer struct {
	b    strings.Builder
	srcs map[string]domain.Source
}

func index(list []domain.Source) map[string]domain.Source {
	m := make(map[string]domain.Source, len(list))
	for _, s := range list {
		m[s.ID] = s
	}
	return m
}

func (w *writer) line(s string) { w.b.WriteString(s + "\n") }

// blank ставит пустую строку, но не две подряд: в Markdown лишний отступ
// превращается в разрыв абзаца там, где его не задумывали.
func (w *writer) blank() {
	if s := w.b.String(); strings.HasSuffix(s, "\n\n") {
		return
	}
	w.b.WriteString("\n")
}

func (w *writer) head(title string) {
	w.line("## " + title)
	w.blank()
}

func (w *writer) sub(title string) {
	w.line("**" + title + "**")
	w.blank()
}

// value — значение вместе с пометкой происхождения и опорой.
//
// Опора идёт в скобках рядом, а не сноской: выгрузку читают в один проход, и
// сноска внизу означала бы, что до неё не дойдут.
func (w *writer) value(v domain.Value) string {
	out := v.Text
	if !v.Known() {
		out = "нет данных"
	}
	out += " " + Mark(v.Origin)

	var why []string
	if title := w.title(v.SourceID); title != "" {
		why = append(why, title)
	}
	if v.Quote != "" {
		why = append(why, "«"+v.Quote+"»")
	}
	if v.Note != "" {
		why = append(why, v.Note)
	}
	if len(why) > 0 {
		out += " (" + strings.Join(why, "; ") + ")"
	}
	return out
}

func (w *writer) title(id string) string {
	if src, ok := w.srcs[id]; ok {
		return src.Title
	}
	return ""
}

func (w *writer) pair(label string, v domain.Value) {
	w.line("- **" + label + ":** " + w.value(v))
}

// para — значение прозой: цель читают целиком, и списком она читается хуже.
func (w *writer) para(label string, v domain.Value) {
	w.line("**" + label + "**")
	w.line("")
	w.line(w.value(v))
	w.blank()
}

func (w *writer) values(title string, vs []domain.Value) {
	if len(vs) == 0 {
		return
	}
	w.sub(fmt.Sprintf("%s (%d)", title, len(vs)))
	for _, v := range vs {
		w.line("- " + w.value(v))
	}
	w.blank()
}

// evidence — основание карточки отдельной строкой с отступом. Пустое опускаем:
// строка «нет данных» под каждым блокером ничего не добавляет.
func (w *writer) evidence(v domain.Value) {
	if !v.Known() {
		return
	}
	w.line("      " + w.value(v))
}

// box — отметка выполнения в списке Markdown.
func box(done bool) string {
	if done {
		return "x"
	}
	return " "
}

// text отдаёт значение или запасную строку, если значения нет.
func text(v domain.Value, fallback string) string {
	if v.Known() {
		return v.Text
	}
	return fallback
}

// left — сколько осталось до срока, словами.
func left(t domain.Task, now time.Time) string {
	switch d := t.DaysLeft(now); {
	case d < 0:
		return "просрочено на " + ru.Days(-d)
	case d == 0:
		return "срок сегодня"
	default:
		return "осталось " + ru.Days(d)
	}
}

// Mark — знак происхождения. Тот же, что и на экране: человек, сверяющий
// выгрузку с реестром, не должен разбираться, почему пометки разные.
func Mark(o domain.Origin) string {
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
