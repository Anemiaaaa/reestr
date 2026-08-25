// Пакет storetest — общая проверка для всех реализаций store.Store.
//
// Существует потому, что взаимозаменяемость хранилищ обещана вслух: сервис
// работает с интерфейсом и не должен уметь отличить файл от базы. Такое
// обещание нельзя оставлять комментарием в пакете — оно проверяется или не
// выполняется. Восемь расхождений между файловым хранилищем и базой нашлись
// именно здесь, и все восемь были невидимы, пока у каждого бэкенда был свой
// набор тестов: оба свои проверки проходили.
//
// Проверяется поведение, видимое сервису: порядок выдачи, вид ошибки, судьба
// пустых значений. Внутреннее устройство — файл на диске, пул соединений — дело
// самих реализаций и проверяется рядом с ними.
//
// Импорт testing в не тестовом пакете здесь уместен по той же причине, по
// которой он уместен в net/http/httptest: пакет и существует ради тестов, а
// зависимость от него — обычная зависимость.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// New открывает пустое хранилище для одной проверки. Каждый вызов обязан
// отдавать чистое состояние: проверки не рассчитаны на чужие данные и не
// убирают за собой.
type New func(t *testing.T) store.Store

// Run прогоняет весь общий контракт. Подпроверки идут последовательно
// намеренно: за реализацией в базе стоит одна схема на весь прогон, и
// параллельные подпроверки мешали бы друг другу.
func Run(t *testing.T, open New) {
	t.Helper()

	for _, tc := range []struct {
		name string
		run  func(t *testing.T, st store.Store)
	}{
		{"Empty", testEmpty},
		{"Task", testTask},
		{"TasksOrder", testTasksOrder},
		{"Source", testSource},
		{"SourcesOrder", testSourcesOrder},
		{"SharedSource", testSharedSource},
		{"SourceByExternal", testSourceByExternal},
		{"Facts", testFacts},
		{"Processes", testProcesses},
		{"Slices", testSlices},
		{"SlicesUsing", testSlicesUsing},
		{"DuplicateSource", testDuplicateSource},
		{"ZeroDates", testZeroDates},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, open(t)) })
	}
}

// --- проверки ---

// testEmpty: по этому признаку сервис решает, наполнять ли реестр примером.
// Ошибка в обе стороны заметна сразу: непустой реестр затрут демонстрационными
// данными либо пустой останется пустым и открывать будет нечего.
func testEmpty(t *testing.T, st store.Store) {
	ctx := context.Background()

	if !empty(t, st) {
		t.Error("новое хранилище не считает себя пустым")
	}

	list, err := st.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("в пустом хранилище %d задач", len(list))
	}

	create(t, st, task("t1", utc(2026, time.August, 1)))
	if empty(t, st) {
		t.Error("хранилище с задачей считает себя пустым")
	}
}

