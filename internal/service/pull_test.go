package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// fakeToken — выдуманный токен вебхука. Настоящий адрес в репозиторий не
// попадает: он равносилен паролю к порталу заказчика.
const fakeToken = "abcdef1234567890"

// portalMessage — сообщение подставного портала.
type portalMessage struct {
	ID     int
	Text   string
	Author int
	Date   time.Time
}

// fakePortal — портал, отдающий заданные сообщения постранично, с тем же
// поведением курсора, что и живой Bitrix: FIRST_ID отдаёт сообщения новее
// указанного, порядок от новых к старым.
type fakePortal struct {
	msgs []portalMessage

	// calls — сколько раз спрашивали сообщения. По нему видно, ходит ли подтяжка
	// в портал лишний раз.
	calls int

	// firstIDs — значения курсора, с которыми приходили. Проверка того, что
	// подтяжка продолжает с места, а не перечитывает чат заново.
	firstIDs []int
}

func (p *fakePortal) client(t *testing.T) *bitrix.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("не разобрана форма: %v", err)
		}
		p.calls++

		first, _ := strconv.Atoi(r.PostForm.Get("FIRST_ID"))
		p.firstIDs = append(p.firstIDs, first)

		limit, err := strconv.Atoi(r.PostForm.Get("LIMIT"))
		if err != nil || limit <= 0 {
			limit = 100
		}

		var picked []portalMessage
		for _, m := range p.msgs {
			if m.ID > first {
				picked = append(picked, m)
			}
			if len(picked) >= limit {
				break
			}
		}

		// Портал отдаёт от новых к старым и кладёт имена отдельным блоком.
		items := make([]map[string]any, 0, len(picked))
		for i := len(picked) - 1; i >= 0; i-- {
			m := picked[i]
			items = append(items, map[string]any{
				"id": m.ID, "chat_id": 28, "author_id": m.Author,
				"date": m.Date.Format(time.RFC3339), "text": m.Text,
			})
		}
		body, _ := json.Marshal(map[string]any{
			"result": map[string]any{
				"chat_id":  28,
				"messages": items,
				"users": []map[string]any{
					{"id": 8, "name": "Амируллах Муталибов"},
				},
			},
		})
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(body); err != nil {
			t.Errorf("не записан ответ: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	// Адрес вебхука с путём и токеном — как у настоящего. Нужен именно такой,
	// чтобы проверить, что в ссылку на оригинал токен не попадает.
	return bitrix.New(srv.URL+"/rest/1/"+fakeToken+"/", nil)
}

// pinned готовит сервис с задачей и закреплённым за ней чатом портала.
func pinned(t *testing.T, p *fakePortal) (*Service, string) {
	t.Helper()

	s := newService(t)
	s.Portal(p.client(t))
	ctx := context.Background()

	task, err := s.CreateTask(ctx, domain.Task{Title: "Внедрение"})
	if err != nil {
		t.Fatalf("создать задачу: %v", err)
	}
	_, err = s.PinChat(ctx, domain.ChatLink{
		TaskID: task.ID, System: domain.SystemBitrix,
		DialogID: "chat28", Title: "Внедрение", ExternalTaskID: "4",
	})
	if err != nil {
		t.Fatalf("закрепить чат: %v", err)
	}
	return s, task.ID
}

func msg(id int, text string) portalMessage {
	return portalMessage{
		ID: id, Text: text, Author: 8,
		Date: time.Date(2026, time.August, 24, 16, 0, id, 0, time.UTC),
	}
}

func TestPullChats(t *testing.T) {
	p := &fakePortal{msgs: []portalMessage{
		msg(40, "первое"), msg(44, "второе"), msg(48, "третье"),
	}}
	s, taskID := pinned(t, p)
	ctx := context.Background()

	got, err := s.PullChats(ctx, taskID)
	if err != nil {
		t.Fatalf("PullChats: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("итогов %d, хотели 1: %+v", len(got), got)
	}
	res := got[0]
	switch {
	case res.Fetched != 3:
		t.Errorf("получено %d, хотели 3", res.Fetched)
	case res.Added != 3:
		t.Errorf("новых %d, хотели 3", res.Added)
	case res.LastMessageID != 48:
		t.Errorf("курсор %d, хотели 48", res.LastMessageID)
	}

	// Курсор обязан оказаться в хранилище, а не только в ответе: следующая
	// подтяжка читает его оттуда, и разойтись эти две величины не должны.
	link := onlyChatLink(t, s, taskID)
	if link.LastMessageID != 48 {
		t.Errorf("курсор в хранилище %d, хотели 48", link.LastMessageID)
	}
	if !link.LastSyncAt.Equal(today) {
		t.Errorf("время похода %v, часы сервиса на %v", link.LastSyncAt, today)
	}

	stored, err := s.ChatMessages(ctx, taskID)
	if err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("сообщений %d, хотели 3", len(stored))
	}
	// Порядок хроникой: подтяжка забирает от старых к новым, и на экране
	// переписка должна читаться сверху вниз.
	if stored[0].Body != "первое" || stored[2].Body != "третье" {
		t.Errorf("порядок сообщений: %q … %q", stored[0].Body, stored[2].Body)
	}
	if stored[0].Author != "Амируллах Муталибов" {
		t.Errorf("автор = %q", stored[0].Author)
	}
	if !stored[0].FetchedAt.Equal(today) {
		t.Errorf("дата переноса %v, часы сервиса на %v", stored[0].FetchedAt, today)
	}
}

// TestPullMakesSource: перенесённая переписка становится материалом для
// разбора. Без этого подтяжка складывает сообщения в журнал, до которого
// разбору не дотянуться, — и кнопка «Подтянуть переписку» ничего не меняет в
// срезе.
func TestPullMakesSource(t *testing.T) {
	p := &fakePortal{msgs: []portalMessage{
		{ID: 40, Text: "чат создан", Author: 0,
			Date: time.Date(2026, time.August, 24, 16, 0, 0, 0, time.UTC)},
		msg(44, "срок двигаем на 20 июня"),
		msg(48, "жду доступ к контуру"),
	}}
	s, taskID := pinned(t, p)
	ctx := context.Background()

	got, err := s.PullChats(ctx, taskID)
	if err != nil {
		t.Fatalf("PullChats: %v", err)
	}
	if got[0].SourceID == "" {
		t.Fatal("источник из переписки не заведён")
	}

	sources, err := s.Sources(ctx, taskID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	// Один источник на подтяжку, а не один на сообщение: сообщений в чате сотни,
	// и каждое отдельным источником превратило бы список в ленту чата.
	if len(sources) != 1 {
		t.Fatalf("источников %d, хотели 1: %+v", len(sources), sources)
	}

	src := sources[0]
	switch {
	case src.Kind != domain.KindCorrespondence:
		t.Errorf("вид источника = %q", src.Kind)
	case src.TaskID != taskID:
		t.Errorf("источник не привязан к задаче: %q", src.TaskID)
	case !strings.Contains(src.Body, "срок двигаем на 20 июня"):
		t.Errorf("текста сообщения нет в источнике: %q", src.Body)
	}

	// Системные сообщения портала в источник не попадают: цитировать в них
	// нечего, а разбор они заваливают шумом. В журнале сообщений они при этом
	// остаются — он обязан быть полным.
	if strings.Contains(src.Body, "чат создан") {
		t.Errorf("системное сообщение попало в источник: %q", src.Body)
	}
	stored, err := s.ChatMessages(ctx, taskID)
	if err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if len(stored) != 3 {
		t.Errorf("сообщений в журнале %d, хотели 3", len(stored))
	}

	// Автор и дата попадают в текст: разбор цитирует отсюда, и без них цитата
	// не даёт ни времени, ни говорящего.
	if !strings.Contains(src.Body, "Амируллах Муталибов") {
		t.Errorf("автора нет в тексте источника: %q", src.Body)
	}

	// Повторная подтяжка нового источника не заводит: переносить нечего.
	if _, err := s.PullChats(ctx, taskID); err != nil {
		t.Fatalf("вторая подтяжка: %v", err)
	}
	after, err := s.Sources(ctx, taskID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("источников после второй подтяжки %d, хотели 1", len(after))
	}
}

// TestPullOnlyServiceMessages: чат, где одни системные сообщения, источника не
// даёт. Пустой источник в списке — обещание материала, которого нет.
func TestPullOnlyServiceMessages(t *testing.T) {
	p := &fakePortal{msgs: []portalMessage{
		{ID: 40, Text: "чат создан", Author: 0,
			Date: time.Date(2026, time.August, 24, 16, 0, 0, 0, time.UTC)},
	}}
	s, taskID := pinned(t, p)
	ctx := context.Background()

	got, err := s.PullChats(ctx, taskID)
	if err != nil {
		t.Fatalf("PullChats: %v", err)
	}
	if got[0].Added != 1 {
		t.Errorf("новых сообщений %d, хотели 1", got[0].Added)
	}
	if got[0].SourceID != "" {
		t.Errorf("заведён источник из одних системных сообщений: %q", got[0].SourceID)
	}
	sources, err := s.Sources(ctx, taskID)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("источников %d, хотели 0: %+v", len(sources), sources)
	}
}

// TestPullChatsRepeat: вторая подтяжка продолжает с курсора, а не перечитывает
// чат. Ошибка здесь стоит дорого и молча: перечитанный чат приедет вторым
// экземпляром каждого сообщения.
func TestPullChatsRepeat(t *testing.T) {
	p := &fakePortal{msgs: []portalMessage{msg(40, "первое"), msg(44, "второе")}}
	s, taskID := pinned(t, p)
	ctx := context.Background()

	if _, err := s.PullChats(ctx, taskID); err != nil {
		t.Fatalf("первая подтяжка: %v", err)
	}

	// Между подтяжками в чате появилось сообщение.
	p.msgs = append(p.msgs, msg(52, "третье"))

	got, err := s.PullChats(ctx, taskID)
	if err != nil {
		t.Fatalf("вторая подтяжка: %v", err)
	}
	if got[0].Fetched != 1 || got[0].Added != 1 {
		t.Errorf("вторая подтяжка: получено %d, новых %d — хотели по одному",
			got[0].Fetched, got[0].Added)
	}
	if got[0].LastMessageID != 52 {
		t.Errorf("курсор %d, хотели 52", got[0].LastMessageID)
	}

	// Курсор ушёл в портал: вторая подтяжка спрашивала от последнего известного
	// сообщения, а не с начала чата.
	if len(p.firstIDs) < 2 || p.firstIDs[len(p.firstIDs)-1] != 44 {
		t.Errorf("курсор в запросах портала: %v, последний хотели 44", p.firstIDs)
	}

	stored, err := s.ChatMessages(ctx, taskID)
	if err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}
	if len(stored) != 3 {
		t.Errorf("сообщений %d, хотели 3: переписка приехала вторым экземпляром", len(stored))
	}
}

