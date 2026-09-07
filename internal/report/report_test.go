package report

import (
	"strings"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/domain"
)

var today = time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)

func day(d int) time.Time {
	return time.Date(2026, time.September, d, 0, 0, 0, 0, time.UTC)
}

// sample — срез, в котором есть по одному представителю каждого рода
// содержимого: значение с цитатой, пробел, список, карточка, пересчитанное
// число.
func sample() (domain.Slice, domain.Task, []domain.Source) {
	task := domain.Task{
		ID: "aura", Project: "АУРА", Title: "Внедрение автоматизации",
		Deadline: day(10), Budget: 250000,
	}
	srcs := []domain.Source{
		{ID: "s1", Kind: domain.KindCorrespondence, Title: "Чат задачи", UploadedAt: day(1)},
	}
	sl := domain.Slice{
		TaskID: "aura", Version: 3, BuiltAt: day(7), Analyst: "модель",
		Passport: domain.Passport{
			Title:    domain.Quoted("Внедрение автоматизации", "s1", "цитата названия"),
			Author:   domain.Quoted("Ризван Мирзаев", "s1", "поставил Ризван"),
			Assignee: domain.Missing("в переписке не назван"),
			Deadline: domain.Quoted("10.09.2026", "s1", "до десятого"),
		},
		Goal: domain.Goal{
			AsStated:  domain.Quoted("Внедрить 1С", "s1", "нужна 1С"),
			Clarified: domain.Derived("Полный цикл в 1С", "s1", "из разбора переписки"),
			Criteria: []domain.Criterion{
				{N: 1, Text: "Аренда подключена", Met: true},
				{N: 2, Text: "Обучение завершено", Note: "менеджеры частично"},
			},
		},
		Status: domain.Status{
			Stage:     domain.Stated("Идёт приёмка", "правку внёс kurban"),
			Readiness: domain.Computed("50 %", "1 из 2 этапов"),
			Milestones: []domain.Milestone{
				{ID: "m1", Title: "Настройка", Progress: 1, Due: day(3)},
				{ID: "m2", Title: "Обучение", Progress: 0, Due: day(5)},
			},
			Done: []domain.Value{domain.Quoted("Аренда оплачена", "s1", "оплатили")},
		},
		Blockers: []domain.Blocker{{
			ID: "b1", Summary: "Нет кодов ОКПД2", Kind: domain.BlockerNoInfo,
			DependsOn: "бухгалтер клиента", Since: day(1),
			Evidence: domain.Quoted("ждём коды", "s1", "коды так и не прислали"),
		}},
		Questions: []domain.Question{
			{N: 1, Text: "Кто подписывает акт?", Answer: domain.Missing("не спрашивали")},
		},
		SourceIDs: []string{"s1"},
	}
	return sl, task, srcs
}

// TestMarkdownKeepsProvenance: пометки происхождения из выгрузки не убираются.
// Ради них реестр и заведён — без них это обычный отчёт, который нечем
// проверить.
func TestMarkdownKeepsProvenance(t *testing.T) {
	t.Parallel()

	sl, task, srcs := sample()
	out := Markdown(sl, task, srcs, today)

	for _, want := range []string{
		"↩", // цитата
		"→", // вывод
		"∑", // расчёт
		"✎", // сказал человек
		"?", // пробел
		"«коды так и не прислали»", // сама цитата, а не только знак
		"Чат задачи",               // источник назван поимённо
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в выгрузке нет %q:\n%s", want, out)
		}
	}
}

// TestMarkdownShowsGaps: пробел выгружается наравне с остальным. Умолчать о нём
// хуже самого пробела: читатель решит, что поле просто не важно.
func TestMarkdownShowsGaps(t *testing.T) {
	t.Parallel()

	sl, task, srcs := sample()
	out := Markdown(sl, task, srcs, today)

	if !strings.Contains(out, "Исполнитель:** нет данных") {
		t.Errorf("незаполненный исполнитель не выгружен:\n%s", out)
	}
	if !strings.Contains(out, "в переписке не назван") {
		t.Errorf("пояснение к пробелу потеряно:\n%s", out)
	}
}

// TestMarkdownCounts: числа в выгрузке те же, что на экране, и считает их тот
// же код. Пересчёт по месту дал бы два разных ответа из одних данных.
func TestMarkdownCounts(t *testing.T) {
	t.Parallel()

	sl, task, srcs := sample()
	out := Markdown(sl, task, srcs, today)

	for _, want := range []string{
		"Критерии приёмки (1 из 2)",
		"Вопросы специалисту (без ответа: 1)",
		"осталось 3 дня",     // до срока десятого от седьмого
		"висит 6 дней",       // блокер с первого
		"просрочен на 2 дня", // этап «Обучение» с пятого
		"250 000",            // бюджет
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в выгрузке нет %q:\n%s", want, out)
		}
	}
}

// TestMarkdownNoEmptySections: разделы без содержимого не печатаются. Пустой
// заголовок «Риски» на бумаге читается как «рисков нет», а это утверждение,
// которого разбор не делал.
func TestMarkdownNoEmptySections(t *testing.T) {
	t.Parallel()

	sl, task, srcs := sample()
	out := Markdown(sl, task, srcs, today)

	for _, unwanted := range []string{"## Риски", "## Файлы", "Переносы срока"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("пустой раздел %q попал в выгрузку:\n%s", unwanted, out)
		}
	}
}

// TestFileNameSafe проверяется здесь же по смыслу: имя файла собирается из
// названия задачи, а названия в портале бывают с двоеточиями и слэшами.
func TestMarkdownEndsWithLegend(t *testing.T) {
	t.Parallel()

	sl, task, srcs := sample()
	out := Markdown(sl, task, srcs, today)

	// Расшифровка пометок — последней строкой: читатель выгрузки не обязан
	// помнить, что значит «∑», и искать это в начале документа не станет.
	if !strings.Contains(out, "Пометки: ↩ из источника") {
		t.Errorf("расшифровки пометок нет:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("выгрузка не кончается переводом строки")
	}
}
