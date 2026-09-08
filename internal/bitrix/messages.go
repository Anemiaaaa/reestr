package bitrix

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Message — сообщение чата, уже пригодное для превращения в источник: разметка
// снята, токены замазаны, автор назван по имени.
type Message struct {
	ID       int
	ChatID   int
	AuthorID int

	// Author — имя автора, если портал его прислал. Пустая строка означает
	// «автор неизвестен», и это честнее, чем подставлять «Сотрудник №5».
	Author string

	Date time.Time
	Text string

	// Service отмечает системное сообщение портала: «чат создан», «пользователь
	// присоединился». Источником такое становиться не должно — в срезе от него
	// нет ни факта, ни цитаты.
	Service bool

	// Redacted означает, что из текста убран токен доступа. Подтяжка пишет об
	// этом в лог: молча менять текст источника нельзя.
	Redacted bool
}

// Messages читает сообщения диалога.
//
// sinceID — идентификатор последнего уже сохранённого сообщения; 0 означает
// «прочитать конец чата с начала синхронизации».
//
// Про направление обхода стоит сказать отдельно, потому что названия параметров
// портала вводят в заблуждение. Проверено на живом портале:
//
//	FIRST_ID=42 → отдаёт 46, то есть более новые сообщения;
//	LAST_ID=42  → отдаёт 22, 18, 16, 14, 8 — уходит в историю назад.
//
// То есть курсор синхронизации — FIRST_ID. Клиент, написанный «по смыслу
// названия», листал бы архив и никогда не увидел бы нового сообщения.
func (c *Client) Messages(ctx context.Context, dialogID string, sinceID, limit int) ([]Message, error) {
	dialogID = strings.TrimSpace(dialogID)
	if dialogID == "" {
		return nil, &Error{Code: "DIALOG_ID_EMPTY", Description: "не указан чат"}
	}
	if limit <= 0 || limit > 100 {
		// Портал всё равно ограничивает выборку сотней; просить больше
		// бессмысленно, а обещать вызывающему больше — вредно.
		limit = 100
	}

	params := url.Values{
		"DIALOG_ID": {dialogID},
		"LIMIT":     {strconv.Itoa(limit)},
	}
	if sinceID > 0 {
		params.Set("FIRST_ID", strconv.Itoa(sinceID))
	}

	var out struct {
		ChatID   int              `json:"chat_id"`
		Messages []messageItem    `json:"messages"`
		Users    []messageUser    `json:"users"`
		Files    []map[string]any `json:"files"`
	}
	if err := c.Call(ctx, "im.dialog.messages.get", params, &out); err != nil {
		// Отказ в доступе переводим так же, как при чтении карточки чата: это не
		// сбой портала, а закрытая для владельца вебхука переписка. Разница
		// видна вызывающему — подтяжка остальных чатов задачи из-за одного
		// закрытого останавливаться не должна.
		var pe *Error
		if errors.As(err, &pe) && strings.EqualFold(pe.Code, "ACCESS_ERROR") {
			return nil, fmt.Errorf("%s: %w", dialogID, ErrChatForbidden)
		}
		return nil, err
	}

	// Имена приходят отдельным блоком в том же ответе, поэтому отдельный вызов
	// user.get на каждую подтяжку не нужен.
	names := make(map[int]string, len(out.Users))
	for _, u := range out.Users {
		if name := u.name(); name != "" {
			names[u.ID] = name
		}
	}

	msgs := make([]Message, 0, len(out.Messages))
	for _, it := range out.Messages {
		// Подстраховка от смены семантики курсора на стороне портала: то, что
		// мы уже знаем, второй раз в источники не попадёт.
		if sinceID > 0 && it.ID <= sinceID {
			continue
		}
		msgs = append(msgs, it.message(names))
	}

	// Портал отдаёт сообщения от новых к старым (46, затем 42). Наверх слоя
	// это выносить незачем: источники добавляются в порядке событий.
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })
	return msgs, nil
}

type messageItem struct {
	ID       int    `json:"id"`
	ChatID   int    `json:"chat_id"`
	AuthorID int    `json:"author_id"`
	Date     string `json:"date"`
	Text     string `json:"text"`
	Params   params `json:"params"`
}

func (it messageItem) message(names map[int]string) Message {
	text, hidden := Redact(Plain(it.Text))

	return Message{
		ID:       it.ID,
		ChatID:   it.ChatID,
		AuthorID: it.AuthorID,
		Author:   names[it.AuthorID],
		Date:     parseTime(it.Date),
		Text:     text,
		// Системное сообщение портал помечает компонентом отрисовки; кроме
		// того, у него нет автора — author_id равен нулю.
		Service:  it.AuthorID == 0 || it.Params.has("COMPONENT_ID"),
		Redacted: hidden,
	}
}

// messageUser — карточка автора из блока users того же ответа. Ключи здесь в
// нижнем регистре, а идентификатор числовой — в отличие от метода user.get,
// который отвечает совсем другими соглашениями.
type messageUser struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func (u messageUser) name() string {
	if n := strings.TrimSpace(u.Name); n != "" {
		return n
	}
	return strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
}
