package bitrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Task — задача портала в том виде, в каком её выбирают при создании задачи
// реестра.
//
// Здесь есть и чат, и поля карточки, потому что берутся они одним запросом.
// Отдельный поход за каждой задачей ради имени исполнителя обошёлся бы в
// десятки вызовов на один показ формы, а tasks.task.list присылает имена сразу.
type Task struct {
	// ID — номер задачи в портале. Строка, а не число: портал отдаёт его строкой
	// («4»), и обратно в фильтры он уходит тоже строкой. Превращать его в число
	// и назад значило бы переписывать чужой идентификатор дважды без нужды.
	ID string

	Title string

	// ChatID — чат задачи. Ноль означает, что чата у задачи нет: он заводится не
	// при постановке, а при первом событии по задаче.
	ChatID int

	// Author — постановщик, Assignee — ответственный. Имена, а не номера: в
	// реестре эти поля текстовые, и номер сотрудника портала в них ничего не
	// объясняет.
	Author   string
	Assignee string

	// CreatedAt — когда задачу поставили, Deadline — крайний срок. Незаполненный
	// срок остаётся нулевым: «срок не назван» и «первое января первого года» —
	// разные утверждения.
	CreatedAt time.Time
	Deadline  time.Time

	// Description — постановка задачи словами автора, уже без разметки и без
	// токенов. Материал не хуже переписки: в нём состав работ и суммы, а прав
	// на него хватает тех же, что на список задач.
	Description string

	// Closed отмечает завершённую задачу. Такие из списка не убираются: срез
	// собирают и по закрытой задаче, а иногда именно по ней.
	Closed bool
}

// DialogID — чат задачи в том виде, в каком его ждёт параметр DIALOG_ID. Пусто,
// если чата у задачи ещё нет.
//
// Отдельный метод, потому что превращение «28 → chat28» должно быть сделано
// один раз. Написанное по месту, оно разошлось бы: в одном месте «chat28», в
// другом «28», и подтяжка молча читала бы личную переписку сотрудника №28.
func (t Task) DialogID() string {
	if t.ChatID <= 0 {
		return ""
	}
	return "chat" + strconv.Itoa(t.ChatID)
}

// Tasks отдаёт задачи портала, свежие сверху.
//
// Метод существует потому, что im.recent.list чаты задач не показывает вообще —
// проверено на живом портале: в его выдаче только личные переписки и служебные
// групповые чаты. Чат задачи доступен, но найти его можно лишь через карточку
// задачи, где он лежит полем chatId.
// Задач в портале тысячи, и берутся они страницами: портал отдаёт не больше
// полусотни за вызов и кладёт смещение следующей страницы в конверт. Одной
// страницей обходиться нельзя — это полсотни самых свежих задач, а срез чаще
// всего нужен по той, что идёт третий месяц.
func (c *Client) Tasks(ctx context.Context, query string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}

	var tasks []Task
	start := 0
	for page := 0; page < taskMaxPages; page++ {
		batch, next, err := c.tasksPage(ctx, query, start)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, batch...)

		// Конец списка портал показывает отсутствием смещения. Пустая страница —
		// та же новость, но сказанная иначе, и полагаться только на смещение
		// нельзя: цикл обязан кончиться при любом поведении портала.
		if len(tasks) >= limit || next == nil || len(batch) == 0 {
			break
		}
		start = *next
	}
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}

	// Портал уже отсортировал, но полагаться на это нельзя: order — просьба, а
	// не обещание, и на следующей версии портала список может приехать иначе.
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})
	return tasks, nil
}

// taskMaxPages — предел страниц за один запрос списка. Нужен не ради скорости,
// а чтобы цикл был конечным: сломавшееся смещение иначе гоняло бы нас по кругу.
const taskMaxPages = 40

// tasksPage читает одну страницу списка и возвращает смещение следующей.
func (c *Client) tasksPage(ctx context.Context, query string, start int) ([]Task, *int, error) {
	params := url.Values{}
	// Поиск идёт на портале, а не в браузере: задач почти десять тысяч, и
	// выгружать их все ради подстроки — это мегабайты по сети на каждое
	// открытие формы. Проценты вокруг значения — синтаксис портала для «часть
	// названия».
	if query = strings.TrimSpace(query); query != "" {
		params.Set("filter[%TITLE]", query)
	}
	// Поля перечислены явно: без select портал присылает карточку целиком, а из
	// неё нужны семь полей из семидесяти.
	for i, f := range []string{
		"ID", "TITLE", "CHAT_ID", "DEADLINE", "CREATED_DATE",
		"RESPONSIBLE_ID", "CREATED_BY", "STATUS",
	} {
		params.Set(fmt.Sprintf("select[%d]", i), f)
	}
	// Свежие сверху — тот же порядок, что и у списка чатов: сверху то, чем
	// занимаются сейчас.
	params.Set("order[ID]", "DESC")
	if start > 0 {
		params.Set("start", strconv.Itoa(start))
	}

	var out struct {
		Tasks []taskItem `json:"tasks"`
	}
	env, err := c.call(ctx, "tasks.task.list", params, &out)
	if err != nil {
		return nil, nil, err
	}

	tasks := make([]Task, 0, len(out.Tasks))
	for _, it := range out.Tasks {
		if t, ok := it.task(); ok {
			tasks = append(tasks, t)
		}
	}
	return tasks, env.Next, nil
}

