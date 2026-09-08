package bitrix

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Токен в тестовых данных выдуманный. Настоящий адрес вебхука в репозиторий не
// попадает — ни в код, ни в testdata: он равносилен паролю к порталу заказчика.
const fakeToken = "abcdef1234567890"

// serve поднимает подставной портал. Обработчик получает имя метода без
// расширения — так же, как его видит человек в документации Bitrix.
func serve(t *testing.T, h func(method string, form url.Values) (int, string)) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("не разобрана форма запроса: %v", err)
		}
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".json")

		code, body := h(method, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("не записан ответ: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	return New(srv.URL, nil)
}

// noDelay убирает паузы между повторами. Логику повторов проверять нужно, а
// отсиживать при этом настоящие паузы — нет.
//
// Тесты, которые это вызывают, не помечаются t.Parallel: retryDelay — общая
// переменная пакета.
func noDelay(t *testing.T) {
	t.Helper()

	saved := retryDelay
	retryDelay = 0
	t.Cleanup(func() { retryDelay = saved })
}

// recentJSON повторяет форму живого ответа im.recent.list, включая всё, на чём
// ломается декодер, написанный по названиям полей: объект вместо массива,
// идентификатор то числом, то строкой, last_name равный null.
const recentJSON = `{"result":{"items":[
{"id":20,"chat_id":32,"type":"user","title":"Битрикс24 Гид",
 "message":{"id":64,"text":"Привет! Заглядывайте в Маркетплейс","author_id":20,"date":"2026-08-26T10:43:08+03:00"},
 "last_id":0,"counter":2,"date_update":"2026-08-26T10:43:08+03:00","date_last_activity":"2026-08-26T10:43:08+03:00",
 "user":{"id":20,"name":"Битрикс24 Гид","first_name":"Битрикс24 Гид","last_name":null,"bot":true,"type":"bot"}},
{"id":8,"chat_id":8,"type":"user","title":"Амируллах Муталибов",
 "message":{"id":46,"text":"URL:\n\n[URL]https://b24-lhajtv.bitrix24.ru/rest/1/` + fakeToken + `/profile.json[/URL]","author_id":1,"date":"2026-08-24T16:53:30+03:00"},
 "last_id":0,"counter":0,"date_update":"2026-08-24T16:53:30+03:00","date_last_activity":"2026-08-24T16:53:30+03:00",
 "user":{"id":1,"name":"Александр Волошук","first_name":"Александр","last_name":"Волошук","bot":false,"type":"user"}},
{"id":"chat12","chat_id":12,"type":"chat","title":"Новости компании",
 "message":{"id":12,"text":"Новости компании. Делитесь анонсами","author_id":0,"date":"2026-08-24T16:47:23+03:00"},
 "last_id":0,"counter":0,"date_update":"2026-08-24T16:47:23+03:00","date_last_activity":"2026-08-24T16:47:23+03:00",
 "chat":{"id":12,"name":"Новости компании","owner":1}}
]}}`

func TestChats(t *testing.T) {
	c := serve(t, func(method string, _ url.Values) (int, string) {
		if method != "im.recent.list" {
			t.Errorf("вызван не тот метод: %s", method)
		}
		return http.StatusOK, recentJSON
	})

	chats, err := c.Chats(context.Background(), 0)
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if len(chats) != 3 {
		t.Fatalf("получено чатов %d, ожидалось 3", len(chats))
	}

	// Порядок — по убыванию активности, самый свежий разговор сверху.
	if got := []string{chats[0].DialogID, chats[1].DialogID, chats[2].DialogID}; got[0] != "20" || got[1] != "8" || got[2] != "chat12" {
		t.Errorf("порядок чатов %v, ожидался [20 8 chat12]", got)
	}

	// Числовой id стал строкой, строковый остался строкой.
	if chats[2].DialogID != "chat12" || !chats[2].Group() {
		t.Errorf("групповой чат опознан неверно: %+v", chats[2])
	}
	if chats[1].DialogID != "8" || chats[1].Group() {
		t.Errorf("личная переписка опознана неверно: %+v", chats[1])
	}

	// У диалога с ботом type равен «user»: отличить его можно только по флагу
	// внутри карточки пользователя.
	if !chats[0].Bot || chats[0].Selectable() {
		t.Errorf("бот не отфильтрован: %+v", chats[0])
	}
	if !chats[1].Selectable() {
		t.Errorf("живая переписка отфильтрована зря: %+v", chats[1])
	}

	// Подпись к чату не должна показывать токен доступа на экране.
	if strings.Contains(chats[1].Preview, fakeToken) {
		t.Errorf("токен виден в подписи чата: %q", chats[1].Preview)
	}
	if !strings.Contains(chats[1].Preview, "/rest/1/***") {
		t.Errorf("подпись чата не замазана: %q", chats[1].Preview)
	}
	if strings.Contains(chats[1].Preview, "[URL]") {
		t.Errorf("разметка осталась в подписи: %q", chats[1].Preview)
	}
}

// messagesJSON — форма живого ответа im.dialog.messages.get: сообщения от новых
// к старым, params то объектом, то пустым массивом, uuid равный null.
const messagesJSON = `{"result":{"chat_id":8,"messages":[
{"id":46,"chat_id":8,"author_id":1,"date":"2026-08-24T16:53:30+03:00",
 "text":"Вебхук: [URL]https://b24-lhajtv.bitrix24.ru/rest/1/` + fakeToken + `/[/URL]","uuid":null,"replaces":[],"params":[]},
{"id":44,"chat_id":8,"author_id":1,"date":"2026-08-24T16:50:00+03:00",
 "text":"Пользователь присоединился","uuid":null,"replaces":[],"params":{"COMPONENT_ID":"ChatUserJoin","NOTIFY":"N"}},
{"id":42,"chat_id":8,"author_id":0,"date":"2026-08-24T16:45:20+03:00",
 "text":"Чат создан","uuid":null,"replaces":[],"params":[]},
{"id":40,"chat_id":8,"author_id":1,"date":"2026-08-24T16:40:00+03:00",
 "text":"[B]Смета[/B] согласована, начинаем в понедельник","uuid":null,"replaces":[],"params":[]}
],"users":[{"id":1,"active":true,"name":"Александр Волошук","first_name":"Александр","last_name":"Волошук"}],"files":[]}}`

func TestMessages(t *testing.T) {
	c := serve(t, func(method string, form url.Values) (int, string) {
		if method != "im.dialog.messages.get" {
			t.Errorf("вызван не тот метод: %s", method)
		}
		if got := form.Get("DIALOG_ID"); got != "chat8" {
			t.Errorf("DIALOG_ID = %q, ожидался chat8", got)
		}
		return http.StatusOK, messagesJSON
	})

	msgs, err := c.Messages(context.Background(), "chat8", 0, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("получено сообщений %d, ожидалось 4", len(msgs))
	}

	// Портал отдаёт от новых к старым; наружу выходит порядок событий.
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].ID >= msgs[i].ID {
			t.Fatalf("сообщения не по возрастанию: %d перед %d", msgs[i-1].ID, msgs[i].ID)
		}
	}

	first, last := msgs[0], msgs[3]

	// Обычное сообщение: разметка снята, автор назван по имени.
	if first.Text != "Смета согласована, начинаем в понедельник" {
		t.Errorf("текст сообщения 40: %q", first.Text)
	}
	if first.Author != "Александр Волошук" {
		t.Errorf("автор сообщения 40: %q", first.Author)
	}
	if first.Service {
		t.Errorf("обычное сообщение помечено служебным: %+v", first)
	}
	if want := time.Date(2026, 8, 24, 16, 40, 0, 0, time.FixedZone("", 3*60*60)); !first.Date.Equal(want) {
		t.Errorf("дата сообщения 40: %v, ожидалась %v", first.Date, want)
	}

	// Служебные сообщения: одно опознано по нулевому автору, другое — по
	// компоненту отрисовки. В источники ни то, ни другое попадать не должно.
	if !msgs[1].Service {
		t.Errorf("сообщение без автора не помечено служебным: %+v", msgs[1])
	}
	if !msgs[2].Service {
		t.Errorf("сообщение с COMPONENT_ID не помечено служебным: %+v", msgs[2])
	}

	// Токен доступа замазан, и об этом сказано вызывающему.
	if strings.Contains(last.Text, fakeToken) {
		t.Errorf("токен попал в текст источника: %q", last.Text)
	}
	if !last.Redacted {
		t.Errorf("замазывание не отмечено флагом: %+v", last)
	}
	if !strings.Contains(last.Text, "/rest/1/***") {
		t.Errorf("текст сообщения 46: %q", last.Text)
	}
}

