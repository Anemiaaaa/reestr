package service

import (
	"context"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst/manual"
	"github.com/Anemiaaaa/reestr/internal/domain"
)

// msk — пояс, в котором заказчик назначил пересборку. Берётся честной загрузкой
// из базы поясов: смысл проверки в том, что «20:00 по Москве» остаётся восемью
// вечера, а фиксированное смещение это как раз и обошло бы.
func msk(t *testing.T) *time.Location {
	t.Helper()

	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Skipf("пояс Europe/Moscow недоступен: %v", err)
	}
	return loc
}

// TestUntilNextRun: расписание считается в поясе запуска, а не в UTC. На
// сервере, живущем по Гринвичу, «20:00 по Москве» иначе превратилось бы в
// одиннадцать вечера.
func TestUntilNextRun(t *testing.T) {
	t.Parallel()

	loc := msk(t)
	r := NewRebuilder(nil, 20, 0, loc, 0)

	cases := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{
			name: "днём того же дня",
			now:  time.Date(2026, time.September, 3, 14, 0, 0, 0, loc),
			want: 6 * time.Hour,
		},
		{
			// Ровно в час запуска ждём следующих суток, а не срабатываем
			// повторно: иначе цикл крутился бы весь этот час.
			name: "ровно в час запуска",
			now:  time.Date(2026, time.September, 3, 20, 0, 0, 0, loc),
			want: 24 * time.Hour,
		},
		{
			name: "сразу после запуска",
			now:  time.Date(2026, time.September, 3, 20, 1, 0, 0, loc),
			want: 23*time.Hour + 59*time.Minute,
		},
		{
			name: "ночью до запуска",
			now:  time.Date(2026, time.September, 4, 2, 30, 0, 0, loc),
			want: 17*time.Hour + 30*time.Minute,
		},
		{
			// Сервер по Гринвичу: 14:00 UTC это 17:00 в Москве, значит ждать
			// три часа, а не шесть.
			name: "часы сервера в другом поясе",
			now:  time.Date(2026, time.September, 3, 14, 0, 0, 0, time.UTC),
			want: 3 * time.Hour,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := r.until(tc.now); got != tc.want {
				t.Errorf("ждать %v, хотели %v", got, tc.want)
			}
		})
	}
}

// TestRunOnceSkipsUnchanged: у большинства задач за сутки не появляется ничего
// нового, и ночной проход обязан считать это обычной ночью, а не сбоем. С
// платной моделью это к тому же главная статья экономии.
func TestRunOnceSkipsUnchanged(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()

	r := NewRebuilder(s, 20, 0, time.UTC, 0)

	// Первый проход собирает: срезов ещё нет.
	r.RunOnce(ctx)
	first, err := s.store.LatestSlice(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("срез после первого прохода: %v", err)
	}

	// Второй проход по тому же материалу версии не добавляет.
	r.RunOnce(ctx)
	second, err := s.store.LatestSlice(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("срез после второго прохода: %v", err)
	}
	if second.Version != first.Version {
		t.Errorf("версия %d, ожидалась прежняя %d: ночной проход множит версии",
			second.Version, first.Version)
	}

	// Новый материал проход подхватывает.
	if _, err := s.AddSource(ctx, domain.Source{
		TaskID: manual.TaskID, Kind: domain.KindNote, Title: "Заметка", Body: "новое",
	}); err != nil {
		t.Fatalf("добавить источник: %v", err)
	}
	r.RunOnce(ctx)

	third, err := s.store.LatestSlice(ctx, manual.TaskID)
	if err != nil {
		t.Fatalf("срез после третьего прохода: %v", err)
	}
	if third.Version != first.Version+1 {
		t.Errorf("версия %d, ожидалась %d", third.Version, first.Version+1)
	}
}

// TestRunOnceKeepsGoing: ошибка на одной задаче не останавливает остальные.
// Ночной проход обязан дойти до конца — отказ модели на одной задаче не повод
// оставить без свежего среза все прочие.
func TestRunOnceKeepsGoing(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()

	// Вторая задача без единого источника: разбор её не осилит.
	if _, err := s.CreateTask(ctx, domain.Task{ID: "пустая", Title: "Без источников"}); err != nil {
		t.Fatalf("создать задачу: %v", err)
	}

	NewRebuilder(s, 20, 0, time.UTC, 0).RunOnce(ctx)

	// Задача с материалом собралась, несмотря на соседку.
	if _, err := s.store.LatestSlice(ctx, manual.TaskID); err != nil {
		t.Errorf("срез задачи с материалом не собран: %v", err)
	}
}

// TestRunOnceHonoursLimit: предел разборов за ночь. Разбор платный, и ночь,
// когда материал появился разом у всех задач, без предела списала бы весь
// остаток — а узнали бы об этом утром.
func TestRunOnceHonoursLimit(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()

	// Три задачи с материалом: столько же разборов и потребовалось бы.
	for _, id := range []string{"вторая", "третья"} {
		if _, err := s.CreateTask(ctx, domain.Task{ID: id, Title: id}); err != nil {
			t.Fatalf("создать задачу %s: %v", id, err)
		}
		if _, err := s.AddSource(ctx, domain.Source{
			TaskID: id, Kind: domain.KindNote, Title: "Заметка", Body: "материал",
		}); err != nil {
			t.Fatalf("добавить источник в %s: %v", id, err)
		}
	}

	NewRebuilder(s, 20, 0, time.UTC, 1).RunOnce(ctx)

	// Собран ровно один срез: предел считается по разобранным задачам.
	built := 0
	for _, id := range []string{manual.TaskID, "вторая", "третья"} {
		if _, err := s.store.LatestSlice(ctx, id); err == nil {
			built++
		}
	}
	if built != 1 {
		t.Errorf("собрано срезов %d, предел разрешал 1", built)
	}

	// Следующая ночь доберёт остальное: предел откладывает разбор, а не
	// отменяет его.
	NewRebuilder(s, 20, 0, time.UTC, 1).RunOnce(ctx)

	built = 0
	for _, id := range []string{manual.TaskID, "вторая", "третья"} {
		if _, err := s.store.LatestSlice(ctx, id); err == nil {
			built++
		}
	}
	if built != 2 {
		t.Errorf("после второй ночи собрано %d, ожидали 2", built)
	}
}

// TestRunOnceStopsOnCancel: остановка сервера не должна ждать, пока модель
// разберёт весь реестр.
func TestRunOnceStopsOnCancel(t *testing.T) {
	s := seeded(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	NewRebuilder(s, 20, 0, time.UTC, 0).RunOnce(ctx)

	if _, err := s.store.LatestSlice(context.Background(), manual.TaskID); err == nil {
		t.Error("отменённый проход всё равно собрал срез")
	}
}
