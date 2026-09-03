package bitrix

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tasksJSON повторяет форму живого ответа tasks.task.list, включая то, на чём
// ломается декодер, написанный по здравому смыслу: числа приходят строками, а
// на месте проекта у задачи без проекта стоит пустой массив, а не объект.
const tasksJSON = `{"result":{"tasks":[
{"id":"4","title":"Внедрение Сделка Усама","chatId":"28","status":"2","subStatus":"-1",
 "createdDate":"2026-08-24T16:53:46+03:00","deadline":"2026-08-31T19:00:00+03:00",
 "closedDate":null,"group":[],"groupId":"0",
 "creator":{"id":"8","name":"Амируллах Муталибов","workPosition":null},
 "responsible":{"id":"1","name":"Александр Волошук","workPosition":null}},
{"id":"2","title":"Внедрение Замира клиент","chatId":"24","status":"5",
 "createdDate":"2026-08-24T16:50:08+03:00","deadline":null,
 "group":{"id":"7","name":"Проект"},"groupId":"7",
 "creator":{"id":"8","name":"Амируллах Муталибов"},
 "responsible":{"id":"14","name":"Кевин Джонсон"}},
{"id":"1","title":"Задача без чата","chatId":null,"status":"2",
 "createdDate":"2026-08-20T09:00:00+03:00","deadline":null,
 "group":[],"creator":null,"responsible":null}
]}}`

func TestTasks(t *testing.T) {
	t.Parallel()

	var got url.Values
	c := serve(t, func(method string, form url.Values) (int, string) {
		if method != "tasks.task.list" {
			t.Errorf("вызван метод %q", method)
		}
		got = form
		return 200, tasksJSON
	})

	list, err := c.Tasks(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("задач %d, хотели 3: %+v", len(list), list)
	}

	// Чат задачи запрашивается явно: без CHAT_ID в select портал его не
	// присылает, и весь смысл метода пропадает — именно за чатом сюда и идут.
	if !hasSelect(got, "CHAT_ID") {
		t.Errorf("CHAT_ID не запрошен: %v", got)
	}

	first := list[0]
	switch {
	case first.ID != "4":
		t.Errorf("номер задачи = %q, хотели 4", first.ID)
	case first.Title != "Внедрение Сделка Усама":
		t.Errorf("название = %q", first.Title)
	case first.ChatID != 28:
		t.Errorf("чат = %d, хотели 28", first.ChatID)
	case first.Author != "Амируллах Муталибов":
		t.Errorf("постановщик = %q", first.Author)
	case first.Assignee != "Александр Волошук":
		t.Errorf("ответственный = %q", first.Assignee)
	case first.Closed:
		t.Error("задача в работе названа завершённой")
	}

	// «28» и «chat28» — разные вещи для портала: второе это чат, первое —
	// личная переписка с сотрудником №28. Ошибка здесь молча привела бы в срез
	// чужую переписку.
	if first.DialogID() != "chat28" {
		t.Errorf("DialogID = %q, хотели chat28", first.DialogID())
	}

	deadline := time.Date(2026, time.August, 31, 19, 0, 0, 0, first.Deadline.Location())
	if !first.Deadline.Equal(deadline) {
		t.Errorf("срок = %v, хотели %v", first.Deadline, deadline)
	}

	// Незаполненный срок обязан остаться нулевым: правило всей схемы — «дата не
	// названа» и «первое января первого года» это разные утверждения.
	if !list[1].Deadline.IsZero() {
		t.Errorf("пустой срок заполнился: %v", list[1].Deadline)
	}
	if !list[1].Closed {
		t.Error("завершённая задача не отмечена завершённой")
	}

	// Задача без чата в список попадает: она существует, и человек должен
	// увидеть, почему её выбрать нельзя. Молча пропасть она не вправе.
	last := list[2]
	if last.ChatID != 0 || last.DialogID() != "" {
		t.Errorf("у задачи без чата появился чат: %d / %q", last.ChatID, last.DialogID())
	}
	if last.Author != "" || last.Assignee != "" {
		t.Errorf("у задачи без карточек людей появились имена: %q / %q", last.Author, last.Assignee)
	}
}

// TestTasksRedactsTitle: название задачи и имена сотрудников идут через ту же
// замазку, что и подписи чатов. Причина не в аккуратности: название попадает в
// поле связи и в форму, а задачу в портале могут назвать адресом вебхука —
// сохранённый секрет из хранилища уже не отзовёшь.
func TestTasksRedactsTitle(t *testing.T) {
	t.Parallel()

	body := `{"result":{"tasks":[{"id":"9","title":"Смотри https://b24-x.bitrix24.ru/rest/1/` +
		fakeToken + `/ вот тут","chatId":"3","status":"2",
		"creator":{"id":"8","name":"Тест [b]жирный[/b]"}}]}}`

	c := serve(t, func(string, url.Values) (int, string) { return 200, body })

	list, err := c.Tasks(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("задач %d, хотели 1", len(list))
	}
	if title := list[0].Title; strings.Contains(title, fakeToken) {
		t.Errorf("токен доступа остался в названии: %q", title)
	}
	// Разметка снимается тем же проходом: в выпадающем списке [b] показывать
	// нечего.
	if list[0].Author != "Тест жирный" {
		t.Errorf("постановщик = %q, хотели «Тест жирный»", list[0].Author)
	}
}

