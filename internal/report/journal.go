package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
)

// Journal превращает журнал инцидентов в текст для разговора об оценке.
//
// Разговор идёт по человеку, поэтому и текст сгруппирован по людям, а не по
// дням: в журнале записи лежат в порядке событий, а руководитель садится
// говорить с одним специалистом за месяц.
//
// Оценки в процентах здесь нет и не будет. Реестр хранит случаи и считает то,
// что можно посчитать; меру ставит человек — за это ему и платят. Текст, в
// котором стояла бы цифра «75 %», читался бы как решение реестра, а решение не
// его.
func Journal(list []domain.Incident, titles map[string]string, period string) string {
	w := &writer{}

	w.line("# Журнал инцидентов")
	if period != "" {
		w.line("_" + period + "_")
	}
	w.blank()

	if len(list) == 0 {
		w.line("За этот отрезок записей нет.")
		return w.b.String()
	}

	counted := 0
	for _, in := range list {
		if in.Countable() {
			counted++
		}
	}
	// «В оценку идёт: N» вместо «идут N»: согласовать глагол с любым числом
	// нечем, а «идут 1» в документе, который показывают человеку, читается как
	// небрежность — и вместе с ней небрежным кажется весь счёт.
	w.line(fmt.Sprintf("Всего %s, в оценку идёт: %d.",
		ru.Count(len(list), "случай", "случая", "случаев"), counted))
	w.blank()

	// Сначала свод по блокам: он отвечает на вопрос «где болит», с которого
	// разговор и начинают.
	w.head("По блокам KPI")
	for _, b := range domain.KPIBlocks() {
		var all, out int
		for _, in := range list {
			if in.Block != b {
				continue
			}
			all++
			if !in.Countable() {
				out++
			}
		}
		row := fmt.Sprintf("- **%s** (вес %.0f%%): ", b.Label(), b.Weight()*100)
		if all == 0 {
			w.line(row + "претензий нет")
			continue
		}
		row += ru.Count(all, "случай", "случая", "случаев")
		if out > 0 {
			row += fmt.Sprintf(", из них %d вне зоны контроля", out)
		}
		w.line(row)
	}
	w.blank()

	for _, name := range people(list) {
		w.head(name)

		var mine []domain.Incident
		for _, in := range list {
			if in.Employee == name {
				mine = append(mine, in)
			}
		}

		var out, esc int
		for _, in := range mine {
			if !in.Countable() {
				out++
			}
			if in.Escalated {
				esc++
			}
		}
		row := fmt.Sprintf("%s, в оценку идёт: %d",
			ru.Count(len(mine), "случай", "случая", "случаев"), len(mine)-out)
		if esc > 0 {
			// Эскалации называем сразу под счётом: блок «эскалация и
			// самостоятельность» штрафует не проблему, а молчание о ней, и
			// вовремя названная помеха — довод в пользу специалиста.
			row += fmt.Sprintf(", сообщил сам — %d", esc)
		}
		w.line(row + ".")
		w.blank()

		for _, in := range mine {
			w.line(fmt.Sprintf("**%s — %s** (%s)",
				domain.FormatDate(in.At), in.Project, in.Block.Label()))
			if title := titles[in.TaskID]; title != "" {
				w.line("Задача реестра: " + title)
			}
			w.line("")
			// Текст руководителя переносится как есть: он делит запись на факты
			// и последствия, и склеить это в абзац значит потерять его работу.
			for _, l := range strings.Split(in.Text, "\n") {
				w.line(l)
			}
			w.line("")

			w.line("В зоне контроля: " + yes(!in.External) +
				" · эскалация: " + escalation(in))
			if in.ManagerNote != "" {
				w.line("Решение: " + in.ManagerNote)
			}
			if in.RecordedBy != "" {
				w.line("Зафиксировал: " + in.RecordedBy + ", " + domain.FormatDate(in.CreatedAt))
			}
			w.blank()
		}
	}

	w.line("---")
	w.line("Оценку в процентах ставит руководитель. Реестр хранит случаи и считает счёт:")
	w.line("без зафиксированного случая KPI не снижается.")
	return strings.TrimRight(w.b.String(), "\n") + "\n"
}

// people перечисляет сотрудников журнала, нагруженных первыми.
func people(list []domain.Incident) []string {
	counted := map[string]int{}
	for _, in := range list {
		if _, ok := counted[in.Employee]; !ok {
			counted[in.Employee] = 0
		}
		if in.Countable() {
			counted[in.Employee]++
		}
	}

	out := make([]string, 0, len(counted))
	for name := range counted {
		out = append(out, name)
	}
	// При равном счёте — по имени: иначе порядок брался бы из обхода карты и
	// менялся от выгрузки к выгрузке.
	sort.Slice(out, func(i, k int) bool {
		if counted[out[i]] != counted[out[k]] {
			return counted[out[i]] > counted[out[k]]
		}
		return out[i] < out[k]
	})
	return out
}

func yes(v bool) string {
	if v {
		return "да"
	}
	return "нет"
}

func escalation(in domain.Incident) string {
	if !in.Escalated {
		return "нет"
	}
	if d := domain.FormatDate(in.EscalatedAt); d != "" {
		return "да, " + d
	}
	return "да"
}
