package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/analyst/manual"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
	"github.com/Anemiaaaa/reestr/internal/store/jsonstore"
)

// today — день, на котором стоят часы во всех тестах.
//
// Часы подменяются, а не берутся настоящие, потому что срез считает возраст
// блокеров и просрочки от «сегодня». На настоящих часах такие проверки живут до
// следующего утра, а потом начинают падать без изменений в коде.
var today = time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC)

// newService поднимает сервис на настоящем хранилище во временном каталоге.
//
// Подделку хранилища здесь писать не за чем: jsonstore покрыт своими тестами,
// живёт в каталоге, который t.TempDir унесёт с собой, и проверка идёт тем же
// путём, что рабочий запуск, — включая сериализацию среза в JSON и обратно.
func newService(t *testing.T) *Service {
	t.Helper()

	st, err := jsonstore.Open(filepath.Join(t.TempDir(), "reestr.json"))
	if err != nil {
		t.Fatalf("открыть хранилище: %v", err)
	}
	s := New(st, manual.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Clock(func() time.Time { return today })
	return s
}

// seeded возвращает сервис с посеянной задачей АУРА.
//
// Посев идёт прямо в хранилище, минуя CreateTask, — так же, как в cmd/reestr.
// Сервис заполнил бы пустую дату постановки текущей, а в карточке АУРА её нет
// намеренно: срез обязан восстановить её фактом из переписки, и подмена на
// входе съела бы ровно то, что проверяется.
func seeded(t *testing.T) *Service {
	t.Helper()

	s := newService(t)
	ctx := context.Background()
	task, sources := manual.Seed()

	if err := s.store.CreateTask(ctx, task); err != nil {
		t.Fatalf("посев задачи: %v", err)
	}
	for _, src := range sources {
		if err := s.store.AddSource(ctx, src); err != nil {
			t.Fatalf("посев источника %s: %v", src.ID, err)
		}
	}
	return s
}

func TestCreateTask(t *testing.T) {
	t.Run("обрезает пробелы и заполняет пропущенное", func(t *testing.T) {
		s := newService(t)

		got, err := s.CreateTask(context.Background(), domain.Task{
			Project:  "  АУРА  ",
			Title:    "  Внедрение автоматизации  ",
			Author:   "  Ризван Мирзаев  ",
			Assignee: "  Мухаммад Минатулаев  ",
		})
		if err != nil {
			t.Fatalf("создать задачу: %v", err)
		}

		if got.Title != "Внедрение автоматизации" {
			t.Errorf("название %q, а пробелы должны быть срезаны", got.Title)
		}
		if got.Project != "АУРА" || got.Author != "Ризван Мирзаев" || got.Assignee != "Мухаммад Минатулаев" {
			t.Errorf("остальные поля не обрезаны: %+v", got)
		}
		if !strings.HasPrefix(got.ID, "t-") {
			t.Errorf("идентификатор %q, ожидался префикс t-", got.ID)
		}
		if !got.OpenedAt.Equal(today) {
			t.Errorf("дата постановки %v, а часы сервиса стоят на %v", got.OpenedAt, today)
		}
	})

	t.Run("не трогает заданное", func(t *testing.T) {
		s := newService(t)
		opened := time.Date(2026, time.June, 6, 0, 0, 0, 0, time.UTC)

		got, err := s.CreateTask(context.Background(), domain.Task{
			ID:       "aura",
			Title:    "Внедрение",
			OpenedAt: opened,
		})
		if err != nil {
			t.Fatalf("создать задачу: %v", err)
		}
		if got.ID != "aura" {
			t.Errorf("идентификатор %q, а заданный должен сохраниться", got.ID)
		}
		if !got.OpenedAt.Equal(opened) {
			t.Errorf("дата постановки %v, ожидалась заданная %v", got.OpenedAt, opened)
		}
	})

	t.Run("пустое название не проходит", func(t *testing.T) {
		s := newService(t)

		for _, title := range []string{"", "   ", "\t\n"} {
			_, err := s.CreateTask(context.Background(), domain.Task{Title: title})
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("название %q: ошибка %v, ожидалась ErrInvalid", title, err)
			}
		}
	})

	t.Run("повторный идентификатор не проходит", func(t *testing.T) {
		s := newService(t)
		ctx := context.Background()

		if _, err := s.CreateTask(ctx, domain.Task{ID: "aura", Title: "Первая"}); err != nil {
			t.Fatalf("создать задачу: %v", err)
		}
		_, err := s.CreateTask(ctx, domain.Task{ID: "aura", Title: "Вторая"})
		if !errors.Is(err, store.ErrExists) {
			t.Errorf("ошибка %v, ожидалась store.ErrExists", err)
		}
	})
}

