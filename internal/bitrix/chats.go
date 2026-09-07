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

// Chat — чат портала в том виде, в каком он нужен выпадающему списку при
// создании задачи.
type Chat struct {
	// DialogID — то, что портал ждёт в параметре DIALOG_ID. Именно его мы
	// сохраняем в привязке задачи к чату.
	DialogID string
	ChatID   int
	Title    string

	// Kind — «user» (личная переписка) или «chat» (групповой чат); бывают ещё
	// служебные виды вроде «general».
	Kind string

	// Entity — что за сущностью стоит групповой чат: «LINES» у открытой линии
	// контакт-центра, «TASKS» у чата задачи, пусто у обычного группового.
	//
	// По Kind этого не видно: и открытая линия, и чат задачи — «chat». А
	// разница существенная: в открытой линии говорит клиент.
	Entity string

	// Bot отмечает диалог с ботом портала. Такие чаты в списке не нужны:
	// обновлений по задаче в них не бывает.
	Bot bool

	LastActivity time.Time

	// Preview — начало последнего сообщения, чтобы человек узнал чат в списке.
	Preview string

	Unread int
}

// Group отвечает, групповой ли это чат. Отличие существенное: у группового
// DIALOG_ID выглядит как «chat12», у личного — это числовой идентификатор
// собеседника.
func (c Chat) Group() bool { return strings.HasPrefix(c.DialogID, "chat") }

// Selectable отсеивает то, что предлагать в списке не стоит.
func (c Chat) Selectable() bool { return !c.Bot && c.DialogID != "" }

// Lines отвечает, открытая ли это линия контакт-центра — та, где говорит
// клиент.
//
// Сравнение без учёта регистра: портал отдаёт «LINES», но это его запись, а не
// договор, и завязываться на её вид не стоит.
func (c Chat) Lines() bool { return strings.EqualFold(c.Entity, "LINES") }

// Kinded переводит чат портала в род, которым его помечает реестр.
func (c Chat) Kinded() ChatKindHint {
	switch {
	case c.Lines():
		return HintLines
	case strings.EqualFold(c.Entity, "TASKS"):
		return HintTask
	case c.Group():
		return HintGroup
	}
	return HintPrivate
}

// ChatKindHint — род чата в терминах портала. Отдельный тип, а не domain.ChatKind,
// потому что пакет bitrix о домене реестра не знает и знать не должен: он
// говорит на языке портала, а перевод делает тот, кто их сводит.
type ChatKindHint string

const (
	HintLines   ChatKindHint = "lines"
	HintTask    ChatKindHint = "task"
	HintGroup   ChatKindHint = "chat"
	HintPrivate ChatKindHint = "user"
)

// Chats отдаёт последние чаты пользователя, от которого выдан вебхук.
//
// Ограничение, о котором нужно знать: im.recent.list показывает не все чаты
// портала, а недавние чаты владельца вебхука. Если нужного чата в списке нет,
// человек должен в нём хотя бы раз побывать.
func (c *Client) Chats(ctx context.Context, limit int) ([]Chat, error) {
	if limit <= 0 {
		limit = 50
	}

	// Ответ — объект с полем items, а не массив. Выглядит как мелочь, но
	// декодер, написанный по названию метода, на этом ломается.
	var out struct {
		Items []recentItem `json:"items"`
	}
	params := url.Values{"LIMIT": {strconv.Itoa(limit)}}
	if err := c.Call(ctx, "im.recent.list", params, &out); err != nil {
		return nil, err
	}

	chats := make([]Chat, 0, len(out.Items))
	for _, it := range out.Items {
		if ch, ok := it.chat(); ok {
			chats = append(chats, ch)
		}
	}

	// По убыванию активности: сверху то, где разговор идёт сейчас.
	sort.SliceStable(chats, func(i, j int) bool {
		return chats[i].LastActivity.After(chats[j].LastActivity)
	})
	return chats, nil
}

