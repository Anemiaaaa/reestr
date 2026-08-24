package jsonstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// open поднимает пустое хранилище в каталоге теста и отдаёт путь к файлу:
// половина проверок ниже про то, что лежит на диске, а не в памяти.
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

func task(id string, opened time.Time) domain.Task {
	return domain.Task{ID: id, Title: "задача " + id, OpenedAt: opened}
}

// TestOpenEmpty: на пустом месте хранилище открывается, но файла не создаёт.
// Это важно для первого запуска: сервис решает, сеять ли данные, по Empty, и
// пустой файл с нулями обманул бы его.
func TestOpenEmpty(t *testing.T) {
	st, path := open(t)
	ctx := context.Background()

	if !st.Empty() {
		t.Error("новое хранилище не считает себя пустым")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("файл появился до первой записи: %v", err)
	}

	list, err := st.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("в пустом хранилище %d задач", len(list))
	}

	if err := st.CreateTask(ctx, task("t1", utc(2026, time.August, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if st.Empty() {
		t.Error("хранилище с задачей считает себя пустым")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("файл не появился после записи: %v", err)
	}
}

func TestCreateTask(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()

	want := domain.Task{
		ID:       "aura",
		Project:  "АУРА",
		Title:    "Внедрение автоматизации",
		Author:   "Ризван Мирзаев",
		Assignee: "Мухаммад Минатулаев",
		Deadline: utc(2026, time.August, 31),
		Budget:   820000,
	}
	if err := st.CreateTask(ctx, want); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := st.Task(ctx, "aura")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if got.Title != want.Title || got.Author != want.Author || got.Budget != want.Budget {
		t.Errorf("задача вернулась искажённой: %+v", got)
	}
	if !got.Deadline.Equal(want.Deadline) {
		t.Errorf("срок = %v, хотели %v", got.Deadline, want.Deadline)
	}

	// Занятый идентификатор — не молчаливая перезапись. Хранилище только
	// добавляет, и повторная сборка не должна затирать прежнюю задачу.
	if err := st.CreateTask(ctx, domain.Task{ID: "aura", Title: "другая"}); !errors.Is(err, store.ErrExists) {
		t.Errorf("повторный идентификатор: ошибка %v, хотели ErrExists", err)
	}
	if again, _ := st.Task(ctx, "aura"); again.Title != want.Title {
		t.Errorf("название затёрлось: %q", again.Title)
	}

	if _, err := st.Task(ctx, "нет такой"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("неизвестная задача: ошибка %v, хотели ErrNotFound", err)
	}
}

// TestTasksOrderAndCopy проверяет и порядок списка, и то, что список — копия.
// Копия здесь не мелочь: боковая колонка получает эти задачи и не должна иметь
// возможности изменить хранилище.
func TestTasksOrderAndCopy(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()

	for _, tc := range []struct {
		id     string
		opened time.Time
	}{
		{"средняя", utc(2026, time.July, 1)},
		{"старая", utc(2026, time.June, 1)},
		{"свежая", utc(2026, time.August, 1)},
	} {
		if err := st.CreateTask(ctx, task(tc.id, tc.opened)); err != nil {
			t.Fatalf("CreateTask %s: %v", tc.id, err)
		}
	}

	list, err := st.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	want := []string{"свежая", "средняя", "старая"}
	if len(list) != len(want) {
		t.Fatalf("задач %d, хотели %d", len(list), len(want))
	}
	for i, id := range want {
		if list[i].ID != id {
			t.Errorf("задача %d = %q, хотели %q", i, list[i].ID, id)
		}
	}

	list[0].Title = "испорчено"
	if got, _ := st.Task(ctx, "свежая"); got.Title == "испорчено" {
		t.Error("правка возвращённого списка дошла до хранилища")
	}
}

func TestSources(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()

	if err := st.CreateTask(ctx, task("aura", utc(2026, time.June, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := st.CreateTask(ctx, task("other", utc(2026, time.June, 2))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Источник к несуществующей задаче — ошибка, а не сирота в файле: ссылка из
	// среза на такой источник никуда бы не привела.
	orphan := domain.Source{ID: "s0", TaskID: "нет такой", Kind: domain.KindNote, Body: "текст"}
	if err := st.AddSource(ctx, orphan); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("источник к неизвестной задаче: ошибка %v, хотели ErrNotFound", err)
	}

	for _, src := range []domain.Source{
		{ID: "s1", TaskID: "aura", Kind: domain.KindCorrespondence, Title: "лента", Body: "первое"},
		{ID: "s2", TaskID: "other", Kind: domain.KindNote, Title: "чужой", Body: "не сюда"},
		{ID: "s3", TaskID: "aura", Kind: domain.KindAudit, Title: "аудит", Body: "второе"},
	} {
		if err := st.AddSource(ctx, src); err != nil {
			t.Fatalf("AddSource %s: %v", src.ID, err)
		}
	}

	got, err := st.Sources(ctx, "aura")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("источников %d, хотели 2: %+v", len(got), got)
	}
	// Порядок загрузки, а не сортировка: журнал источников читают как хронику.
	if got[0].ID != "s1" || got[1].ID != "s3" {
		t.Errorf("порядок источников: %q, %q", got[0].ID, got[1].ID)
	}

	one, err := st.Source(ctx, "s3")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if one.Title != "аудит" || one.TaskID != "aura" {
		t.Errorf("источник вернулся искажённым: %+v", one)
	}
	if _, err := st.Source(ctx, "s404"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("неизвестный источник: ошибка %v, хотели ErrNotFound", err)
	}
}

func TestFacts(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()

	if err := st.CreateTask(ctx, task("aura", utc(2026, time.June, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if err := st.AddFacts(ctx, nil); err != nil {
		t.Fatalf("пустой список фактов: %v", err)
	}

	first := []domain.Fact{
		{ID: "f1", TaskID: "aura", Field: "passport.author", Value: domain.Quoted("Ризван", "s1", "цитата")},
		{ID: "f2", TaskID: "other", Field: "passport.author", Value: domain.Quoted("чужой", "s2", "цитата")},
	}
	if err := st.AddFacts(ctx, first); err != nil {
		t.Fatalf("AddFacts: %v", err)
	}
	second := []domain.Fact{
		{ID: "f3", TaskID: "aura", Field: "passport.author", Value: domain.Quoted("Мухаммад", "s3", "цитата")},
	}
	if err := st.AddFacts(ctx, second); err != nil {
		t.Fatalf("AddFacts: %v", err)
	}

	got, err := st.Facts(ctx, "aura")
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	// Тот же field дважды — это две записи, а не замена. Срез берёт последнюю,
	// но обе остаются в журнале: иначе не видно, что утверждение поменялось.
	if len(got) != 2 {
		t.Fatalf("фактов %d, хотели 2: %+v", len(got), got)
	}
	if got[0].ID != "f1" || got[1].ID != "f3" {
		t.Errorf("порядок фактов: %q, %q", got[0].ID, got[1].ID)
	}
	if got[1].Value.Text != "Мухаммад" {
		t.Errorf("поздний факт = %q, хотели «Мухаммад»", got[1].Value.Text)
	}
}

func TestProcesses(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()

	if err := st.CreateTask(ctx, task("aura", utc(2026, time.June, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	asIs := domain.Process{
		TaskID: "aura",
		Kind:   domain.ProcessAsIs,
		Title:  "как есть",
		Nodes:  []domain.Node{{ID: "a1", Title: "заявка"}},
	}
	toBe := domain.Process{
		TaskID: "aura",
		Kind:   domain.ProcessToBe,
		Title:  "как будет",
		Nodes:  []domain.Node{{ID: "b1", Title: "заявка в системе"}},
	}
	for _, p := range []domain.Process{toBe, asIs} {
		if err := st.PutProcess(ctx, p); err != nil {
			t.Fatalf("PutProcess %s: %v", p.Kind, err)
		}
	}

	got, err := st.Processes(ctx, "aura")
	if err != nil {
		t.Fatalf("Processes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("схем %d, хотели 2", len(got))
	}
	// «Как есть» первой независимо от порядка сохранения: схемы читают слева
	// направо, от текущего процесса к предлагаемому.
	if got[0].Kind != domain.ProcessAsIs || got[1].Kind != domain.ProcessToBe {
		t.Errorf("порядок схем: %s, %s", got[0].Kind, got[1].Kind)
	}

	// Пересборка заменяет схему того же вида, а не добавляет вторую: схема —
	// отображение фактов, и двух актуальных «как есть» быть не может.
	again := asIs
	again.Title = "как есть, версия 2"
	again.Nodes = []domain.Node{{ID: "a1", Title: "заявка"}, {ID: "a2", Title: "проверка"}}
	if err := st.PutProcess(ctx, again); err != nil {
		t.Fatalf("PutProcess повторно: %v", err)
	}

	got, err = st.Processes(ctx, "aura")
	if err != nil {
		t.Fatalf("Processes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("после пересборки схем %d, хотели 2", len(got))
	}
	if got[0].Title != "как есть, версия 2" || len(got[0].Nodes) != 2 {
		t.Errorf("схема не заменилась: %+v", got[0])
	}
}

func TestSlices(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()

	if err := st.CreateTask(ctx, task("aura", utc(2026, time.June, 1))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if _, err := st.LatestSlice(ctx, "aura"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("среза ещё нет: ошибка %v, хотели ErrNotFound", err)
	}
	if v, err := st.NextSliceVersion(ctx, "aura"); err != nil || v != 1 {
		t.Errorf("первая версия = %d (%v), хотели 1", v, err)
	}

	for _, v := range []int{1, 2, 3} {
		sl := domain.Slice{TaskID: "aura", Version: v, BuiltAt: utc(2026, time.August, v)}
		sl.Status.Readiness = domain.Computed("27 %", "2,4 из 9 этапов закрыто")
		if err := st.SaveSlice(ctx, sl); err != nil {
			t.Fatalf("SaveSlice %d: %v", v, err)
		}
	}
	// Срез чужой задачи с большим номером не должен подменять последнюю версию.
	if err := st.CreateTask(ctx, task("other", utc(2026, time.June, 2))); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := st.SaveSlice(ctx, domain.Slice{TaskID: "other", Version: 99}); err != nil {
		t.Fatalf("SaveSlice чужой: %v", err)
	}

	last, err := st.LatestSlice(ctx, "aura")
	if err != nil {
		t.Fatalf("LatestSlice: %v", err)
	}
	if last.Version != 3 {
		t.Errorf("последняя версия = %d, хотели 3", last.Version)
	}
	if last.Status.Readiness.Text != "27 %" {
		t.Errorf("готовность = %q", last.Status.Readiness.Text)
	}
	if v, err := st.NextSliceVersion(ctx, "aura"); err != nil || v != 4 {
		t.Errorf("следующая версия = %d (%v), хотели 4", v, err)
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
	if again.Empty() {
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