func TestPinChat(t *testing.T) {
	t.Run("закрепляет чат за задачей", func(t *testing.T) {
		s := newService(t)
		ctx := context.Background()

		task, err := s.CreateTask(ctx, domain.Task{Title: "Внедрение"})
		if err != nil {
			t.Fatalf("создать задачу: %v", err)
		}
		got, err := s.PinChat(ctx, domain.ChatLink{
			TaskID: task.ID, System: domain.SystemBitrix,
			DialogID: "chat12", Title: "АУРА", ExternalTaskID: "4",
		})
		if err != nil {
			t.Fatalf("закрепить чат: %v", err)
		}
		if got.TaskID != task.ID || got.DialogID != "chat12" || got.Title != "АУРА" {
			t.Errorf("связь %+v", got)
		}
		// Номер задачи портала обязан дойти до хранилища: без него по «chat12» не
		// вернуться к задаче, чей это чат.
		if got.ExternalTaskID != "4" {
			t.Errorf("задача портала = %q, хотели 4", got.ExternalTaskID)
		}
		if !got.LinkedAt.Equal(today) {
			t.Errorf("дата закрепления %v, часы сервиса на %v", got.LinkedAt, today)
		}

		list, err := s.TaskChats(ctx, task.ID)
		if err != nil {
			t.Fatalf("список чатов: %v", err)
		}
		if len(list) != 1 || list[0].DialogID != "chat12" {
			t.Errorf("чаты %+v", list)
		}
	})

	t.Run("без задачи не проходит", func(t *testing.T) {
		s := newService(t)
		_, err := s.PinChat(context.Background(), domain.ChatLink{
			TaskID: "нет-такой", System: domain.SystemBitrix, DialogID: "8",
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ошибка %v, ожидалась store.ErrNotFound", err)
		}
	})

	t.Run("пустое не проходит", func(t *testing.T) {
		s := newService(t)
		_, err := s.PinChat(context.Background(), domain.ChatLink{})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ошибка %v, ожидалась ErrInvalid", err)
		}
	})

	// Закрепление по задаче портала без настроенного Bitrix — отказ, а не связь
	// с пустым чатом. Реестр при этом продолжает работать: задача создаётся,
	// просто без закрепления, и решает это транспорт, а не сервис.
	t.Run("задача портала без портала не проходит", func(t *testing.T) {
		s := newService(t)
		ctx := context.Background()

		if _, err := s.PortalTask(ctx, "4"); !errors.Is(err, ErrInvalid) {
			t.Errorf("ошибка %v, ожидалась ErrInvalid", err)
		}
		if _, err := s.PortalTask(ctx, "  "); !errors.Is(err, ErrInvalid) {
			t.Errorf("пустой номер задачи портала: ошибка %v, ожидалась ErrInvalid", err)
		}
	})
}

