package bitrix

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestChatsMarksOpenLines: открытую линию контакт-центра надо отличать от чата
// задачи и от обычного группового. По типу этого не видно — у всех трёх он
// «chat», — а разница существенная: в открытой линии говорит клиент, и его
// слова весят иначе, чем обсуждение между своими.
func TestChatsMarksOpenLines(t *testing.T) {
	t.Parallel()

	const body = `{"result":{"items":[
	{"id":"chat77","chat_id":77,"type":"chat","title":"Открытая линия: клиент",
	 "chat":{"name":"Открытая линия","entity_type":"LINES"}},
	{"id":"chat28","chat_id":28,"type":"chat","title":"Чат задачи",
	 "chat":{"name":"Задача","entity_type":"TASKS"}},
	{"id":"chat12","chat_id":12,"type":"chat","title":"Просто группа",
	 "chat":{"name":"Группа","entity_type":""}},
	{"id":"8","type":"user","title":"Коллега"}
	]}}`

	c := serve(t, func(string, url.Values) (int, string) { return http.StatusOK, body })

	chats, err := c.Chats(context.Background(), 0)
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if len(chats) != 4 {
		t.Fatalf("чатов %d, хотели 4", len(chats))
	}

	byID := map[string]Chat{}
	for _, ch := range chats {
		byID[ch.DialogID] = ch
	}

	cases := []struct {
		dialog string
		want   ChatKindHint
		lines  bool
	}{
		{"chat77", HintLines, true},
		{"chat28", HintTask, false},
		{"chat12", HintGroup, false},
		{"8", HintPrivate, false},
	}
	for _, tc := range cases {
		got := byID[tc.dialog]
		if k := got.Kinded(); k != tc.want {
			t.Errorf("%s: род %q, хотели %q", tc.dialog, k, tc.want)
		}
		if got.Lines() != tc.lines {
			t.Errorf("%s: открытая линия = %v, хотели %v", tc.dialog, got.Lines(), tc.lines)
		}
	}
}

// TestChat: чат берётся по номеру, потому что im.recent.list показывает только
// недавние чаты владельца вебхука. Переписку контакт-центра ведут операторы, и
// владельца вебхука в ней может не быть ни одного сообщения.
func TestChat(t *testing.T) {
	t.Parallel()

	const body = `{"result":{"id":"77","title":"Открытая линия: Иванов",
	 "entity_type":"LINES"}}`

	var got url.Values
	c := serve(t, func(method string, form url.Values) (int, string) {
		if method != "im.chat.get" {
			t.Errorf("вызван метод %q", method)
		}
		got = form
		return http.StatusOK, body
	})

	chat, err := c.Chat(context.Background(), "77")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// Голый номер дополняется приставкой: в адресной строке портала он стоит
	// без неё, и требовать её от человека значит требовать помнить наше
	// внутреннее соглашение.
	if got.Get("DIALOG_ID") != "chat77" {
		t.Errorf("DIALOG_ID = %q, хотели chat77", got.Get("DIALOG_ID"))
	}
	switch {
	case chat.DialogID != "chat77":
		t.Errorf("чат = %q", chat.DialogID)
	case chat.ChatID != 77:
		t.Errorf("номер чата = %d", chat.ChatID)
	case chat.Title != "Открытая линия: Иванов":
		t.Errorf("подпись = %q", chat.Title)
	case !chat.Lines():
		t.Error("открытая линия не опознана")
	}

	// Приставку, набранную человеком, второй раз не приписываем.
	if _, err := c.Chat(context.Background(), "chat77"); err != nil {
		t.Fatalf("Chat с приставкой: %v", err)
	}
	if got.Get("DIALOG_ID") != "chat77" {
		t.Errorf("DIALOG_ID = %q, хотели chat77", got.Get("DIALOG_ID"))
	}
}

// TestChatMissing: у недоступного чата портал отдаёт на месте результата пустой
// массив, а не объект. Строгий разбор падал бы с ошибкой разбора, а её клиент
// считает сбоем связи и повторяет запрос: опечатка в номере стоила бы трёх
// походов в портал вместо внятного «нет такого чата».
func TestChatMissing(t *testing.T) {
	t.Parallel()

	calls := 0
	c := serve(t, func(string, url.Values) (int, string) {
		calls++
		return http.StatusOK, `{"result":[]}`
	})

	_, err := c.Chat(context.Background(), "999")
	if !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("ошибка %v, хотели ErrChatNotFound", err)
	}
	if calls != 1 {
		t.Errorf("походов в портал %d, хотели 1", calls)
	}
	// Это не сбой портала: *Error транспорт переводит в 502, а опечатка в
	// номере чата — не отказ шлюза.
	var portal *Error
	if errors.As(err, &portal) {
		t.Errorf("отсутствие чата выдано за сбой портала: %v", err)
	}

	// Пустой номер до портала не доходит.
	if _, err := c.Chat(context.Background(), "  "); err == nil {
		t.Error("пустой номер чата принят")
	}
}