// taskItem — задача в ответе tasks.task.list.
//
// Числа здесь приходят строками («id»: «4»), а вложенные карточки автора и
// ответственного — объектами с теми же именами в нижнем регистре. Поле group
// не описано намеренно: у задачи без проекта портал присылает на его месте
// пустой массив, а у задачи в проекте — объект, и структура отвалилась бы на
// одном из двух случаев.
type taskItem struct {
	ID     rawID  `json:"id"`
	Title  string `json:"title"`
	ChatID rawID  `json:"chatId"`
	Status rawID  `json:"status"`

	Deadline    string `json:"deadline"`
	CreatedDate string `json:"createdDate"`
	Description string `json:"description"`

	Creator     *taskUser `json:"creator"`
	Responsible *taskUser `json:"responsible"`
}

type taskUser struct {
	Name string `json:"name"`
}

func (it taskItem) task() (Task, bool) {
	id := it.ID.string()
	if id == "" {
		return Task{}, false
	}

	title := caption(it.Title)
	if title == "" {
		title = "Задача №" + id
	}

	t := Task{
		ID:        id,
		Title:     title,
		CreatedAt: parseTime(it.CreatedDate),
		Deadline:  parseTime(it.Deadline),
		// Пять — «завершена» в нумерации статусов задач портала. Всё, что
		// больше, — отложенная и отклонённая: незакрытыми они тоже не считаются,
		// но и завершёнными их называть нечего, поэтому сравнение точное.
		Closed: it.Status.string() == "5",
	}
	if n, err := strconv.Atoi(it.ChatID.string()); err == nil {
		t.ChatID = n
	}
	// Имена идут через ту же замазку, что и подписи чатов, и по той же причине:
	// они попадают в поля задачи реестра, а сотрудника в портале могут звать как
	// угодно, в том числе адресом вебхука.
	if it.Creator != nil {
		t.Author = caption(it.Creator.Name)
	}
	if it.Responsible != nil {
		t.Assignee = caption(it.Responsible.Name)
	}

	// Описание идёт через снятие разметки и замазку, но без склейки строк:
	// caption для него не годится — это тело материала, а не подпись, и
	// сложенное в одну строку оно потеряет и таблицу состава работ, и абзацы.
	if body, _ := Redact(Plain(it.Description)); strings.TrimSpace(body) != "" {
		t.Description = strings.TrimSpace(body)
	}
	return t, true
}

// ErrTaskNotFound — портал ответил, но такой задачи у него нет.
//
// Отдельно от *Error намеренно: *Error означает «портал не смог», и транспорт
// переводит его в 502. Здесь портал сработал исправно, а не сошлось названное
// человеком, и ответ должен быть про запрос, а не про портал.
var ErrTaskNotFound = errors.New("задача не найдена в портале")

// TaskChat отдаёт чат задачи портала.
//
// Отдельный метод, чтобы закрепление не зависело от того, попала ли задача в
// показанный человеку список: между показом формы и отправкой проходит время, и
// за него задача могла и появиться, и исчезнуть.
func (c *Client) TaskChat(ctx context.Context, taskID string) (Task, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return Task{}, &Error{Code: "TASK_ID_EMPTY", Description: "не указана задача портала"}
	}

	var out taskEnvelope
	if err := c.Call(ctx, "tasks.task.get", url.Values{"taskId": {taskID}}, &out); err != nil {
		return Task{}, err
	}

	t, ok := out.Task.task()
	if !ok {
		return Task{}, fmt.Errorf("%s: %w", taskID, ErrTaskNotFound)
	}
	return t, nil
}

// taskEnvelope — ответ tasks.task.get.
//
// Разбор терпимый по той же причине, что и у params: у несуществующей задачи
// портал отдаёт на месте результата пустой массив, а не объект с задачей — так
// PHP отдаёт пустой ассоциативный массив. Обычная структура на этом падает с
// ошибкой разбора, а ошибку разбора клиент считает сбоем связи и повторяет
// запрос: опечатка в номере задачи оборачивалась тремя походами в портал и
// шестью секундами ожидания вместо внятного «нет такой задачи».
type taskEnvelope struct {
	Task taskItem `json:"task"`
}

func (e *taskEnvelope) UnmarshalJSON(b []byte) error {
	type plain taskEnvelope
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		*e = taskEnvelope{}
		return nil
	}
	*e = taskEnvelope(v)
	return nil
}