// recentItem — элемент ответа im.recent.list. Здесь перечислено только то, что
// нам нужно; остальные поля ответа (аватары, напоминания, настройки) опущены
// намеренно.
type recentItem struct {
	// ID приходит то числом, то строкой: у личной переписки это числовой
	// идентификатор собеседника, у группового чата — «chat12». Поэтому здесь
	// сырое значение: и int, и string на этом месте отвалились бы на половине
	// списка.
	ID rawID `json:"id"`

	ChatID  int    `json:"chat_id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	Counter int    `json:"counter"`

	DateLastActivity string `json:"date_last_activity"`
	DateUpdate       string `json:"date_update"`

	Message struct {
		Text string `json:"text"`
		Date string `json:"date"`
	} `json:"message"`

	// User есть только у личных переписок. Признак бота лежит здесь, а не в
	// поле Type: у диалога с ботом портала Type — «user», и отличить его можно
	// только по этому флагу.
	User *struct {
		Name     string `json:"name"`
		LastName string `json:"last_name"`
		Bot      bool   `json:"bot"`
		Type     string `json:"type"`
	} `json:"user"`

	Chat *struct {
		Name       string `json:"name"`
		EntityType string `json:"entity_type"`
	} `json:"chat"`
}

func (it recentItem) chat() (Chat, bool) {
	id := it.ID.string()
	if id == "" {
		return Chat{}, false
	}

	title := caption(it.Title)
	if title == "" && it.Chat != nil {
		title = caption(it.Chat.Name)
	}
	if title == "" {
		title = "Без названия"
	}

	when := parseTime(it.DateLastActivity)
	if when.IsZero() {
		when = parseTime(it.DateUpdate)
	}
	if when.IsZero() {
		when = parseTime(it.Message.Date)
	}

	ch := Chat{
		DialogID:     id,
		ChatID:       it.ChatID,
		Title:        title,
		Kind:         it.Type,
		LastActivity: when,
		Preview:      preview(it.Message.Text),
		Unread:       it.Counter,
	}
	if it.User != nil {
		ch.Bot = it.User.Bot || it.User.Type == "bot"
	}
	if it.Chat != nil {
		ch.Entity = it.Chat.EntityType
	}
	return ch, true
}

// caption готовит название чата.
//
// Через ту же замазку, что и preview, и по более веской причине: название мы не
// только показываем, но и храним в связи задачи с чатом. Чат в портале называют
// как угодно, в том числе адресом вебхука, — а сохранённый секрет из хранилища
// уже не отзовёшь.
func caption(text string) string {
	s, _ := Redact(Plain(text))
	return strings.Join(strings.Fields(s), " ")
}

// preview готовит подпись к чату в списке.
//
// Замазывание здесь не перестраховка: в личном чате портала лежит сообщение с
// адресом вебхука, и без этого токен доступа отрисовался бы прямо в выпадающем
// списке на экране.
func preview(text string) string {
	const max = 120

	s, _ := Redact(Plain(text))
	s = strings.Join(strings.Fields(s), " ")

	if len(s) <= max {
		return s
	}
	// Резать по рунам, а не по байтам: половина буквы в конце строки — это
	// битый UTF-8.
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

// ErrChatNotFound — портал ответил, но такого чата у него нет или он недоступен
// владельцу вебхука.
//
// Отдельно от *Error по той же причине, что и ErrTaskNotFound: *Error означает
// «портал не смог», и транспорт переводит его в 502. Здесь портал сработал
// исправно, а не сошлось названное человеком.
var ErrChatNotFound = errors.New("чат не найден в портале")

// ErrChatForbidden — чат в портале есть, но владельцу вебхука он не виден.
//
// Так отвечает портал на переписку, в которой владелец вебхука не участвует, —
// а переписку контакт-центра ведут операторы, и у каждого она своя. Ошибка
// отдельная, потому что человеку тут нужен не «портал не смог», а точное
// «этот чат ведёт кто-то другой»: исправляется это в портале, а не в реестре.
var ErrChatForbidden = errors.New("чат не виден владельцу вебхука")

// Chat отдаёт один чат портала по идентификатору диалога.
//
// Метод нужен потому, что im.recent.list показывает только недавние чаты
// владельца вебхука. Переписку контакт-центра ведут операторы, и в этот список
// она попадает не всегда: в ней может не быть ни одного сообщения от владельца
// вебхука. Тогда чат остаётся доступным для чтения, но выбрать его из списка
// нельзя — и человек должен иметь возможность назвать его номером.
//
// Принимает и «chat28», и «28»: в адресной строке портала номер стоит без
// приставки, и требовать её от человека, который копирует его глазами, значит
// требовать помнить наше внутреннее соглашение.
func (c *Client) Chat(ctx context.Context, dialogID string) (Chat, error) {
	dialogID = strings.TrimSpace(dialogID)
	if dialogID == "" {
		return Chat{}, &Error{Code: "DIALOG_ID_EMPTY", Description: "не указан чат"}
	}
	if _, err := strconv.Atoi(dialogID); err == nil {
		dialogID = "chat" + dialogID
	}

	var out chatEnvelope
	params := url.Values{"DIALOG_ID": {dialogID}}
	if err := c.Call(ctx, "im.chat.get", params, &out); err != nil {
		var pe *Error
		if errors.As(err, &pe) && strings.EqualFold(pe.Code, "ACCESS_ERROR") {
			return Chat{}, fmt.Errorf("%s: %w", dialogID, ErrChatForbidden)
		}
		return Chat{}, err
	}

	id := out.ID.string()
	if id == "" {
		return Chat{}, fmt.Errorf("%s: %w", dialogID, ErrChatNotFound)
	}

	n, _ := strconv.Atoi(id)
	title := caption(out.Title)
	if title == "" {
		title = caption(out.Name)
	}
	if title == "" {
		title = "Чат " + dialogID
	}
	return Chat{
		DialogID: "chat" + id,
		ChatID:   n,
		Title:    title,
		Kind:     "chat",
		Entity:   out.EntityType,
	}, nil
}

// chatEnvelope — ответ im.chat.get.
//
// Разбор терпимый по той же причине, что и у карточки задачи: у недоступного
// чата портал отдаёт на месте результата пустой массив, а не объект. Обычная
// структура на этом падает с ошибкой разбора, а ошибку разбора клиент считает
// сбоем связи и повторяет запрос — опечатка в номере чата оборачивалась бы
// тремя походами в портал вместо внятного «нет такого чата».
type chatEnvelope struct {
	ID         rawID  `json:"id"`
	Title      string `json:"title"`
	Name       string `json:"name"`
	EntityType string `json:"entity_type"`
}

func (e *chatEnvelope) UnmarshalJSON(b []byte) error {
	type plain chatEnvelope
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		*e = chatEnvelope{}
		return nil
	}
	*e = chatEnvelope(v)
	return nil
}
