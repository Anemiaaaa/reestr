package jsonstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
	"github.com/Anemiaaaa/reestr/internal/store/storetest"
)

// TestConformance прогоняет общий контракт хранилищ. Всё, что описано
// интерфейсом, проверяется там, а здесь остаётся только то, чего у хранилища в
// базе нет: файл на диске, его состояние до первой записи и переживание
// перезапуска.
//
// До появления общего набора у каждого хранилища был свой, и оба свои проверки
// проходили. Расхождений между ними от этого не становилось меньше — их просто
// было нечем увидеть.
func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		t.Helper()

		st, _ := open(t)
		return st
	})
}

// open поднимает пустое хранилище в каталоге теста и отдаёт путь к файлу:
// проверки ниже про то, что лежит на диске, а не в памяти.
func open(t *testing.T) (*Store, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "data", "reestr.json")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st, path
}

func utc(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// empty спрашивает у хранилища, пусто ли оно, и роняет тест на ошибке. Файловое
// хранилище её не возвращает, но она есть в сигнатуре ради хранилища в базе, и
// проверять её в каждом месте — только загромождать проверку.
func empty(t *testing.T, st *Store) bool {
	t.Helper()

	yes, err := st.Empty(context.Background())
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	return yes
}

func task(id string, opened time.Time) domain.Task {
	return domain.Task{ID: id, Title: "задача " + id, OpenedAt: opened}
}

// TestOpenEmpty: на пустом месте хранилище открывается, но файла не создаёт.
// Это важно для первого запуска: сервис решает, сеять ли данные, по Empty, и
// пустой файл с нулями обманул бы его.
func TestOpenEmpty(t *testing.T) {
	st, path := open(t)
	ctx := context.Background()

	if !empty(t, st) {
		t.Error("новое хранилище не считает себя пустым")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("файл появился до первой записи: %v", err)
	}

	if err := st.CreateTask(ctx, task("t1", utc(2026, time.August, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if empty(t, st) {
		t.Error("хранилище с задачей считает себя пустым")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("файл не появился после записи: %v", err)
	}
}

// TestOpenBroken: испорченный файл — ошибка, а не пустой реестр.
//
// Разница здесь не в аккуратности, а в потере данных. Сервис наполняет примером
// хранилище, которое считает себя пустым. Если разбор битого файла молча даст
// пустое состояние, первая же запись перезапишет файл — и вместо разбора
// поломки получится демонстрационный набор поверх работы.
func TestOpenBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reestr.json")
	if err := os.WriteFile(path, []byte(`{"tasks": [оборвано`), 0o644); err != nil {
		t.Fatalf("подготовка файла: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Error("испорченный файл открылся без ошибки")
	}
}

// TestTasksAreCopy: возвращённый список — копия, а не окно в хранилище.
// Боковая колонка получает эти задачи и не должна иметь возможности изменить
// реестр, ничего для этого не делая.
func TestTasksAreCopy(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()

	if err := st.CreateTask(ctx, task("aura", utc(2026, time.August, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	list, err := st.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("задач %d, хотели 1", len(list))
	}

	list[0].Title = "испорчено"
	if got, _ := st.Task(ctx, "aura"); got.Title == "испорчено" {
		t.Error("правка возвращённого списка дошла до хранилища")
	}
}

// TestReopen: всё, что записано, читается новым экземпляром из того же файла.
// Ради этого хранилище и файловое — данные должны переживать перезапуск
// сервиса, и их должно быть видно текстовым редактором.
func TestReopen(t *testing.T) {
	st, path := open(t)
	ctx := context.Background()

	if err := st.CreateTask(ctx, task("aura", utc(2026, time.June, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	src := domain.Source{
		ID: "s1", TaskID: "aura", Kind: domain.KindCorrespondence,
		Title: "лента задачи", Body: "срок — до 31 августа",
		UploadedAt: utc(2026, time.August, 20),
	}
	if err := st.AddSource(ctx, src); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	fact := domain.Fact{
		ID: "f1", TaskID: "aura", Field: "passport.deadline",
		Value:      domain.Quoted("31.08.2026", "s1", "срок — до 31 августа"),
		Confidence: 0.9,
		CreatedAt:  utc(2026, time.August, 20),
	}
	if err := st.AddFacts(ctx, []domain.Fact{fact}); err != nil {
		t.Fatalf("AddFacts: %v", err)
	}
	if err := st.PutProcess(ctx, domain.Process{TaskID: "aura", Kind: domain.ProcessAsIs, Title: "как есть"}); err != nil {
		t.Fatalf("PutProcess: %v", err)
	}
	if err := st.SaveSlice(ctx, domain.Slice{TaskID: "aura", Version: 1}); err != nil {
		t.Fatalf("SaveSlice: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}
	if empty(t, again) {
		t.Fatal("после перезапуска хранилище пусто")
	}
	if _, err := again.Task(ctx, "aura"); err != nil {
		t.Errorf("задача не прочиталась: %v", err)
	}

	srcs, err := again.Sources(ctx, "aura")
	if err != nil || len(srcs) != 1 {
		t.Fatalf("источников %d (%v), хотели 1", len(srcs), err)
	}
	if srcs[0].Body != src.Body {
		t.Errorf("тело источника = %q, хотели %q", srcs[0].Body, src.Body)
	}
	if !srcs[0].UploadedAt.Equal(src.UploadedAt) {
		t.Errorf("дата загрузки = %v, хотели %v", srcs[0].UploadedAt, src.UploadedAt)
	}

	facts, err := again.Facts(ctx, "aura")
	if err != nil || len(facts) != 1 {
		t.Fatalf("фактов %d (%v), хотели 1", len(facts), err)
	}
	// Цитата и ссылка на источник — единственное основание доверять срезу.
	// Если они не переживают перезапуск, срез после него врёт.
	if facts[0].Value.Quote != fact.Value.Quote || facts[0].Value.SourceID != "s1" {
		t.Errorf("происхождение факта потерялось: %+v", facts[0].Value)
	}
	if facts[0].Value.Origin != domain.OriginQuoted {
		t.Errorf("происхождение = %q, хотели %q", facts[0].Value.Origin, domain.OriginQuoted)
	}
	if facts[0].Confidence != 0.9 {
		t.Errorf("уверенность = %v, хотели 0.9", facts[0].Confidence)
	}

	procs, err := again.Processes(ctx, "aura")
	if err != nil || len(procs) != 1 {
		t.Fatalf("схем %d (%v), хотели 1", len(procs), err)
	}
	if v, err := again.NextSliceVersion(ctx, "aura"); err != nil || v != 2 {
		t.Errorf("следующая версия после перезапуска = %d (%v), хотели 2", v, err)
	}
}