// TestTaskChat: закрепление ходит за чатом отдельно, потому что между показом
// формы и отправкой в портале могла появиться новая задача.
func TestTaskChat(t *testing.T) {
	t.Parallel()

	const body = `{"result":{"task":{"id":"4","title":"Внедрение","chatId":"28","status":"2",
	 "createdDate":"2026-08-24T16:53:46+03:00","group":[]}}}`

	var got url.Values
	c := serve(t, func(method string, form url.Values) (int, string) {
		if method != "tasks.task.get" {
			t.Errorf("вызван метод %q", method)
		}
		got = form
		return 200, body
	})

	task, err := c.TaskChat(context.Background(), "4")
	if err != nil {
		t.Fatalf("TaskChat: %v", err)
	}
	if got.Get("taskId") != "4" {
		t.Errorf("taskId = %q", got.Get("taskId"))
	}
	if task.DialogID() != "chat28" {
		t.Errorf("чат = %q, хотели chat28", task.DialogID())
	}

	// Пустой номер до портала не доходит: спрашивать «какой чат у задачи ничто»
	// незачем, а ответ портала на такой вопрос ничего не объяснит человеку.
	if _, err := c.TaskChat(context.Background(), "  "); err == nil {
		t.Error("пустой номер задачи принят")
	}
}

// TestTaskChatMissing: у несуществующей задачи портал отдаёт на месте результата
// пустой массив, а не объект. Проверено на живом портале — и обошлось дорого:
// строгий разбор падал с ошибкой, ошибку разбора клиент считает сбоем связи, и
// опечатка в номере задачи стоила трёх походов в портал и шести секунд.
func TestTaskChatMissing(t *testing.T) {
	t.Parallel()

	calls := 0
	c := serve(t, func(string, url.Values) (int, string) {
		calls++
		return 200, `{"result":[]}`
	})

	_, err := c.TaskChat(context.Background(), "999")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("ошибка %v, хотели ErrTaskNotFound", err)
	}
	// Повторы здесь не к месту: портал ответил, и второй такой же вопрос принесёт
	// тот же ответ.
	if calls != 1 {
		t.Errorf("походов в портал %d, хотели 1", calls)
	}
	// Это не сбой портала: *Error транспорт переводит в 502, а опечатка в номере
	// задачи — не отказ шлюза.
	var portal *Error
	if errors.As(err, &portal) {
		t.Errorf("отсутствие задачи выдано за сбой портала: %v", err)
	}
}

func hasSelect(form url.Values, field string) bool {
	for key, values := range form {
		if !strings.HasPrefix(key, "select") {
			continue
		}
		for _, v := range values {
			if v == field {
				return true
			}
		}
	}
	return false
}

// TestTasksSearch: поиск идёт на портале, а не по загруженному списку. Задач
// там почти десять тысяч, и нужная почти никогда не из последней полусотни —
// выгружать их все ради подстроки значило бы гонять мегабайты на каждое
// открытие формы.
func TestTasksSearch(t *testing.T) {
	t.Parallel()

	var got url.Values
	c := serve(t, func(_ string, form url.Values) (int, string) {
		got = form
		return 200, tasksJSON
	})

	if _, err := c.Tasks(context.Background(), "  Асият  ", 50); err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	// Пробелы по краям срезаются: поле поиска их набирает само.
	if q := got.Get("filter[%TITLE]"); q != "Асият" {
		t.Errorf("фильтр по названию = %q", q)
	}

	// Пустой запрос фильтра не ставит: тогда список — просто свежие задачи.
	if _, err := c.Tasks(context.Background(), "   ", 50); err != nil {
		t.Fatalf("Tasks без запроса: %v", err)
	}
	if _, ok := got["filter[%TITLE]"]; ok {
		t.Errorf("пустой запрос превратился в фильтр: %v", got)
	}
}

// TestTasksPaging: портал отдаёт не больше полусотни за вызов и кладёт смещение
// следующей страницы в конверт. Одной страницей обходиться нельзя.
func TestTasksPaging(t *testing.T) {
	t.Parallel()

	var starts []string
	page := 0
	c := serve(t, func(_ string, form url.Values) (int, string) {
		starts = append(starts, form.Get("start"))
		page++
		// Две страницы по одной задаче, потом конец списка без смещения.
		if page < 3 {
			return 200, `{"result":{"tasks":[{"id":"` + strconv.Itoa(page) +
				`","title":"Задача","chatId":"7","status":"2"}]},"next":` + strconv.Itoa(page*50) + `}`
		}
		return 200, `{"result":{"tasks":[]}}`
	})

	list, err := c.Tasks(context.Background(), "", 50)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("задач %d, хотели 2: %+v", len(list), list)
	}
	// Первая страница без смещения, дальше — то, что назвал портал.
	want := []string{"", "50", "100"}
	if len(starts) != len(want) {
		t.Fatalf("страниц %d, хотели %d: %v", len(starts), len(want), starts)
	}
	for i, s := range want {
		if starts[i] != s {
			t.Errorf("страница %d: start=%q, хотели %q", i, starts[i], s)
		}
	}
}

// TestTasksStopsAtLimit: набрав нужное число, за следующей страницей не идём —
// поход в чужой сервис не бесплатный.
func TestTasksStopsAtLimit(t *testing.T) {
	t.Parallel()

	calls := 0
	c := serve(t, func(string, url.Values) (int, string) {
		calls++
		return 200, tasksJSON
	})

	list, err := c.Tasks(context.Background(), "", 2)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("задач %d, хотели 2", len(list))
	}
	if calls != 1 {
		t.Errorf("походов в портал %d, хотели 1", calls)
	}
}