func testTask(t *testing.T, st store.Store) {
	ctx := context.Background()

	want := domain.Task{
		ID:       "aura",
		Project:  "АУРА",
		Title:    "Внедрение автоматизации",
		Author:   "Ризван Мирзаев",
		Assignee: "Мухаммад Минатулаев",
		OpenedAt: utc(2026, time.June, 10),
		Deadline: utc(2026, time.August, 31),
		Budget:   820000,
	}
	create(t, st, want)

	got, err := st.Task(ctx, want.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	switch {
	case got.Project != want.Project, got.Title != want.Title:
		t.Errorf("название или проект искажены: %+v", got)
	case got.Author != want.Author, got.Assignee != want.Assignee:
		t.Errorf("участники искажены: %+v", got)
	case got.Budget != want.Budget:
		t.Errorf("смета = %d, хотели %d", got.Budget, want.Budget)
	}
	// Даты сравниваются моментом, а не значением: база отдаёт время в своём
	// часовом поясе, и это то же самое время.
	if !got.OpenedAt.Equal(want.OpenedAt) {
		t.Errorf("дата постановки = %v, хотели %v", got.OpenedAt, want.OpenedAt)
	}
	if !got.Deadline.Equal(want.Deadline) {
		t.Errorf("срок = %v, хотели %v", got.Deadline, want.Deadline)
	}

	// Занятый идентификатор — отказ, а не молчаливая перезапись. Хранилище
	// только добавляет, и повторная сборка не должна затирать прежнюю задачу.
	err = st.CreateTask(ctx, domain.Task{ID: want.ID, Title: "другая"})
	if !errors.Is(err, store.ErrExists) {
		t.Errorf("повторный идентификатор: ошибка %v, хотели ErrExists", err)
	}
	if again, _ := st.Task(ctx, want.ID); again.Title != want.Title {
		t.Errorf("название затёрлось: %q", again.Title)
	}

	if _, err := st.Task(ctx, "нет такой"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("неизвестная задача: ошибка %v, хотели ErrNotFound", err)
	}
}

// testTasksOrder: свежими сверху, при равных датах — в порядке появления.
// Устойчивость важна не меньше самого порядка: список задач не должен
// перетасовываться сам по себе между двумя обновлениями страницы.
func testTasksOrder(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("средняя", utc(2026, time.July, 1)))
	create(t, st, task("без даты", time.Time{}))
	create(t, st, task("старая", utc(2026, time.June, 1)))
	create(t, st, task("свежая", utc(2026, time.August, 1)))
	create(t, st, task("тоже свежая", utc(2026, time.August, 1)))

	list, err := st.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	// Задача без даты постановки уходит в конец: пустая дата — это «неизвестно»,
	// и ставить такую задачу впереди свежих значило бы читать её как древнюю.
	want := []string{"свежая", "тоже свежая", "средняя", "старая", "без даты"}
	if len(list) != len(want) {
		t.Fatalf("задач %d, хотели %d", len(list), len(want))
	}
	for i, id := range want {
		if list[i].ID != id {
			t.Errorf("задача %d = %q, хотели %q", i, list[i].ID, id)
		}
	}
}

func testSource(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))
	create(t, st, task("other", utc(2026, time.June, 2)))

	// Источник к несуществующей задаче — ошибка, а не сирота в хранилище:
	// ссылка из среза на такой источник никуда бы не привела.
	orphan := domain.Source{ID: "s0", TaskID: "нет такой", Kind: domain.KindNote, Body: "текст"}
	if err := st.AddSource(ctx, orphan); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("источник к неизвестной задаче: ошибка %v, хотели ErrNotFound", err)
	}

	want := domain.Source{
		ID: "s1", TaskID: "aura", ParentID: "km-2026-08-20",
		Kind: domain.KindBridge, Title: "мостик, 20 августа",
		Body: "по АУРА закрыли интеграцию с 1С", Author: "Ризван Мирзаев",
		OccurredAt: utc(2026, time.August, 20),
		UploadedAt: utc(2026, time.August, 21),
	}
	add(t, st, want)
	add(t, st, domain.Source{ID: "s2", TaskID: "other", Kind: domain.KindNote, Body: "не сюда"})

	got, err := st.Source(ctx, want.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	switch {
	case got.TaskID != want.TaskID, got.ParentID != want.ParentID:
		t.Errorf("привязки искажены: %+v", got)
	case got.Kind != want.Kind, got.Title != want.Title:
		t.Errorf("вид или заголовок искажены: %+v", got)
	case got.Body != want.Body, got.Author != want.Author:
		t.Errorf("текст или автор искажены: %+v", got)
	}
	// Дата события и дата загрузки — разные вещи: первая говорит, когда это
	// произошло, вторая — когда попало в реестр. Срез опирается на первую.
	if !got.OccurredAt.Equal(want.OccurredAt) {
		t.Errorf("дата события = %v, хотели %v", got.OccurredAt, want.OccurredAt)
	}
	if !got.UploadedAt.Equal(want.UploadedAt) {
		t.Errorf("дата загрузки = %v, хотели %v", got.UploadedAt, want.UploadedAt)
	}

	list, err := st.Sources(ctx, "aura")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(list) != 1 || list[0].ID != want.ID {
		t.Errorf("источники задачи: %v", ids(list))
	}

	if _, err := st.Source(ctx, "s404"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("неизвестный источник: ошибка %v, хотели ErrNotFound", err)
	}
}