// TestChatForbidden: на закрытый для него чат портал отвечает ACCESS_ERROR.
// Это не сбой шлюза и не опечатка — чат существует, просто внутрь не пускают, —
// и разбирается это в портале. Значит, и ошибка должна быть своя.
//
// Открытых линий это не касается: их портал отдаёт по номеру любому сотруднику.
func TestChatForbidden(t *testing.T) {
	t.Parallel()

	calls := 0
	c := serve(t, func(string, url.Values) (int, string) {
		calls++
		return http.StatusOK,
			`{"error":"ACCESS_ERROR","error_description":"You do not have access to the specified dialog"}`
	})

	_, err := c.Chat(context.Background(), "32000")
	if !errors.Is(err, ErrChatForbidden) {
		t.Fatalf("ошибка %v, хотели ErrChatForbidden", err)
	}
	// Повторять отказ в доступе незачем: со второй попытки доступ не появится.
	if calls != 1 {
		t.Errorf("походов в портал %d, хотели 1", calls)
	}
	var portal *Error
	if errors.As(err, &portal) {
		t.Errorf("отказ в доступе выдан за сбой портала: %v", err)
	}
	// Номер в тексте: человек прикрепляет чаты пачкой и должен видеть, какой
	// именно не дался.
	if !strings.Contains(err.Error(), "chat32000") {
		t.Errorf("в ошибке нет номера чата: %v", err)
	}
}

// TestMe: имя владельца вебхука нужно надписи над списком чатов. Ответ
// user.current — объект с ключами в ВЕРХНЕМ регистре и идентификатором строкой:
// декодер методов im.* на нём не работает.
func TestMe(t *testing.T) {
	t.Parallel()

	c := serve(t, func(method string, _ url.Values) (int, string) {
		if method != "user.current" {
			t.Errorf("вызван метод %q", method)
		}
		return http.StatusOK,
			`{"result":{"ID":"3116","NAME":"Александр","LAST_NAME":"Волощук","ACTIVE":true}}`
	})

	u, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if u.ID != 3116 || u.Name != "Александр Волощук" {
		t.Errorf("владелец вебхука: %d %q", u.ID, u.Name)
	}
}

// TestDialogID: номер чата нигде в портале не написан цифрами — его берут из
// адресной строки. Значит, вставленный целиком адрес обязан работать наравне с
// набранным номером: выковыривать «chat28» глазами из ссылки человек не должен.
func TestDialogID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{"28", "chat28"},
		{"  28  ", "chat28"},
		{"chat28", "chat28"},
		{"https://mbtgroup.bitrix24.ru/online/?IM_DIALOG=chat33220", "chat33220"},
		{"/online/?IM_DIALOG=chat19162&hello=1", "chat19162"},
		{"", ""},
		{"мусор", ""},
		// Ссылка без номера диалога — не номер: лучше сказать «не разобрал», чем
		// сходить в портал за первым попавшимся числом из адреса.
		{"https://mbtgroup.bitrix24.ru/company/personal/user/3116/", ""},
	} {
		if got := DialogID(tc.in); got != tc.want {
			t.Errorf("DialogID(%q) = %q, хотели %q", tc.in, got, tc.want)
		}
	}
}

// TestMessagesForbidden: закрытый чат при чтении сообщений отвечает так же, как
// при чтении карточки, и переводиться должен так же.
//
// Разница видна не здесь, а в подтяжке: одна закрытая переписка не должна
// останавливать чтение остальных чатов задачи. Пока отказ приходил обычной
// ошибкой портала, чат задачи, в который человека не добавили, отменял и
// подтяжку открытой линии.
func TestMessagesForbidden(t *testing.T) {
	t.Parallel()

	c := serve(t, func(string, url.Values) (int, string) {
		return http.StatusForbidden,
			`{"error":"ACCESS_ERROR","error_description":"You do not have access to the specified dialog"}`
	})

	_, err := c.Messages(context.Background(), "chat33460", 0, 0)
	if !errors.Is(err, ErrChatForbidden) {
		t.Fatalf("ошибка %v, хотели ErrChatForbidden", err)
	}
	if !strings.Contains(err.Error(), "chat33460") {
		t.Errorf("в ошибке нет номера чата: %v", err)
	}
}