func TestAddSource(t *testing.T) {
	t.Run("заполняет заголовок видом и ставит время загрузки", func(t *testing.T) {
		s := seeded(t)

		got, err := s.AddSource(context.Background(), domain.Source{
			TaskID: manual.TaskID,
			Kind:   domain.KindNote,
			Body:   "созвон 19 августа: подход не выбран",
		})
		if err != nil {
			t.Fatalf("добавить источник: %v", err)
		}

		if got.Title != domain.KindNote.Label() {
			t.Errorf("заголовок %q, ожидалась подпись вида %q", got.Title, domain.KindNote.Label())
		}
		if !strings.HasPrefix(got.ID, "s-") {
			t.Errorf("идентификатор %q, ожидался префикс s-", got.ID)
		}
		if !got.UploadedAt.Equal(today) {
			t.Errorf("время загрузки %v, а часы сервиса стоят на %v", got.UploadedAt, today)
		}
	})

	t.Run("пустой текст не проходит", func(t *testing.T) {
		s := seeded(t)

		_, err := s.AddSource(context.Background(), domain.Source{
			TaskID: manual.TaskID,
			Kind:   domain.KindNote,
			Body:   "   \n  ",
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ошибка %v, ожидалась ErrInvalid", err)
		}
	})

	t.Run("неизвестный вид не проходит", func(t *testing.T) {
		s := seeded(t)

		_, err := s.AddSource(context.Background(), domain.Source{
			TaskID: manual.TaskID,
			Kind:   domain.SourceKind("телепатия"),
			Body:   "текст",
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ошибка %v, ожидалась ErrInvalid", err)
		}
	})

	t.Run("источник без задачи не проходит", func(t *testing.T) {
		s := newService(t)

		_, err := s.AddSource(context.Background(), domain.Source{
			TaskID: "нет-такой",
			Kind:   domain.KindNote,
			Body:   "текст",
		})
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ошибка %v, ожидалась store.ErrNotFound", err)
		}
	})
}

// TestRebuildSeed проверяет срез АУРА целиком: это и разбор реальной задачи, и
// образец того, что должна выдавать модель. Числа здесь те же, что обещаны в
// README, — иначе документация начнёт расходиться с кодом молча.
func TestRebuildSeed(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()

	sl, err := s.Rebuild(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("собрать срез: %v", err)
	}

	if sl.Version != 1 {
		t.Errorf("версия %d, ожидалась первая", sl.Version)
	}
	if !sl.BuiltAt.Equal(today) {
		t.Errorf("дата сборки %v, а часы сервиса стоят на %v", sl.BuiltAt, today)
	}
	if sl.Analyst != s.Analyst() {
		t.Errorf("аналитик %q, ожидался %q", sl.Analyst, s.Analyst())
	}

	// Готовность считает код, а не аналитик: 2,4 закрытых этапа из 9.
	if got, want := sl.Status.Readiness.Text, "27 %"; got != want {
		t.Errorf("готовность %q, ожидалось %q", got, want)
	}
	if sl.Status.Readiness.Origin != domain.OriginComputed {
		t.Errorf("происхождение готовности %q, ожидалось «посчитано»", sl.Status.Readiness.Origin)
	}
	if got, want := sl.Status.Readiness.Note, "2,4 из 9 этапов плана закрыто"; got != want {
		t.Errorf("расшифровка готовности %q, ожидалось %q", got, want)
	}

	// Факт из переписки перекрывает поле карточки — и объясняет, почему.
	if got, want := sl.Passport.Author.Text, "Курбанмагомед Джамалутдинов"; got != want {
		t.Errorf("постановщик %q, ожидался %q: факт должен перебить карточку", got, want)
	}
	if sl.Passport.Author.Origin != domain.OriginDerived {
		t.Errorf("происхождение постановщика %q, ожидалось «выведено»", sl.Passport.Author.Origin)
	}
	// А там, где факта нет, значение берётся из карточки и помечается как её поле.
	if got, want := sl.Passport.Title.Note, "поле карточки задачи"; got != want {
		t.Errorf("пояснение к названию %q, ожидалось %q", got, want)
	}

	counts := []struct {
		what string
		got  int
		want int
	}{
		{"этапов плана", len(sl.Status.Milestones), 9},
		{"блокеров", len(sl.Blockers), 1},
		{"рисков", len(sl.Risks), 7},
		{"вопросов", len(sl.Questions), 13},
		{"артефактов", len(sl.Artifacts), 4},
		{"критериев приёмки", len(sl.Goal.Criteria), 6},
		{"источников", len(sl.SourceIDs), 2},
		{"пробелов", sl.Gaps(), 8},
	}
	for _, c := range counts {
		if c.got != c.want {
			t.Errorf("%s: %d, ожидалось %d", c.what, c.got, c.want)
		}
	}

	want := []string{manual.SourceHis, manual.SourceAud}
	for i := range want {
		if i >= len(sl.SourceIDs) {
			t.Errorf("источник %d потерян, ожидался %q", i, want[i])
			continue
		}
		if sl.SourceIDs[i] != want[i] {
			t.Errorf("источник %d: %q, ожидался %q", i, sl.SourceIDs[i], want[i])
		}
	}

	facts, err := s.Facts(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("прочитать факты: %v", err)
	}
	if len(facts) != 32 {
		t.Errorf("фактов %d, ожидалось 32", len(facts))
	}
	for _, f := range facts {
		if f.Field == "" {
			t.Errorf("факт %s без адреса поля", f.ID)
		}
		if f.Value.Origin == domain.OriginQuoted && f.Value.Quote == "" {
			t.Errorf("факт %s помечен цитатой, но цитаты нет", f.Field)
		}
		if f.Value.Origin != domain.OriginMissing && f.Value.SourceID == "" {
			t.Errorf("факт %s без ссылки на источник", f.Field)
		}
	}

	// Порядок внутри разделов задаёт сервис, а не аналитик: этапы и блокеры по
	// возрастанию даты, риски — по убыванию влияния на срок.
	for i := 1; i < len(sl.Status.Milestones); i++ {
		if sl.Status.Milestones[i].Due.Before(sl.Status.Milestones[i-1].Due) {
			t.Errorf("этапы не по возрастанию срока на позиции %d", i)
		}
	}
	for i := 1; i < len(sl.Risks); i++ {
		if sl.Risks[i].DaysImpact > sl.Risks[i-1].DaysImpact {
			t.Errorf("риски не по убыванию влияния на позиции %d", i)
		}
	}
}

// TestRebuildSkipsUnchangedMaterial: пересборка по неизменившемуся материалу
// новой версии не даёт.
//
// Раньше каждое нажатие кнопки добавляло в журнал те же факты заново и заводило
// версию, ничем не отличающуюся от предыдущей: история заполнялась пустыми
// различиями, а журнал фактов рос от кнопки, а не от событий. С подключённой
// моделью у этого появилась ещё и цена — оплаченный вызов, после которого в
// реестре ничего не меняется.
func TestRebuildSkipsUnchangedMaterial(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()

	first, err := s.Rebuild(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("первая сборка: %v", err)
	}
	factsAfterFirst, err := s.Facts(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("факты: %v", err)
	}

	again, err := s.Rebuild(ctx, manual.TaskID)
	// Срез возвращается прежний и годный, но вызывающий обязан заметить, что
	// собрано не было: молчаливое «вот вам прежняя версия» человек прочитал бы
	// как «разбор ничего нового не нашёл», а это другое утверждение.
	if !errors.Is(err, ErrNoChanges) {
		t.Fatalf("ошибка %v, ожидалась ErrNoChanges", err)
	}
	if again.Version != first.Version {
		t.Errorf("версия %d, ожидалась прежняя %d", again.Version, first.Version)
	}

	factsAfterSecond, err := s.Facts(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("факты: %v", err)
	}
	if len(factsAfterSecond) != len(factsAfterFirst) {
		t.Errorf("фактов стало %d вместо %d: журнал растёт от кнопки, а не от событий",
			len(factsAfterSecond), len(factsAfterFirst))
	}

	// Новый материал пересборку разблокирует.
	_, err = s.AddSource(ctx, domain.Source{
		TaskID: manual.TaskID, Kind: domain.KindNote, Title: "Заметка", Body: "новое",
	})
	if err != nil {
		t.Fatalf("добавить источник: %v", err)
	}
	third, err := s.Rebuild(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("сборка после нового материала: %v", err)
	}
	if third.Version != first.Version+1 {
		t.Errorf("версия %d, ожидалась %d", third.Version, first.Version+1)
	}
}

// TestRebuildIsReproducible — главное свойство разделения труда: аналитик
// извлекает, код считает, и второй проход по тем же фактам даёт те же цифры.
// Версия при этом растёт, прежняя остаётся лежать.
//
// Между сборками добавляется источник: без нового материала пересборка теперь
// пропускается, а проверить надо именно повторяемость счёта.
func TestRebuildIsReproducible(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()

	first, err := s.Rebuild(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("первая сборка: %v", err)
	}

	// Ручной разбор от источников не зависит — он возвращает один и тот же
	// набор фактов. Значит все посчитанные величины обязаны совпасть, а
	// расхождение означало бы, что счёт зависит от чего-то, кроме фактов.
	_, err = s.AddSource(ctx, domain.Source{
		TaskID: manual.TaskID, Kind: domain.KindNote, Title: "Заметка", Body: "повод пересобрать",
	})
	if err != nil {
		t.Fatalf("добавить источник: %v", err)
	}

	second, err := s.Rebuild(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("вторая сборка: %v", err)
	}

	if second.Version != first.Version+1 {
		t.Errorf("версия %d, ожидалась %d", second.Version, first.Version+1)
	}
	if second.Status.Readiness.Text != first.Status.Readiness.Text {
		t.Errorf("готовность разошлась: %q против %q",
			second.Status.Readiness.Text, first.Status.Readiness.Text)
	}
	if second.Gaps() != first.Gaps() {
		t.Errorf("число пробелов разошлось: %d против %d", second.Gaps(), first.Gaps())
	}
	if len(second.Status.Milestones) != len(first.Status.Milestones) {
		t.Errorf("этапов %d, ожидалось %d",
			len(second.Status.Milestones), len(first.Status.Milestones))
	}

	// Срез не правится, а добавляется: обе версии лежат в хранилище.
	latest, err := s.store.LatestSlice(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("прочитать последний срез: %v", err)
	}
	if latest.Version != second.Version {
		t.Errorf("последняя версия %d, ожидалась %d", latest.Version, second.Version)
	}
}

// TestSliceBuildsOnce: чтение среза собирает его, если среза ещё нет, и не
// пересобирает, если он уже есть. Иначе каждое открытие страницы плодило бы
// версию.
func TestSliceBuildsOnce(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()

	first, err := s.Slice(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("первое чтение: %v", err)
	}
	if first.Version != 1 {
		t.Errorf("версия %d, ожидалась первая", first.Version)
	}

	second, err := s.Slice(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("второе чтение: %v", err)
	}
	if second.Version != first.Version {
		t.Errorf("версия %d, ожидалась та же %d: чтение не должно пересобирать",
			second.Version, first.Version)
	}
}

func TestRebuildUnknownTask(t *testing.T) {
	s := newService(t)

	_, err := s.Rebuild(context.Background(), "нет-такой")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ошибка %v, ожидалась store.ErrNotFound", err)
	}
}

