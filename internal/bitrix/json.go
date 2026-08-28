package bitrix

import (
	"encoding/json"
	"strconv"
	"strings"
)

// rawID — идентификатор, который портал присылает то числом, то строкой.
//
// В im.recent.list поле «id» у личной переписки приходит числом (20), а у
// группового чата — строкой («chat12»). Обычное поле int отвалилось бы на
// групповых чатах, обычное string — на личных, причём с ошибкой на весь ответ:
// один неудобный элемент обнулил бы весь список.
type rawID json.RawMessage

func (r *rawID) UnmarshalJSON(b []byte) error {
	*r = rawID(append([]byte(nil), b...))
	return nil
}

// string нормализует идентификатор к тому виду, в каком его ждёт параметр
// DIALOG_ID: «chat12» остаётся строкой, 20 становится «20».
func (r rawID) string() string {
	s := strings.TrimSpace(string(r))
	switch {
	case s == "" || s == "null":
		return ""
	case strings.HasPrefix(s, `"`):
		var out string
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	default:
		// Число может прийти и как 20, и как 20.0 — второе от PHP вполне
		// ожидаемо.
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return strconv.FormatInt(int64(n), 10)
		}
		return ""
	}
}

// params — служебные параметры сообщения.
//
// Форма поля непостоянна: у сообщения с параметрами это объект, а у обычного —
// пустой массив «[]» (так PHP отдаёт пустой ассоциативный массив). Поле
// map[string]any на этом падало бы на каждом втором сообщении, поэтому разбор
// терпимый: не разобралось — считаем, что параметров нет.
type params map[string]json.RawMessage

func (p *params) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		*p = nil
		return nil
	}
	*p = m
	return nil
}

// has сообщает, есть ли непустой параметр с таким именем.
func (p params) has(key string) bool {
	raw, ok := p[key]
	if !ok {
		return false
	}
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""`
}