// TestMessagesCursor закрепляет главный вывод проверки на живом портале:
// синхронизация идёт по FIRST_ID. LAST_ID листает историю назад, и клиент с ним
// никогда не увидел бы нового сообщения.
func TestMessagesCursor(t *testing.T) {
	var got url.Values

	c := serve(t, func(_ string, form url.Values) (int, string) {
		got = form
		return http.StatusOK, messagesJSON
	})

	msgs, err := c.Messages(context.Background(), "chat8", 42, 10)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	if got.Get("FIRST_ID") != "42" {
		t.Errorf("FIRST_ID = %q, ожидался 42", got.Get("FIRST_ID"))
	}
	if _, ok := got["LAST_ID"]; ok {
		t.Errorf("отправлен LAST_ID — это листание в архив, а не синхронизация")
	}
	if got.Get("LIMIT") != "10" {
		t.Errorf("LIMIT = %q, ожидался 10", got.Get("LIMIT"))
	}

	// Всё, что не новее курсора, отбрасывается: повторно те же сообщения
	// источниками стать не должны. В выборке остаются 44 и 46, а 42 и 40 —
	// уже сохранённые — отсекаются.
	for _, m := range msgs {
		if m.ID <= 42 {
			t.Errorf("сообщение %d не новее курсора, но пришло наружу", m.ID)
		}
	}
	if len(msgs) != 2 {
		t.Errorf("получено сообщений %d, ожидалось 2 (44 и 46)", len(msgs))
	}
}