// TestPullChatsNothingNew: поход, не принёсший нового, — обычный исход. Курсор
// сообщений не двигается, а время похода обновляется: «сходили и ничего нет» и
// «не ходили вовсе» — разные вещи.
func TestPullChatsNothingNew(t *testing.T) {
	p := &fakePortal{msgs: []portalMessage{msg(40, "первое")}}
	s, taskID := pinned(t, p)
	ctx := context.Background()

	if _, err := s.PullChats(ctx, taskID); err != nil {
		t.Fatalf("первая подтяжка: %v", err)
	}
	before := onlyChatLink(t, s, taskID)

	got, err := s.PullChats(ctx, taskID)
	if err != nil {
		t.Fatalf("вторая подтяжка: %v", err)
	}
	if got[0].Fetched != 0 || got[0].Added != 0 {
		t.Errorf("пустая подтяжка: получено %d, новых %d", got[0].Fetched, got[0].Added)
	}

	after := onlyChatLink(t, s, taskID)
	if after.LastMessageID != before.LastMessageID {
		t.Errorf("курсор сдвинулся на пустой подтяжке: %d → %d",
			before.LastMessageID, after.LastMessageID)
	}
	if after.LastSyncAt.IsZero() {
		t.Error("время похода не записано")
	}
}

// TestPullChatsPaging: чат длиннее страницы забирается целиком. Первая подтяжка
// большого чата иначе принесла бы сотню сообщений и остановилась, а человек
// решил бы, что переписка на этом кончается.
func TestPullChatsPaging(t *testing.T) {
	var msgs []portalMessage
	for i := 1; i <= 250; i++ {
		msgs = append(msgs, msg(i, fmt.Sprintf("сообщение %d", i)))
	}
	p := &fakePortal{msgs: msgs}
	s, taskID := pinned(t, p)

	got, err := s.PullChats(context.Background(), taskID)
	if err != nil {
		t.Fatalf("PullChats: %v", err)
	}
	if got[0].Fetched != 250 || got[0].Added != 250 {
		t.Errorf("получено %d, новых %d — хотели по 250", got[0].Fetched, got[0].Added)
	}
	if got[0].LastMessageID != 250 {
		t.Errorf("курсор %d, хотели 250", got[0].LastMessageID)
	}
	// Три полные страницы и одна неполная. Лишнего похода за заведомо пустой
	// страницей быть не должно: неполная страница уже сказала, что чат кончился.
	if p.calls != 3 {
		t.Errorf("походов в портал %d, хотели 3: %v", p.calls, p.firstIDs)
	}
}

