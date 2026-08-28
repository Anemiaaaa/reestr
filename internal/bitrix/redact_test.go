package bitrix

import "testing"

// Токен вебхука лежит в тексте сообщений самого портала, поэтому замазывание —
// не перестраховка, а часть пути данных. Проверяется и то, что ключ убирается, и
// то, что обычные адреса портала при этом не портятся: «/rest/1/im.recent.list»
// должен остаться читаемым.
func TestRedact(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		hidden bool
	}{
		{
			name:   "адрес вебхука",
			in:     "URL: https://b24-lhajtv.bitrix24.ru/rest/1/abcdef1234567890/profile.json",
			want:   "URL: https://b24-lhajtv.bitrix24.ru/rest/1/***/profile.json",
			hidden: true,
		},
		{
			name:   "без завершающего пути",
			in:     "https://portal.example.ru/rest/12/qwerty0987654321",
			want:   "https://portal.example.ru/rest/12/***",
			hidden: true,
		},
		{
			name:   "верхний регистр",
			in:     "HTTPS://PORTAL/REST/1/ABCDEF1234567890/",
			want:   "HTTPS://PORTAL/REST/1/***/",
			hidden: true,
		},
		{
			name:   "два адреса в одном тексте",
			in:     "старый /rest/1/aaaaaaaaaa1 и новый /rest/1/bbbbbbbbbb2",
			want:   "старый /rest/1/*** и новый /rest/1/***",
			hidden: true,
		},
		{
			name: "обычный текст",
			in:   "Смета согласована, начинаем в понедельник",
			want: "Смета согласована, начинаем в понедельник",
		},
		{
			// Имя метода — не секрет, и превращать его в «***» нельзя: по такому
			// адресу в логе разбирают, что именно вызывалось.
			name: "имя метода не трогаем",
			in:   "вызов /rest/1/im.recent.list",
			want: "вызов /rest/1/im.recent.list",
		},
		{
			name: "короткий сегмент",
			in:   "/rest/1/tasks",
			want: "/rest/1/tasks",
		},
		{
			name: "пусто",
			in:   "",
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, hidden := Redact(c.in)
			if got != c.want {
				t.Errorf("Redact(%q)\nполучено %q\nожидалось %q", c.in, got, c.want)
			}
			if hidden != c.hidden {
				t.Errorf("Redact(%q) вернул hidden=%v, ожидалось %v", c.in, hidden, c.hidden)
			}
		})
	}
}

// TestRedactAfterPlain закрепляет порядок обработки: сначала снимается разметка,
// потом замазывается ключ. Обратный порядок оставил бы токен в тексте, потому что
// внутри «[URL]…[/URL]» он выглядит иначе.
func TestRedactAfterPlain(t *testing.T) {
	const in = "Вебхук: [URL]https://b24.example.ru/rest/1/abcdef1234567890/[/URL]"

	got, hidden := Redact(Plain(in))

	if !hidden {
		t.Fatalf("ключ не найден в %q", got)
	}
	if want := "Вебхук: https://b24.example.ru/rest/1/***/"; got != want {
		t.Errorf("получено %q, ожидалось %q", got, want)
	}
}
