// Package service собирает срез из источников.
//
// Здесь проходит главная граница системы. Аналитик (позже — модель) читает
// источники и возвращает утверждения со ссылками на них. Всё остальное —
// готовность, просрочки, возраст блокера, количество пробелов — считается
// здесь, кодом, из тех же данных. Поэтому два запуска на одних источниках дают
// одинаковые числа, и любую цифру в срезе можно проверить руками.
//
// Срез не редактируется. Его пересобирают: источники и факты только
// добавляются, версия увеличивается, прежние версии остаются лежать. Так видно,
// что изменилось между двумя отчётами и почему.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// ErrInvalid возвращается, когда во входных данных не хватает обязательного
// поля.
var ErrInvalid = errors.New("некорректные данные")

// Service — прикладной слой реестра.
type Service struct {
	store   store.Store
	analyst analyst.Analyst
	now     func() time.Time
	log     *slog.Logger
}

// New собирает сервис. Часы отдельным полем, а не вызовом time.Now по месту:
// срез весь состоит из расстояний между датами, и тесту нужно уметь встать в
// нужный день.
func New(st store.Store, an analyst.Analyst, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:   st,
		analyst: an,
		now:     func() time.Time { return time.Now().UTC() },
		log:     log,
	}
}

// Clock подменяет часы сервиса.
func (s *Service) Clock(f func() time.Time) { s.now = f }

// Now сообщает текущее время сервиса.
func (s *Service) Now() time.Time { return s.now() }

// Analyst сообщает, кто разбирает источники.
func (s *Service) Analyst() string { return s.analyst.Name() }

// Tasks возвращает все задачи.
func (s *Service) Tasks(ctx context.Context) ([]domain.Task, error) {
	return s.store.Tasks(ctx)
}

// Task возвращает задачу по идентификатору.
func (s *Service) Task(ctx context.Context, id string) (domain.Task, error) {
	return s.store.Task(ctx, id)
}

// CreateTask создаёт задачу, дополняя незаполненные поля.
func (s *Service) CreateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		return domain.Task{}, fmt.Errorf("название задачи пустое: %w", ErrInvalid)
	}
	t.Project = strings.TrimSpace(t.Project)
	t.Author = strings.TrimSpace(t.Author)
	t.Assignee = strings.TrimSpace(t.Assignee)

	if t.ID == "" {
		t.ID = newID("t")
	}
	if t.OpenedAt.IsZero() {
		t.OpenedAt = s.now()
	}

	if err := s.store.CreateTask(ctx, t); err != nil {
		return domain.Task{}, err
	}
	s.log.Info("задача создана", "задача", t.ID, "название", t.Title)
	return t, nil
}

// Sources возвращает источники задачи.
func (s *Service) Sources(ctx context.Context, taskID string) ([]domain.Source, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.Sources(ctx, taskID)
}

// AddSource добавляет источник к задаче.
//
// Срез после этого не пересобирается сам: сборка — отдельное решение
// пользователя, потому что она меняет числа в отчёте, который он мог уже
// прочитать.
func (s *Service) AddSource(ctx context.Context, src domain.Source) (domain.Source, error) {
	src.Title = strings.TrimSpace(src.Title)
	if strings.TrimSpace(src.Body) == "" {
		return domain.Source{}, fmt.Errorf("текст источника пустой: %w", ErrInvalid)
	}
	if !src.Kind.Valid() {
		return domain.Source{}, fmt.Errorf("вид источника %q неизвестен: %w", src.Kind, ErrInvalid)
	}
	if src.Title == "" {
		src.Title = src.Kind.Label()
	}
	if src.ID == "" {
		src.ID = newID("s")
	}
	if src.UploadedAt.IsZero() {
		src.UploadedAt = s.now()
	}

	if err := s.store.AddSource(ctx, src); err != nil {
		return domain.Source{}, err
	}
	s.log.Info("источник загружен", "задача", src.TaskID, "источник", src.ID, "знаков", src.Size())
	return src, nil
}

// Slice возвращает последний собранный срез, а если его ещё нет — собирает.
func (s *Service) Slice(ctx context.Context, taskID string) (domain.Slice, error) {
	sl, err := s.store.LatestSlice(ctx, taskID)
	switch {
	case err == nil:
		return sl, nil
	case errors.Is(err, store.ErrNotFound):
		return s.Rebuild(ctx, taskID)
	}
	return domain.Slice{}, err
}

// Rebuild пересобирает срез задачи из её источников.
//
// Порядок важен: факты и схемы сначала ложатся в журнал, и только потом журнал
// читается целиком. Поэтому срез собирается из всей накопленной истории задачи,
// а не только из последнего разбора.
func (s *Service) Rebuild(ctx context.Context, taskID string) (domain.Slice, error) {
	now := s.now()

	task, err := s.store.Task(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}
	sources, err := s.store.Sources(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}

	out, err := s.analyst.Extract(ctx, analyst.Input{Task: task, Sources: sources, Now: now})
	if err != nil {
		return domain.Slice{}, fmt.Errorf("разбор источников: %w", err)
	}

	if err := s.store.AddFacts(ctx, out.Facts); err != nil {
		return domain.Slice{}, err
	}
	for _, p := range out.Processes {
		p.TaskID = taskID
		if err := s.store.PutProcess(ctx, p); err != nil {
			return domain.Slice{}, err
		}
	}

	facts, err := s.store.Facts(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}
	version, err := s.store.NextSliceVersion(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}

	sl := assemble(task, sources, facts, out, version, now, s.analyst.Name())
	if err := s.store.SaveSlice(ctx, sl); err != nil {
		return domain.Slice{}, err
	}

	s.log.Info("срез собран",
		"задача", taskID,
		"версия", sl.Version,
		"источников", len(sources),
		"фактов", len(facts),
		"готовность", sl.Status.Readiness.Text,
		"пробелов", sl.Gaps())
	return sl, nil
}

