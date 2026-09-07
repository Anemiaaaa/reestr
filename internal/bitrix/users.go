package bitrix

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// User — сотрудник портала. Нужен только для подписи автора, поэтому кроме имени
// и должности здесь ничего нет.
type User struct {
	ID       int
	Name     string
	Position string
	Active   bool
}

// User читает карточку сотрудника.
//
// Метод user.get отвечает не так, как методы im.*: ключи в ВЕРХНЕМ регистре, а
// идентификатор приходит строкой («"ID":"1"»). Смешивать эти два соглашения в
// одном декодере нельзя, поэтому разбор здесь свой.
//
// Основной источник имён — блок users в ответе im.dialog.messages.get, он
// приходит вместе с сообщениями. Этот вызов остаётся на случай, когда автор в
// том блоке не пришёл: например, сотрудник уволен и скрыт из выдачи.
func (c *Client) User(ctx context.Context, id int) (User, error) {
	if id <= 0 {
		return User{}, &Error{Code: "USER_ID_EMPTY", Description: "не указан сотрудник"}
	}

	var out []struct {
		ID           string `json:"ID"`
		Name         string `json:"NAME"`
		LastName     string `json:"LAST_NAME"`
		SecondName   string `json:"SECOND_NAME"`
		WorkPosition string `json:"WORK_POSITION"`
		Active       bool   `json:"ACTIVE"`
	}
	params := url.Values{"ID": {strconv.Itoa(id)}}
	if err := c.Call(ctx, "user.get", params, &out); err != nil {
		return User{}, err
	}
	if len(out) == 0 {
		return User{}, &Error{Code: "USER_NOT_FOUND", Description: "сотрудник не найден"}
	}

	u := out[0]
	num, _ := strconv.Atoi(strings.TrimSpace(u.ID))
	if num == 0 {
		num = id
	}
	return User{
		ID:       num,
		Name:     fullName(u.Name, u.LastName),
		Position: strings.TrimSpace(u.WorkPosition),
		Active:   u.Active,
	}, nil
}

// Me читает карточку того сотрудника, от чьего имени выдан вебхук.
//
// Метод нужен ради одной надписи, и надпись эта важная. Вебхук выдаёт
// конкретный человек, и портал показывает реестру ровно его переписку: чаты
// коллег в im.recent.list не попадают, а прочитать их по номеру нельзя —
// портал отвечает отказом в доступе. Пока в списке чатов не написано, чьи это
// чаты, отсутствие своей переписки выглядит как поломка реестра.
//
// Ответ user.current — объект, а не массив, как у user.get; в остальном
// соглашение то же самое: ключи в ВЕРХНЕМ регистре, идентификатор строкой.
func (c *Client) Me(ctx context.Context) (User, error) {
	var out struct {
		ID           string `json:"ID"`
		Name         string `json:"NAME"`
		LastName     string `json:"LAST_NAME"`
		WorkPosition string `json:"WORK_POSITION"`
		Active       bool   `json:"ACTIVE"`
	}
	if err := c.Call(ctx, "user.current", nil, &out); err != nil {
		return User{}, err
	}

	id, _ := strconv.Atoi(strings.TrimSpace(out.ID))
	return User{
		ID:       id,
		Name:     fullName(out.Name, out.LastName),
		Position: strings.TrimSpace(out.WorkPosition),
		Active:   out.Active,
	}, nil
}

// Names дособирает имена для тех авторов, которых не оказалось в ответе вместе с
// сообщениями.
//
// Ошибка по отдельному сотруднику не прерывает работу: без подписи автора срез
// собрать можно, а без сообщений — нет. Неудачи возвращаются вызывающему, чтобы
// он записал их в лог, а не чтобы останавливаться.
func (c *Client) Names(ctx context.Context, ids []int) (map[int]string, []error) {
	const maxLookups = 20

	names := make(map[int]string, len(ids))
	var problems []error

	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true

		if len(names)+len(problems) >= maxLookups {
			break
		}
		u, err := c.User(ctx, id)
		if err != nil {
			problems = append(problems, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if u.Name != "" {
			names[id] = u.Name
		}
	}
	return names, problems
}

// fullName собирает подпись автора. Фамилия у портала бывает пустой и бывает
// null — и то, и другое означает «фамилии нет».
func fullName(first, last string) string {
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	switch {
	case first == "":
		return last
	case last == "":
		return first
	default:
		return first + " " + last
	}
}