// TestRebuildWithoutPlan: задача, которую ручной аналитик не разбирал. Пробелы
// должны стать видны, а не подмениться нулями — «нет данных» это значение.
func TestRebuildWithoutPlan(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, domain.Task{Title: "Задача без разбора"})
	if err != nil {
		t.Fatalf("создать задачу: %v", err)
	}

	sl, err := s.Rebuild(ctx, task.ID)
	if err != nil {
		t.Fatalf("собрать срез: %v", err)
	}

	if sl.Status.Readiness.Known() {
		t.Errorf("готовность %q, а плана нет — ожидалось «нет данных»", sl.Status.Readiness.Text)
	}
	if got, want := sl.Status.Readiness.Note, "плана нет: этапы в источниках не найдены"; got != want {
		t.Errorf("пояснение %q, ожидалось %q", got, want)
	}
	if sl.Status.Stage.Known() {
		t.Errorf("этап %q, а разбора нет — ожидалось «нет данных»", sl.Status.Stage.Text)
	}
	// Название пришло из карточки, поэтому оно известно даже без разбора.
	if !sl.Passport.Title.Known() {
		t.Error("название неизвестно, а в карточке оно есть")
	}
}

func TestRebuildRespectsContext(t *testing.T) {
	s := seeded(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Rebuild(ctx, manual.TaskID); !errors.Is(err, context.Canceled) {
		t.Errorf("ошибка %v, ожидалась context.Canceled", err)
	}
}

// TestAssembleFactsOverCard проверяет само правило подстановки, без хранилища и
// без часов: assemble принимает всё параметрами, поэтому его можно позвать
// напрямую.
func TestAssembleFactsOverCard(t *testing.T) {
	card := domain.Task{ID: "t", Title: "Название из карточки", Author: "Ризван Мирзаев"}

	tests := []struct {
		name       string
		task       domain.Task
		facts      []domain.Fact
		wantAuthor string
		wantOrigin domain.Origin
	}{
		{
			name:       "без фактов берётся карточка",
			task:       card,
			wantAuthor: "Ризван Мирзаев",
			wantOrigin: domain.OriginQuoted,
		},
		{
			name: "известный факт перекрывает карточку",
			task: card,
			facts: []domain.Fact{{
				Field: "passport.author",
				Value: domain.Derived("Курбанмагомед", "s1", "приёмку ведёт он"),
			}},
			wantAuthor: "Курбанмагомед",
			wantOrigin: domain.OriginDerived,
		},
		{
			name: "факт «нет данных» карточку не перекрывает",
			task: card,
			facts: []domain.Fact{{
				Field: "passport.author",
				Value: domain.Missing("в переписке не назван"),
			}},
			wantAuthor: "Ризван Мирзаев",
			wantOrigin: domain.OriginQuoted,
		},
		{
			name: "поздний факт перекрывает ранний",
			task: card,
			facts: []domain.Fact{
				{Field: "passport.author", Value: domain.Quoted("Первый", "s1", "цитата")},
				{Field: "passport.author", Value: domain.Quoted("Второй", "s2", "цитата")},
			},
			wantAuthor: "Второй",
			wantOrigin: domain.OriginQuoted,
		},
		{
			name:       "пустая карточка без факта — пробел",
			task:       domain.Task{ID: "t", Title: "Название"},
			wantAuthor: "",
			wantOrigin: domain.OriginMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sl := assemble(tt.task, nil, tt.facts, analyst.Output{}, 1, today, "тест")

			if sl.Passport.Author.Text != tt.wantAuthor {
				t.Errorf("постановщик %q, ожидался %q", sl.Passport.Author.Text, tt.wantAuthor)
			}
			if sl.Passport.Author.Origin != tt.wantOrigin {
				t.Errorf("происхождение %q, ожидалось %q", sl.Passport.Author.Origin, tt.wantOrigin)
			}
		})
	}
}

