// Package analyst — место, куда позже подключится модель.
//
// Здесь описан один интерфейс и ничего больше. Реализация на модели ляжет
// рядом в analyst/claude, ручная разметка уже лежит в analyst/manual, и сервис
// не заметит подмены: он знает только Analyst.
//
// Контракт узкий намеренно. Аналитик читает источники и возвращает утверждения
// со ссылками на них — он не считает и не решает. Готовность, просрочки,
// возраст блокеров считает сервис на Go, потому что цифру, которую PM назовёт
// заказчику, нужно уметь повторить и проверить, а не получить заново при каждом
// вызове модели.
//
// Отсюда же следует граница ответственности за ошибки: если аналитик соврал,
// это видно по ссылке на источник; если соврала арифметика — это ошибка в коде,
// которую поймает тест.
package analyst

import (
	"context"
	"time"

	"github.com/Anemiaaaa/reestr/internal/domain"
)

// Input — всё, что аналитик получает на вход.
type Input struct {
	Task    domain.Task
	Sources []domain.Source

	// Now — дата, на которую собирается срез. Передаётся, а не берётся из
	// часов, чтобы разбор был воспроизводимым: тот же вход даёт тот же выход.
	Now time.Time
}

// Output — то, что аналитик извлёк из источников.
//
// Здесь нет ни готовности, ни числа дней просрочки: это производные величины,
// и считать их — работа сервиса. Аналитик даёт основания, а не выводы.
type Output struct {
	// Facts — атомарные утверждения с адресом поля в срезе.
	Facts []domain.Fact

	// Milestones — этапы плана с их прогрессом. Прогресс — наблюдение
	// («принято заказчиком», «сделано вполовину»), а не оценка готовности
	// задачи: её выведет сервис.
	Milestones []domain.Milestone

	// Shifts — найденные переносы срока.
	Shifts []domain.DeadlineShift

	Blockers  []domain.Blocker
	Risks     []domain.Risk
	Questions []domain.Question
	Artifacts []domain.Artifact
	Criteria  []domain.Criterion

	// Processes — схемы «как есть» и «как будет».
	Processes []domain.Process

	// Stage — этап задачи.
	Stage domain.Value

	// GoalAsStated и GoalClarified — цель словами постановщика и цель после
	// уточнений. Расхождение между ними само по себе важный вывод.
	GoalAsStated  domain.Value
	GoalClarified domain.Value

	// OutOfScope — то, что в объём работ не входит.
	OutOfScope []domain.Value

	// Done и Left — сделанное и оставшееся человеческим языком.
	Done []domain.Value
	Left []domain.Value

	// PMActions — что требуется от PM, чтобы работа пошла дальше.
	PMActions []domain.PMAction
}

// Analyst извлекает из источников утверждения о задаче.
type Analyst interface {
	// Name возвращает имя реализации; оно попадает в срез, чтобы читатель
	// видел, кто разбирал источники.
	Name() string

	// Extract разбирает источники задачи. Вызов может быть долгим и платным,
	// поэтому обязан уважать отмену контекста.
	Extract(ctx context.Context, in Input) (Output, error)
}