// testSourcesOrder: журнал источников читают как хронику, поэтому порядок —
// по дате загрузки, а при равных датах — по очереди поступления.
//
// Одной даты загрузки мало: подтяжка чата приносит десяток сообщений одним
// вызовом, и без второго признака они выстроятся как попало. Сортировка обязана
// быть устойчивой, иначе список источников будет меняться сам по себе.
func testSourcesOrder(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))

	for _, src := range []domain.Source{
		{ID: "третий", TaskID: "aura", Kind: domain.KindCorrespondence, UploadedAt: utc(2026, time.August, 20)},
		{ID: "первый", TaskID: "aura", Kind: domain.KindNote},
		{ID: "второй", TaskID: "aura", Kind: domain.KindCorrespondence, UploadedAt: utc(2026, time.August, 10)},
		{ID: "четвёртый", TaskID: "aura", Kind: domain.KindCorrespondence, UploadedAt: utc(2026, time.August, 20)},
	} {
		add(t, st, src)
	}

	list, err := st.Sources(ctx, "aura")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	// Источник без даты загрузки идёт первым: пустая дата означает, что материал
	// лежал в реестре до того, как даты начали проставлять.
	want := []string{"первый", "второй", "третий", "четвёртый"}
	if got := ids(list); !same(got, want) {
		t.Errorf("порядок источников: %v, хотели %v", got, want)
	}
}

// testSharedSource: у сводки с капитанского мостика задачи нет — она говорит
// сразу о нескольких. Это тот самый шов, ради которого TaskID необязателен.
func testSharedSource(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))

	shared := domain.Source{
		ID: "km-2026-08-20", Kind: domain.KindBridge,
		Title: "капитанский мостик, 20 августа",
		Body:  "АУРА: интеграция закрыта. Другой проект: ждём доступы.",
	}
	add(t, st, shared)

	// Фрагмент, вырезанный из сводки, привязан и к задаче, и к сводке. Завтра
	// его будет вырезать разбор, а не PM, — записи получатся одинаковые.
	add(t, st, domain.Source{
		ID: "s1", TaskID: "aura", ParentID: shared.ID,
		Kind: domain.KindBridge, Body: "АУРА: интеграция закрыта",
	})

	// Общий источник не попадает в источники задачи: в задаче участвует
	// фрагмент, а не сводка целиком.
	list, err := st.Sources(ctx, "aura")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if got := ids(list); !same(got, []string{"s1"}) {
		t.Errorf("источники задачи: %v, хотели [s1]", got)
	}

	// Но по идентификатору он читается: ссылка ParentID должна куда-то вести,
	// иначе фрагмент нельзя показать в контексте.
	got, err := st.Source(ctx, shared.ID)
	if err != nil {
		t.Fatalf("Source общего источника: %v", err)
	}
	if got.TaskID != "" {
		t.Errorf("у общего источника появилась задача %q", got.TaskID)
	}
	if got.Body != shared.Body {
		t.Errorf("текст сводки искажён: %q", got.Body)
	}
}

// testSourceByExternal: по этому методу подтяжка чата отличает новое сообщение
// от уже загруженного. Без него повторный запуск синхронизации завёл бы второй
// экземпляр каждого сообщения, и в срезе одна фраза стала бы двумя фактами.
func testSourceByExternal(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))

	ref := domain.ExternalRef{System: "bitrix24", ChatID: "8", MessageID: "42"}
	add(t, st, domain.Source{
		ID: "s1", TaskID: "aura", Kind: domain.KindCorrespondence,
		Body: "смета согласована", External: ref,
	})

	got, err := st.SourceByExternal(ctx, ref.Key())
	if err != nil {
		t.Fatalf("SourceByExternal: %v", err)
	}
	if got.ID != "s1" {
		t.Errorf("нашёлся источник %q, хотели s1", got.ID)
	}
	if got.External != ref {
		t.Errorf("внешний адрес искажён: %+v", got.External)
	}

	// Тот же адрес оригинала под другим идентификатором — отказ. Это ожидаемый
	// исход повторной подтяжки, а не сбой, поэтому ошибка именно ErrExists.
	dup := domain.Source{ID: "s2", TaskID: "aura", Kind: domain.KindCorrespondence, External: ref}
	if err := st.AddSource(ctx, dup); !errors.Is(err, store.ErrExists) {
		t.Errorf("повторный внешний адрес: ошибка %v, хотели ErrExists", err)
	}

	// А вот материал без внешнего адреса — вставленный руками — не конфликтует
	// ни с чем: у него ключа нет вообще, и пустые ключи не считаются дублями.
	add(t, st, domain.Source{ID: "s3", TaskID: "aura", Kind: domain.KindNote, Body: "пояснение"})
	add(t, st, domain.Source{ID: "s4", TaskID: "aura", Kind: domain.KindNote, Body: "ещё одно"})

	if _, err := st.SourceByExternal(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("пустой адрес: ошибка %v, хотели ErrNotFound", err)
	}
	if _, err := st.SourceByExternal(ctx, "bitrix24:8:999"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("неизвестный адрес: ошибка %v, хотели ErrNotFound", err)
	}
}