// TestPullChatsMessageURL: ссылка на оригинал ведёт в портал и не содержит
// токена. Токен в ссылке означал бы пароль к порталу, сохранённый в хранилище и
// показанный на экране, — а сохранённый секрет уже не отзовёшь.
func TestPullChatsMessageURL(t *testing.T) {
	p := &fakePortal{msgs: []portalMessage{msg(40, "первое")}}
	s, taskID := pinned(t, p)
	ctx := context.Background()

	if _, err := s.PullChats(ctx, taskID); err != nil {
		t.Fatalf("PullChats: %v", err)
	}
	stored, err := s.ChatMessages(ctx, taskID)
	if err != nil {
		t.Fatalf("ChatMessages: %v", err)
	}

	link := stored[0].External.URL
	if link == "" {
		t.Fatal("ссылки на оригинал нет")
	}
	if strings.Contains(link, fakeToken) {
		t.Errorf("токен доступа попал в ссылку: %q", link)
	}
	if strings.Contains(link, "/rest/") {
		t.Errorf("в ссылке остался путь вебхука: %q", link)
	}

	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("ссылка не разбирается: %v", err)
	}
	if u.Query().Get("IM_DIALOG") != "chat28" || u.Query().Get("IM_MESSAGE") != "40" {
		t.Errorf("ссылка ведёт не туда: %q", link)
	}
}