// TestErrorEnvelope проверяет разбор конверта с ошибкой на списке чатов.
//
// Не на сообщениях, хотя раньше было на них: чтение чата переводит ACCESS_ERROR
// в ErrChatForbidden намеренно — см. TestMessagesForbidden. А конверт как
// таковой разбирается одинаково для всех методов, и проверять его надо там, где
// над ним ничего не надстроено.
func TestErrorEnvelope(t *testing.T) {
	calls := 0
	c := serve(t, func(_ string, _ url.Values) (int, string) {
		calls++
		return http.StatusForbidden, `{"error":"ACCESS_ERROR","error_description":"You do not have access to the specified dialog"}`
	})

	_, err := c.Chats(context.Background(), 0)
	if err == nil {
		t.Fatal("ожидалась ошибка доступа")
	}

	var be *Error
	if !errors.As(err, &be) {
		t.Fatalf("ошибка не разобрана как *Error: %T %v", err, err)
	}
	if be.Code != "ACCESS_ERROR" || be.Status != http.StatusForbidden {
		t.Errorf("разобрано неверно: %+v", be)
	}
	if be.Retryable() {
		t.Error("отказ в правах помечен как повторяемый — прав от повтора не появится")
	}
	if calls != 1 {
		t.Errorf("запросов сделано %d, ожидался 1", calls)
	}
}

func TestRetryOnServerError(t *testing.T) {
	noDelay(t)

	calls := 0
	c := serve(t, func(_ string, _ url.Values) (int, string) {
		calls++
		if calls == 1 {
			return http.StatusBadGateway, `{"error":"INTERNAL_SERVER_ERROR","error_description":"опять что-то упало"}`
		}
		return http.StatusOK, recentJSON
	})

	chats, err := c.Chats(context.Background(), 0)
	if err != nil {
		t.Fatalf("Chats после повтора: %v", err)
	}
	if len(chats) != 3 {
		t.Errorf("получено чатов %d, ожидалось 3", len(chats))
	}
	if calls != 2 {
		t.Errorf("запросов сделано %d, ожидалось 2", calls)
	}
}

// TestNotConfigured закрепляет требование: без настроенного Bitrix сервис
// остаётся работоспособным. Список чатов пуст, задача всё равно создаётся.
func TestNotConfigured(t *testing.T) {
	c := New("  ", nil)

	if c.Configured() {
		t.Error("пустой вебхук считается настроенным")
	}
	if _, err := c.Chats(context.Background(), 0); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("ошибка = %v, ожидалась ErrNotConfigured", err)
	}
}

// TestNotJSON — портал умеет отвечать страницей вместо данных: заблокирован,
// на обслуживании, за прокси. Это должно быть понятно из текста ошибки.
func TestNotJSON(t *testing.T) {
	noDelay(t)

	c := serve(t, func(_ string, _ url.Values) (int, string) {
		return http.StatusServiceUnavailable, "<html><body>Портал недоступен</body></html>"
	})

	_, err := c.Chats(context.Background(), 0)
	if err == nil {
		t.Fatal("ожидалась ошибка разбора")
	}
	if !strings.Contains(err.Error(), "не JSON") {
		t.Errorf("текст ошибки не объясняет причину: %v", err)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("в ошибке нет кода ответа: %v", err)
	}
}

// TestNoTokenInErrors проверяет, что адрес вебхука не вытекает через текст
// ошибки транспорта: в нём лежит токен, а ошибки попадают и в лог, и на экран.
func TestNoTokenInErrors(t *testing.T) {
	noDelay(t)

	// Заведомо мёртвый адрес того же вида, что настоящий вебхук.
	c := New("http://127.0.0.1:1/rest/1/"+fakeToken, nil)

	_, err := c.Chats(context.Background(), 0)
	if err == nil {
		t.Fatal("ожидалась ошибка соединения")
	}
	if strings.Contains(err.Error(), fakeToken) {
		t.Errorf("токен виден в тексте ошибки: %v", err)
	}
}