func testFacts(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))
	create(t, st, task("other", utc(2026, time.June, 2)))

	// Разбор, не давший ни одного факта, — законный исход, а не ошибка.
	if err := st.AddFacts(ctx, nil); err != nil {
		t.Fatalf("пустой список фактов: %v", err)
	}

	// Факт к несуществующей задаче не должен оседать в хранилище: он никогда не
	// попадёт ни в один срез и останется мусором, который никто не найдёт.
	orphan := []domain.Fact{{ID: "f0", TaskID: "нет такой", Field: "passport.author",
		Value: domain.Quoted("никто", "s1", "цитата")}}
	if err := st.AddFacts(ctx, orphan); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("факт к неизвестной задаче: ошибка %v, хотели ErrNotFound", err)
	}

	want := domain.Fact{
		ID: "f1", TaskID: "aura", Field: "passport.deadline",
		Value:      domain.Quoted("31.08.2026", "s1", "срок — до 31 августа"),
		Confidence: 0.9,
		ObservedAt: utc(2026, time.August, 20),
		CreatedAt:  utc(2026, time.August, 21),
	}
	if err := st.AddFacts(ctx, []domain.Fact{
		want,
		{ID: "f2", TaskID: "other", Field: "passport.deadline", Value: domain.Missing("не назван")},
	}); err != nil {
		t.Fatalf("AddFacts: %v", err)
	}
	// Второй разбор ложится сверху, а не заменяет первый: журнал обязан помнить,
	// что утверждение менялось, иначе не объяснить расхождение двух срезов.
	if err := st.AddFacts(ctx, []domain.Fact{
		{ID: "f3", TaskID: "aura", Field: "passport.deadline",
			Value: domain.Quoted("15.09.2026", "s5", "сдвинули на 15 сентября")},
	}); err != nil {
		t.Fatalf("AddFacts повторно: %v", err)
	}

	got, err := st.Facts(ctx, "aura")
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("фактов %d, хотели 2: %v", len(got), got)
	}
	if got[0].ID != "f1" || got[1].ID != "f3" {
		t.Errorf("порядок фактов: %q, %q", got[0].ID, got[1].ID)
	}
	if got[1].Value.Text != "15.09.2026" {
		t.Errorf("поздний факт = %q, хотели 15.09.2026", got[1].Value.Text)
	}

	// Цитата и ссылка на источник — единственное основание доверять срезу. Если
	// они не переживают запись, срез перестаёт быть проверяемым.
	first := got[0]
	switch {
	case first.Value.Origin != domain.OriginQuoted:
		t.Errorf("происхождение = %q, хотели %q", first.Value.Origin, domain.OriginQuoted)
	case first.Value.SourceID != want.Value.SourceID:
		t.Errorf("ссылка на источник = %q, хотели %q", first.Value.SourceID, want.Value.SourceID)
	case first.Value.Quote != want.Value.Quote:
		t.Errorf("цитата = %q, хотели %q", first.Value.Quote, want.Value.Quote)
	case first.Confidence != want.Confidence:
		t.Errorf("уверенность = %v, хотели %v", first.Confidence, want.Confidence)
	}
	if !first.ObservedAt.Equal(want.ObservedAt) {
		t.Errorf("дата события = %v, хотели %v", first.ObservedAt, want.ObservedAt)
	}
	if !first.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("дата извлечения = %v, хотели %v", first.CreatedAt, want.CreatedAt)
	}
}