// TestPullChatsWithoutPortal: подтяжку запускает человек нажатием, и молчаливое
// «ничего не произошло» он прочитает как «новых сообщений нет».
func TestPullChatsWithoutPortal(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, domain.Task{Title: "Внедрение"})
	if err != nil {
		t.Fatalf("создать задачу: %v", err)
	}
	if _, err := s.PullChats(ctx, task.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("ошибка %v, ожидалась ErrInvalid", err)
	}
}

// TestPullChatsUnknownTask: у несуществующей задачи чатов нет, и это ErrNotFound
// от хранилища, а не пустой список.
func TestPullChatsUnknownTask(t *testing.T) {
	p := &fakePortal{}
	s, _ := pinned(t, p)

	if _, err := s.PullChats(context.Background(), "нет-такой"); err == nil {
		t.Error("подтяжка неизвестной задачи прошла")
	}
	if p.calls != 0 {
		t.Errorf("походов в портал %d, хотели 0", p.calls)
	}
}

// brokenWrites — хранилище, у которого не выходит записать сообщения. Всё
// остальное работает как обычно.
type brokenWrites struct {
	store.Store
	err error
}

func (b brokenWrites) AddRawMessages(context.Context, []domain.RawMessage) (int, error) {
	return 0, b.err
}

// TestPullChatsKeepsCursorOnWriteFailure проверяет то, ради чего в подтяжке
// вообще задан порядок действий: курсор двигается только после того, как
// сообщения легли в хранилище.
//
// Курсор, уехавший вперёд раньше записи, — это переписка, потерянная молча.
// Следующая подтяжка начнёт с номера, до которого ничего не сохранено, и узнать
// об этом будет неоткуда: в чате пусто, в реестре пусто, ошибок нет.
func TestPullChatsKeepsCursorOnWriteFailure(t *testing.T) {
	p := &fakePortal{msgs: []portalMessage{msg(40, "первое"), msg(44, "второе")}}
	s, taskID := pinned(t, p)
	ctx := context.Background()

	fail := errors.New("диск кончился")
	s.store = brokenWrites{Store: s.store, err: fail}

	if _, err := s.PullChats(ctx, taskID); !errors.Is(err, fail) {
		t.Fatalf("ошибка %v, ожидалась ошибка записи", err)
	}

	link := onlyChatLink(t, s, taskID)
	if link.LastMessageID != 0 {
		t.Errorf("курсор уехал на %d, хотя сообщения не записаны", link.LastMessageID)
	}
	if !link.LastSyncAt.IsZero() {
		t.Errorf("отмечен успешный поход %v, хотя запись не удалась", link.LastSyncAt)
	}
}

func onlyChatLink(t *testing.T, s *Service, taskID string) domain.ChatLink {
	t.Helper()

	list, err := s.TaskChats(context.Background(), taskID)
	if err != nil {
		t.Fatalf("TaskChats: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("чатов %d, хотели 1: %+v", len(list), list)
	}
	return list[0]
}