// Список источников версии выводится из ссылок в самом срезе, а не из того, что
// подали на разбор: в чате может быть сто сообщений, а для вывода понадобиться
// семь. Проверяем оба пути ссылки — факты и разбор аналитика, — порядок первого
// упоминания и то, что один источник в двух полях не удваивается.
func TestAssembleCollectsOnlyCitedSources(t *testing.T) {
	sources := []domain.Source{
		{ID: "s1", Kind: domain.KindCorrespondence},
		{ID: "s2", Kind: domain.KindAudit},
		{ID: "s3", Kind: domain.KindNote},
		{ID: "s4", Kind: domain.KindCorrespondence}, // ни в одном поле не упомянут
		{ID: "s5", Kind: domain.KindSpec},
	}

	// Паспорт заполняется фактами, остальное — разбором аналитика. Оба пути
	// должны приводить источник в список.
	facts := []domain.Fact{
		{Field: "passport.author", Value: domain.Quoted("Волошук", "s2", "поставил Волошук")},
		{Field: "passport.assignee", Value: domain.Quoted("Джонсон", "s2", "исполнитель Джонсон")},
	}
	out := analyst.Output{
		GoalAsStated: domain.Quoted("Автоматизировать приём заявок", "s1", "нужно автоматизировать приём"),
		Stage:        domain.Quoted("в работе", "s3", "работы идут"),
		Processes: []domain.Process{
			{Kind: domain.ProcessAsIs, Evidence: domain.Quoted("как есть", "s5", "заявки принимают вручную")},
		},
	}

	sl := assemble(domain.Task{ID: "t", Title: "Название"}, sources, facts, out, 3, today, "тест")

	// Порядок обхода: паспорт, цель, статус, а схемы процессов дописываются
	// в конец. s2 назван дважды и обязан остаться одной записью.
	want := []string{"s2", "s1", "s3", "s5"}
	if len(sl.SourceIDs) != len(want) {
		t.Fatalf("источников %d %v, ожидалось %d %v", len(sl.SourceIDs), sl.SourceIDs, len(want), want)
	}
	for i, id := range want {
		if sl.SourceIDs[i] != id {
			t.Errorf("источник %d: %q, ожидался %q (весь список %v)", i, sl.SourceIDs[i], id, sl.SourceIDs)
		}
	}

	// В версию попадают не все источники, но пересмотрен должен быть каждый:
	// иначе не понять, из какого объёма материала сделан вывод.
	if sl.Considered != len(sources) {
		t.Errorf("рассмотрено %d, ожидалось %d", sl.Considered, len(sources))
	}
}