func testProcesses(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))

	if err := st.PutProcess(ctx, domain.Process{TaskID: "нет такой", Kind: domain.ProcessAsIs}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("схема к неизвестной задаче: ошибка %v, хотели ErrNotFound", err)
	}

	asIs := domain.Process{
		TaskID: "aura", Kind: domain.ProcessAsIs, Title: "как есть",
		Nodes: []domain.Node{{ID: "a1", Title: "заявка в WhatsApp"}},
	}
	toBe := domain.Process{
		TaskID: "aura", Kind: domain.ProcessToBe, Title: "как будет",
		Nodes: []domain.Node{{ID: "b1", Title: "заявка в системе"}},
	}
	// Порядок сохранения обратный порядку выдачи: он не должен ни на что влиять.
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
	// «Как есть» первой: сравнение читают слева направо, от текущего процесса к
	// предлагаемому, и обратный порядок вывел бы результат раньше причины.
	if got[0].Kind != domain.ProcessAsIs || got[1].Kind != domain.ProcessToBe {
		t.Fatalf("порядок схем: %s, %s", got[0].Kind, got[1].Kind)
	}
	if got[0].Title != asIs.Title || len(got[0].Nodes) != 1 {
		t.Errorf("схема искажена: %+v", got[0])
	}
	if got[0].Nodes[0].Title != asIs.Nodes[0].Title {
		t.Errorf("узел искажён: %+v", got[0].Nodes[0])
	}

	// Пересборка заменяет схему того же вида, а не добавляет вторую: схема —
	// отображение фактов, и двух актуальных «как есть» быть не может.
	again := asIs
	again.Title = "как есть, версия 2"
	again.Nodes = append(again.Nodes, domain.Node{ID: "a2", Title: "звонок мастеру"})
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
	if got[0].Title != again.Title || len(got[0].Nodes) != 2 {
		t.Errorf("схема не заменилась: %+v", got[0])
	}
}

func testSlices(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))

	if _, err := st.LatestSlice(ctx, "aura"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("среза ещё нет: ошибка %v, хотели ErrNotFound", err)
	}
	if v, err := st.NextSliceVersion(ctx, "aura"); err != nil || v != 1 {
		t.Errorf("первая версия = %d (%v), хотели 1", v, err)
	}

	err := st.SaveSlice(ctx, domain.Slice{TaskID: "нет такой", Version: 1})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("срез неизвестной задачи: ошибка %v, хотели ErrNotFound", err)
	}

	for _, v := range []int{1, 2, 3} {
		sl := domain.Slice{
			TaskID: "aura", Version: v, BuiltAt: utc(2026, time.August, 20+v),
			Analyst: "manual", Considered: 100,
			SourceIDs: []string{"s1", "s7"},
		}
		sl.Status.Stage = domain.Quoted("этап 4", "s1", "идёт четвёртый этап")
		sl.Status.Readiness = domain.Computed("27 %", "2,4 из 9 этапов закрыто")
		if err := st.SaveSlice(ctx, sl); err != nil {
			t.Fatalf("SaveSlice %d: %v", v, err)
		}
	}

	// Срез чужой задачи с большим номером не должен подменять последнюю версию.
	create(t, st, task("other", utc(2026, time.June, 2)))
	if err := st.SaveSlice(ctx, domain.Slice{TaskID: "other", Version: 99}); err != nil {
		t.Fatalf("SaveSlice чужой задачи: %v", err)
	}

	last, err := st.LatestSlice(ctx, "aura")
	if err != nil {
		t.Fatalf("LatestSlice: %v", err)
	}
	switch {
	case last.Version != 3:
		t.Errorf("последняя версия = %d, хотели 3", last.Version)
	case last.Analyst != "manual", last.Considered != 100:
		t.Errorf("сборка искажена: аналитик %q, подано %d", last.Analyst, last.Considered)
	case last.Status.Readiness.Text != "27 %":
		t.Errorf("готовность = %q, хотели «27 %%»", last.Status.Readiness.Text)
	case last.Status.Readiness.Origin != domain.OriginComputed:
		t.Errorf("происхождение готовности = %q", last.Status.Readiness.Origin)
	}
	if !last.BuiltAt.Equal(utc(2026, time.August, 23)) {
		t.Errorf("дата сборки = %v", last.BuiltAt)
	}
	if got := last.SourceIDs; !same(got, []string{"s1", "s7"}) {
		t.Errorf("использованные источники: %v", got)
	}

	if v, err := st.NextSliceVersion(ctx, "aura"); err != nil || v != 4 {
		t.Errorf("следующая версия = %d (%v), хотели 4", v, err)
	}

	// Собранная версия не переписывается: её читают как свидетельство о том, что
	// было известно на момент сборки, и подмена задним числом обессмыслила бы
	// сравнение «было → стало».
	err = st.SaveSlice(ctx, domain.Slice{TaskID: "aura", Version: 2, Analyst: "подмена"})
	if !errors.Is(err, store.ErrExists) {
		t.Errorf("повторная версия: ошибка %v, хотели ErrExists", err)
	}
	if again, _ := st.LatestSlice(ctx, "aura"); again.Version != 3 {
		t.Errorf("после отказа последняя версия = %d, хотели 3", again.Version)
	}
}

