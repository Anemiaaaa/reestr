package bitrix

import (
	"strings"
	"unicode"
)

// Plain превращает текст сообщения Bitrix в обычный текст: разметка снимается,
// содержимое остаётся.
//
// Зачем это делается один раз, на входе, а не при показе: срез обязан цитировать
// источник дословно, и проверка цитаты сравнивает её с телом источника. Если в
// теле лежит «[URL]https://…[/URL]», а модель процитирует то, что видит человек,
// цитата не совпадёт ни с чем, и верный факт будет отброшен как выдуманный.
// Поэтому источник хранит текст в том виде, в каком его читают.
//
// Разбор намеренно осторожный: снимаются только известные теги, всё остальное
// проходит насквозь как есть. В переписке встречается «[важно]», «[1]» и
// «[тут был файл]» — это слова человека, а не разметка, и портить их нельзя.
func Plain(s string) string {
	if s == "" {
		return ""
	}

	var (
		out   strings.Builder
		stack []mark
	)
	out.Grow(len(s))

	for i := 0; i < len(s); {
		if s[i] != '[' {
			out.WriteByte(s[i])
			i++
			continue
		}

		// Закрывающая скобка ищется в пределах разумного: длинный текст в
		// квадратных скобках — это текст, а не тег.
		end := closing(s, i)
		if end < 0 {
			out.WriteByte(s[i])
			i++
			continue
		}

		name, attr, closer := parseTag(s[i+1 : end])
		rule, known := tags[name]
		if !known {
			// Не наш тег — отдаём как есть, вместе со скобками.
			out.WriteString(s[i : end+1])
			i = end + 1
			continue
		}

		if closer {
			stack = closeTag(&out, stack, name, rule)
			i = end + 1
			continue
		}

		switch rule.open {
		case openBreak:
			out.WriteByte('\n')
		case openItem:
			out.WriteString("\n— ")
		case openKeep:
			if rule.paired {
				stack = append(stack, mark{name: name, attr: attr, at: out.Len()})
			}
		case openDrop:
			if rule.paired {
				// Содержимое парного тега, который целиком выбрасывается,
				// всё равно пишется в буфер: закрытие обрежет его по метке.
				stack = append(stack, mark{name: name, attr: attr, at: out.Len(), drop: true})
			}
		}
		i = end + 1
	}

	return tidy(out.String())
}

// mark помнит, где в выводе начался парный тег: на закрытии нужно посмотреть,
// что оказалось внутри.
type mark struct {
	name string
	attr string
	at   int
	drop bool
}

type openKind int

const (
	openKeep  openKind = iota // тег снять, содержимое оставить
	openDrop                  // выбросить и тег, и содержимое
	openBreak                 // перевод строки
	openItem                  // пункт списка
)

type rule struct {
	open   openKind
	paired bool
	// link — правило для тегов, у которых значение тега важнее содержимого:
	// «[URL=адрес]текст[/URL]» показывается как «текст (адрес)», но только если
	// текст и адрес действительно разные.
	link bool
}

// tags — полный список того, что мы берёмся понимать. Имя тега в нижнем
// регистре; всё, чего здесь нет, остаётся в тексте нетронутым.
var tags = map[string]rule{
	"br": {open: openBreak},
	"*":  {open: openItem},

	// Оформление: смысла не несёт, содержимое несёт.
	"b":      {open: openKeep, paired: true},
	"i":      {open: openKeep, paired: true},
	"u":      {open: openKeep, paired: true},
	"s":      {open: openKeep, paired: true},
	"code":   {open: openKeep, paired: true},
	"quote":  {open: openKeep, paired: true},
	"color":  {open: openKeep, paired: true},
	"size":   {open: openKeep, paired: true},
	"font":   {open: openKeep, paired: true},
	"list":   {open: openKeep, paired: true},
	"table":  {open: openKeep, paired: true},
	"tr":     {open: openKeep, paired: true},
	"td":     {open: openKeep, paired: true},
	"rating": {open: openKeep, paired: true},

	// Ссылки и упоминания: остаётся то, что видит человек.
	"url":  {open: openKeep, paired: true, link: true},
	"user": {open: openKeep, paired: true},
	"chat": {open: openKeep, paired: true},
	"send": {open: openKeep, paired: true},
	"put":  {open: openKeep, paired: true},
	"call": {open: openKeep, paired: true},

	// Служебные пометки вложений: файл сам приходит отдельным полем ответа,
	// а в тексте от него остаётся только метка — она читателю ничего не даёт.
	"icon":         {open: openDrop, paired: true},
	"attach":       {open: openDrop, paired: true},
	"disk file id": {open: openDrop},
}

// closing находит закрывающую квадратную скобку тега. Ограничение по длине —
// защита от того, чтобы принять за тег абзац, начинающийся со скобки.
func closing(s string, from int) int {
	const maxTag = 128

	limit := from + maxTag
	if limit > len(s) {
		limit = len(s)
	}
	for i := from + 1; i < limit; i++ {
		switch s[i] {
		case ']':
			if i == from+1 {
				return -1 // «[]» — не тег
			}
			return i
		case '[', '\n':
			return -1
		}
	}
	return -1
}

// parseTag разбирает содержимое скобок на имя, значение и признак закрывающего
// тега: «/URL» → («url», "", true), «URL=адрес» → («url», «адрес», false).
func parseTag(body string) (name, attr string, closer bool) {
	if strings.HasPrefix(body, "/") {
		closer = true
		body = body[1:]
	}
	if eq := strings.IndexByte(body, '='); eq >= 0 {
		name, attr = body[:eq], strings.TrimSpace(body[eq+1:])
	} else {
		name = body
	}
	return strings.ToLower(strings.TrimSpace(name)), attr, closer
}

// closeTag снимает парный тег и решает, что делать с тем, что оказалось внутри.
func closeTag(out *strings.Builder, stack []mark, name string, r rule) []mark {
	if !r.paired {
		return stack
	}

	// Ищем ближайший открытый тег с тем же именем. Непарные закрытия в
	// переписке случаются, и падать из-за них незачем.
	top := -1
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].name == name {
			top = i
			break
		}
	}
	if top < 0 {
		return stack
	}

	m := stack[top]
	text := out.String()
	inner := text[m.at:]

	switch {
	case m.drop:
		rewind(out, text[:m.at])
	case r.link:
		rewind(out, text[:m.at]+linkText(m.attr, inner))
	}
	return stack[:top]
}

// linkText собирает читаемую ссылку. Адрес дописывается только тогда, когда он
// добавляет знание: «[URL=x]x[/URL]» — это просто «x», и дублировать нечего.
func linkText(addr, inner string) string {
	switch {
	case strings.TrimSpace(inner) == "":
		return addr
	case addr == "" || strings.TrimSpace(inner) == addr:
		return inner
	default:
		return inner + " (" + addr + ")"
	}
}

// rewind — единственный способ откатить уже записанное: strings.Builder не
// умеет усекаться, поэтому он пересобирается. Дёшево: это происходит только на
// закрытии тега, а не на каждом символе.
func rewind(out *strings.Builder, keep string) {
	out.Reset()
	out.WriteString(keep)
}

// tidy убирает следы снятой разметки: висящие пробелы в конце строк и пустоту,
// оставшуюся от выброшенных тегов. Больше двух переводов строки подряд не
// оставляем — абзац есть, а дырки в тексте не нужны.
func tidy(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")

	kept := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			blank++
			if blank > 1 || len(kept) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		kept = append(kept, line)
	}

	return strings.TrimSpace(strings.Join(kept, "\n"))
}
