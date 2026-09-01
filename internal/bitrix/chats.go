package bitrix

import (
	"context"
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