// Источник, поданный на разбор, но ни в одном поле не процитированный, в версию
// не попадает: иначе список источников превратился бы в опись входа.
func TestAssembleSkipsUncitedSources(t *testing.T) {
	sources := []domain.Source{
		{ID: "s1", Kind: domain.KindCorrespondence},
		{ID: "s2", Kind: domain.KindAudit},
	}

	sl := assemble(domain.Task{ID: "t", Title: "Название"}, sources, nil, analyst.Output{}, 1, today, "тест")

	if len(sl.SourceIDs) != 0 {
		t.Errorf("источники %v, ожидался пустой список: срез ни на один из них не ссылается", sl.SourceIDs)
	}
	if sl.Considered != len(sources) {
		t.Errorf("рассмотрено %d, ожидалось %d", sl.Considered, len(sources))
	}
}

func TestAssembleSetsVersionFields(t *testing.T) {
	sl := assemble(domain.Task{ID: "t", Title: "Название"}, nil, nil, analyst.Output{}, 3, today, "тест")

	if sl.TaskID != "t" {
		t.Errorf("задача %q, ожидалась %q", sl.TaskID, "t")
	}
	if sl.Version != 3 {
		t.Errorf("версия %d, ожидалась 3", sl.Version)
	}
	if !sl.BuiltAt.Equal(today) {
		t.Errorf("собран %v, ожидалось %v", sl.BuiltAt, today)
	}
	if sl.Analyst != "тест" {
		t.Errorf("аналитик %q, ожидался %q", sl.Analyst, "тест")
	}
}