// Processes возвращает схемы процессов задачи.
func (s *Service) Processes(ctx context.Context, taskID string) ([]domain.Process, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.Processes(ctx, taskID)
}

// Facts возвращает журнал фактов задачи.
func (s *Service) Facts(ctx context.Context, taskID string) ([]domain.Fact, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.Facts(ctx, taskID)
}

// Source возвращает источник по идентификатору.
func (s *Service) Source(ctx context.Context, id string) (domain.Source, error) {
	return s.store.Source(ctx, id)
}

// assemble складывает срез из разбора и журнала фактов.
//
// Функция чистая: ни хранилища, ни времени внутри — только то, что передали.
// Поэтому её проверяет обычный табличный тест, без поднятия сервиса.
//
// Правило распределения ответственности, чтобы оно не расползлось:
//   - структуры (этапы, блокеры, риски, вопросы) приходят полями analyst.Output;
//   - одиночные значения, у которых есть основа в карточке (паспорт, следующая
//     точка контроля), берутся из фактов и перебивают карточку;
//   - числа не приходят ниоткуда — они считаются здесь.
func assemble(
	task domain.Task,
	sources []domain.Source,
	facts []domain.Fact,
	out analyst.Output,
	version int,
	now time.Time,
	analystName string,
) domain.Slice {
	byField := make(map[string]domain.Value, len(facts))
	for _, f := range facts {
		byField[f.Field] = f.Value // поздний факт перекрывает ранний
	}

	sl := domain.Slice{
		TaskID:   task.ID,
		Version:  version,
		BuiltAt:  now,
		Analyst:  analystName,
		Passport: passport(task, byField),
		Goal: domain.Goal{
			AsStated:   out.GoalAsStated,
			Clarified:  out.GoalClarified,
			Criteria:   out.Criteria,
			OutOfScope: out.OutOfScope,
		},
		Status: domain.Status{
			Stage:      out.Stage,
			Readiness:  readiness(out.Milestones),
			Milestones: sortMilestones(out.Milestones),
			Done:       out.Done,
			Left:       out.Left,
		},
		Blockers: sortBlockers(out.Blockers),
		Risks:    sortRisks(out.Risks),
		PMActions: domain.PMActions{
			Needed:    out.PMActions,
			NextCheck: value(byField, "pmActions.nextCheck", domain.Missing("следующая точка контроля не назначена")),
		},
		Questions: out.Questions,
		Artifacts: out.Artifacts,
	}

	sl.Passport.Shifts = out.Shifts
	for _, src := range sources {
		sl.SourceIDs = append(sl.SourceIDs, src.ID)
	}
	return sl
}

// passport кладёт факты поверх полей карточки.
//
// Карточка — не источник: она говорит, что в неё вписали, а не что происходит.
// Ради этого расхождения журнал фактов и нужен: постановщик в карточке один, а
// требования ставит другой, и срез должен показать второго, объяснив почему.
func passport(task domain.Task, byField map[string]domain.Value) domain.Passport {
	return domain.Passport{
		Title:    value(byField, "passport.title", card(task.Title)),
		Author:   value(byField, "passport.author", card(task.Author)),
		Assignee: value(byField, "passport.assignee", card(task.Assignee)),
		OpenedAt: value(byField, "passport.openedAt", cardDate(task.OpenedAt)),
		Deadline: value(byField, "passport.deadline", cardDate(task.Deadline)),
	}
}

// value достаёт факт по имени поля, а если его нет — отдаёт основу.
func value(byField map[string]domain.Value, field string, base domain.Value) domain.Value {
	if v, ok := byField[field]; ok && v.Known() {
		return v
	}
	return base
}

// card — значение из поля карточки задачи.
func card(text string) domain.Value {
	if strings.TrimSpace(text) == "" {
		return domain.Missing("поле карточки не заполнено")
	}
	return domain.Value{Text: text, Origin: domain.OriginQuoted, Note: "поле карточки задачи"}
}

// cardDate — дата из поля карточки задачи.
func cardDate(t time.Time) domain.Value {
	if t.IsZero() {
		return domain.Missing("поле карточки не заполнено")
	}
	return card(domain.FormatDate(t))
}

// readiness считает готовность по этапам плана.
//
// Считает всегда здесь и никогда не берёт у аналитика. Модель может ошибиться в
// арифметике незаметно, а спор с заказчиком идёт как раз о проценте: он должен
// пересчитываться из тех же этапов кем угодно и сходиться.
func readiness(ms []domain.Milestone) domain.Value {
	if len(ms) == 0 {
		return domain.Missing("плана нет: этапы в источниках не найдены")
	}
	share, done := domain.Readiness(ms)
	return domain.Computed(ru.Percent(share), fmt.Sprintf("%s из %s плана закрыто",
		ru.Fixed(done, 1), ru.CountOf(len(ms), "этапа", "этапов")))
}

// sortMilestones выстраивает этапы по сроку: план читают как календарь.
func sortMilestones(ms []domain.Milestone) []domain.Milestone {
	out := make([]domain.Milestone, len(ms))
	copy(out, ms)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Due.Before(out[j].Due) })
	return out
}

// sortBlockers ставит вперёд самый старый блокер: он и есть главная новость.
func sortBlockers(bs []domain.Blocker) []domain.Blocker {
	out := make([]domain.Blocker, len(bs))
	copy(out, bs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// sortRisks ставит вперёд риск с большим влиянием на срок.
func sortRisks(rs []domain.Risk) []domain.Risk {
	out := make([]domain.Risk, len(rs))
	copy(out, rs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].DaysImpact > out[j].DaysImpact })
	return out
}