// testSlicesUsing отвечает на обратный вопрос: в каких версиях материал
// пригодился. Без него источник — просто текст в реестре, и непонятно, повлиял
// ли он на выводы.
func testSlicesUsing(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))
	create(t, st, task("beta", utc(2026, time.June, 2)))

	save := func(taskID string, version int, sources ...string) {
		t.Helper()

		sl := domain.Slice{
			TaskID: taskID, Version: version,
			BuiltAt:   utc(2026, time.August, 20+version),
			SourceIDs: sources,
		}
		if err := st.SaveSlice(ctx, sl); err != nil {
			t.Fatalf("SaveSlice %s/%d: %v", taskID, version, err)
		}
	}

	save("aura", 1, "s1", "s2")
	save("aura", 2, "s2")
	save("beta", 1, "s2")

	// От старых к свежим и с группировкой по задаче: список читают как историю.
	refs, err := st.SlicesUsing(ctx, "s2")
	if err != nil {
		t.Fatalf("SlicesUsing: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("версий %d, хотели 3: %+v", len(refs), refs)
	}
	want := []domain.SliceRef{
		{TaskID: "aura", Version: 1},
		{TaskID: "aura", Version: 2},
		{TaskID: "beta", Version: 1},
	}
	for i, w := range want {
		if refs[i].TaskID != w.TaskID || refs[i].Version != w.Version {
			t.Errorf("версия %d = %s/%d, хотели %s/%d", i,
				refs[i].TaskID, refs[i].Version, w.TaskID, w.Version)
		}
	}
	// Дата сборки в ссылке нужна затем, что список версий показывают датами.
	if !refs[0].BuiltAt.Equal(utc(2026, time.August, 21)) {
		t.Errorf("дата версии = %v, хотели 21 августа", refs[0].BuiltAt)
	}

	if refs, err := st.SlicesUsing(ctx, "s1"); err != nil || len(refs) != 1 {
		t.Errorf("источник одной версии: %d версий (%v), хотели 1", len(refs), err)
	}
	if refs, err := st.SlicesUsing(ctx, "s404"); err != nil || len(refs) != 0 {
		t.Errorf("неиспользованный источник: %d версий (%v), хотели 0", len(refs), err)
	}
	// Пустой идентификатор — не повод для ошибки: спрашивать не о чем.
	if refs, err := st.SlicesUsing(ctx, ""); err != nil || len(refs) != 0 {
		t.Errorf("пустой источник: %d версий (%v), хотели 0", len(refs), err)
	}
}

// testDuplicateSource: срез, дважды сославшийся на один материал, сохраняется.
//
// Это не выдуманный случай. Разбор упоминает источник в каждом разделе, где тот
// пригодился, и одно сообщение из чата легко попадает и в «статус», и в
// «блокирующие вопросы». Хранилище в базе держит указатели множеством, и пара
// «версия, источник» у него первичный ключ — то есть повтор ронял всю версию
// целиком, а файловое хранилище то же самое принимало молча.
//
// Потерять номер второго упоминания не жаль, а вот потерять из-за него готовую
// сборку — жаль: пересборка идёт ночью, и разбираться было бы некому.
func testDuplicateSource(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, task("aura", utc(2026, time.June, 1)))

	sl := domain.Slice{
		TaskID: "aura", Version: 1, BuiltAt: utc(2026, time.August, 21),
		SourceIDs: []string{"s1", "s2", "s1"},
	}
	if err := st.SaveSlice(ctx, sl); err != nil {
		t.Fatalf("срез с повторной ссылкой не сохранился: %v", err)
	}

	// Список источников — часть самого среза, и он сохраняется как подан: срез
	// самодостаточен, и переписывать его содержимое хранилище не вправе.
	got, err := st.LatestSlice(ctx, "aura")
	if err != nil {
		t.Fatalf("LatestSlice: %v", err)
	}
	if !same(got.SourceIDs, []string{"s1", "s2", "s1"}) {
		t.Errorf("источники среза: %v, хотели [s1 s2 s1]", got.SourceIDs)
	}

	// А вот обратный указатель — множество: версия в ответе на «где пригодился»
	// названа один раз, иначе история материала показала бы дубли.
	refs, err := st.SlicesUsing(ctx, "s1")
	if err != nil {
		t.Fatalf("SlicesUsing: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("версий по повторной ссылке %d, хотели 1: %+v", len(refs), refs)
	}
}