func TestReadiness(t *testing.T) {
	tests := []struct {
		name     string
		ms       []domain.Milestone
		wantText string
		wantNote string
		wantMiss bool
	}{
		{
			name:     "плана нет",
			wantNote: "плана нет: этапы в источниках не найдены",
			wantMiss: true,
		},
		{
			name:     "ничего не начато",
			ms:       []domain.Milestone{{Progress: 0}, {Progress: 0}},
			wantText: "0 %",
			wantNote: "0 из 2 этапов плана закрыто",
		},
		{
			name:     "один этап закрыт",
			ms:       []domain.Milestone{{Progress: 1}},
			wantText: "100 %",
			wantNote: "1 из 1 этапа плана закрыто",
		},
		{
			name:     "половина одного из двух",
			ms:       []domain.Milestone{{Progress: 0.5}, {Progress: 0}},
			wantText: "25 %",
			wantNote: "0,5 из 2 этапов плана закрыто",
		},
		{
			name: "как в демонстрационных данных",
			ms: []domain.Milestone{
				{Progress: 1}, {Progress: 1}, {Progress: 0.4},
				{Progress: 0}, {Progress: 0}, {Progress: 0},
				{Progress: 0}, {Progress: 0}, {Progress: 0},
			},
			wantText: "27 %",
			wantNote: "2,4 из 9 этапов плана закрыто",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readiness(tt.ms)

			if tt.wantMiss {
				if got.Known() {
					t.Errorf("значение %q, ожидалось «нет данных»", got.Text)
				}
			} else {
				if got.Text != tt.wantText {
					t.Errorf("текст %q, ожидался %q", got.Text, tt.wantText)
				}
				if got.Origin != domain.OriginComputed {
					t.Errorf("происхождение %q, ожидалось «посчитано»", got.Origin)
				}
			}
			if got.Note != tt.wantNote {
				t.Errorf("расшифровка %q, ожидалась %q", got.Note, tt.wantNote)
			}
		})
	}
}

func TestCardAndCardDate(t *testing.T) {
	if got := card("  "); got.Known() {
		t.Errorf("пустое поле карточки дало значение %q, ожидался пробел", got.Text)
	}
	if got := card("Название"); got.Origin != domain.OriginQuoted || got.Text != "Название" {
		t.Errorf("поле карточки собрано неверно: %+v", got)
	}
	if got := cardDate(time.Time{}); got.Known() {
		t.Errorf("нулевая дата дала значение %q, ожидался пробел", got.Text)
	}
	if got := cardDate(today); got.Text != "19.08.2026" {
		t.Errorf("дата %q, ожидалось 19.08.2026", got.Text)
	}
}

func TestClock(t *testing.T) {
	s := newService(t)

	stamp := time.Date(2026, time.December, 31, 23, 59, 0, 0, time.UTC)
	s.Clock(func() time.Time { return stamp })

	if !s.Now().Equal(stamp) {
		t.Errorf("часы показывают %v, ожидалось %v", s.Now(), stamp)
	}
}