// testZeroDates: незаполненная дата обязана вернуться незаполненной.
//
// Это правило всей схемы, и оно не косметическое. «Дата не названа» и
// «01.01.0001» — разные утверждения: первое срез показывает пробелом и ставит
// вопрос специалисту, второе выглядит как заполненное поле и уводит расчёт
// сроков в минус на две тысячи лет.
func testZeroDates(t *testing.T, st store.Store) {
	ctx := context.Background()

	create(t, st, domain.Task{ID: "aura", Title: "без дат"})
	got, err := st.Task(ctx, "aura")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if !got.OpenedAt.IsZero() || !got.Deadline.IsZero() {
		t.Errorf("даты задачи не пусты: постановка %v, срок %v", got.OpenedAt, got.Deadline)
	}

	add(t, st, domain.Source{ID: "s1", TaskID: "aura", Kind: domain.KindNote, Body: "без дат"})
	src, err := st.Source(ctx, "s1")
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if !src.OccurredAt.IsZero() || !src.UploadedAt.IsZero() {
		t.Errorf("даты источника не пусты: событие %v, загрузка %v", src.OccurredAt, src.UploadedAt)
	}

	if err := st.AddFacts(ctx, []domain.Fact{
		{ID: "f1", TaskID: "aura", Field: "passport.deadline", Value: domain.Missing("срок не назван")},
	}); err != nil {
		t.Fatalf("AddFacts: %v", err)
	}
	facts, err := st.Facts(ctx, "aura")
	if err != nil || len(facts) != 1 {
		t.Fatalf("фактов %d (%v), хотели 1", len(facts), err)
	}
	if !facts[0].ObservedAt.IsZero() || !facts[0].CreatedAt.IsZero() {
		t.Errorf("даты факта не пусты: событие %v, извлечение %v",
			facts[0].ObservedAt, facts[0].CreatedAt)
	}
	// Пропуск обязан дойти до среза именно пропуском, с пояснением почему.
	if facts[0].Value.Origin != domain.OriginMissing {
		t.Errorf("происхождение = %q, хотели %q", facts[0].Value.Origin, domain.OriginMissing)
	}

	if err := st.SaveSlice(ctx, domain.Slice{TaskID: "aura", Version: 1}); err != nil {
		t.Fatalf("SaveSlice: %v", err)
	}
	sl, err := st.LatestSlice(ctx, "aura")
	if err != nil {
		t.Fatalf("LatestSlice: %v", err)
	}
	if !sl.BuiltAt.IsZero() {
		t.Errorf("дата сборки не пуста: %v", sl.BuiltAt)
	}
}

// --- вспомогательное ---

func utc(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func task(id string, opened time.Time) domain.Task {
	return domain.Task{ID: id, Title: "задача " + id, OpenedAt: opened}
}

func create(t *testing.T, st store.Store, task domain.Task) {
	t.Helper()

	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask %s: %v", task.ID, err)
	}
}

func add(t *testing.T, st store.Store, src domain.Source) {
	t.Helper()

	if err := st.AddSource(context.Background(), src); err != nil {
		t.Fatalf("AddSource %s: %v", src.ID, err)
	}
}

func empty(t *testing.T, st store.Store) bool {
	t.Helper()

	yes, err := st.Empty(context.Background())
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	return yes
}

func ids(list []domain.Source) []string {
	out := make([]string, len(list))
	for i, src := range list {
		out[i] = src.ID
	}
	return out
}

func same(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
