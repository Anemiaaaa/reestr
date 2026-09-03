package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/analyst/schema"
	"github.com/Anemiaaaa/reestr/internal/domain"
)

// input — минимальная задача с одним источником. Здесь проверяется разговор со
// шлюзом, а не качество разбора: содержательные случаи разбирает analyst/schema.
func input() analyst.Input {
	return analyst.Input{
		Task: domain.Task{ID: "aura", Title: "Внедрение"},
		Sources: []domain.Source{{
			ID:   "s-1",
			Kind: domain.KindCorrespondence,
			Body: "Договорились перенести срок на 20 июня. " +
				"Доступ к тестовому контуру пока не дали.",
		}},
		Now: time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC),
	}
}

// fakeGateway — подставной шлюз. Отдаёт заготовленный ответ и запоминает
// запрос.
type fakeGateway struct {
	// args — аргументы вызова инструмента. Пусто вместе с body означает, что
	// модель ответила текстом.
	args any
	text string

	// body и status подменяют ответ целиком: у шлюзов свои способы сообщать об
	// отказе, и все они должны доходить до человека объяснением, а не пустотой.
	body   string
	status int

	got     map[string]any
	auth    string
	path    string
	calls   int
	timeout time.Duration
}

func (g *fakeGateway) analyst(t *testing.T) *Analyst {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.calls++
		g.auth = r.Header.Get("Authorization")
		g.path = r.URL.Path

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("тело запроса не прочитано: %v", err)
		}
		if err := json.Unmarshal(raw, &g.got); err != nil {
			t.Errorf("тело запроса не разобрано: %v", err)
		}

		if g.status != 0 {
			w.WriteHeader(g.status)
		}
		w.Header().Set("Content-Type", "application/json")

		if g.body != "" {
			if _, err := io.WriteString(w, g.body); err != nil {
				t.Errorf("ответ не записан: %v", err)
			}
			return
		}

		msg := map[string]any{"role": "assistant", "content": g.text}
		finish := "stop"
		if g.args != nil {
			args, _ := json.Marshal(g.args)
			msg["tool_calls"] = []map[string]any{{
				"id": "call_1", "type": "function",
				"function": map[string]any{
					"name": schema.ToolName, "arguments": string(args),
				},
			}}
			finish = "tool_calls"
		}
		out, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "model": "claude-sonnet-5",
			"choices": []map[string]any{{"index": 0, "finish_reason": finish, "message": msg}},
		})
		if _, err := w.Write(out); err != nil {
			t.Errorf("ответ не записан: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a, err := New(Options{BaseURL: srv.URL + "/v1", APIKey: "sk-test", Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestExtract(t *testing.T) {
	t.Parallel()

	g := &fakeGateway{args: map[string]any{
		"stage": map[string]any{
			"text": "в работе", "origin": "quoted", "sourceId": "s-1",
			"quote": "Доступ к тестовому контуру пока не дали",
		},
		"goalClarified": map[string]any{"origin": "missing", "note": "уточнить у заказчика"},
		"shifts": []any{map[string]any{
			"at": "2026-06-08", "from": "2026-06-08", "to": "2026-06-20", "comment": "правки",
		}},
	}}

	out, err := g.analyst(t).Extract(context.Background(), input())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	switch {
	case out.Stage.Origin != domain.OriginQuoted || out.Stage.Text != "в работе":
		t.Errorf("этап: %+v", out.Stage)
	case out.GoalClarified.Origin != domain.OriginMissing:
		t.Errorf("уточнённая цель: %+v", out.GoalClarified)
	case len(out.Shifts) != 1:
		t.Errorf("переносы: %+v", out.Shifts)
	}
}

// TestRequestShape: разбор нужен схемой, а не рассказом о задаче. Форма запроса
// у шлюза своя, и разойтись с ней легко: аргументы инструмента здесь приезжают
// строкой, ключ идёт заголовком Bearer, а схема лежит в parameters.
func TestRequestShape(t *testing.T) {
	t.Parallel()

	g := &fakeGateway{args: map[string]any{}}
	if _, err := g.analyst(t).Extract(context.Background(), input()); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if g.path != "/v1/chat/completions" {
		t.Errorf("адрес = %q", g.path)
	}
	if g.auth != "Bearer sk-test" {
		t.Errorf("ключ передан не так: %q", g.auth)
	}
	if g.got["model"] != "claude-sonnet-5" {
		t.Errorf("модель = %v", g.got["model"])
	}

	tools, _ := g.got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("инструментов %d, хотели 1", len(tools))
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != schema.ToolName {
		t.Errorf("имя инструмента = %v", fn["name"])
	}
	if fn["strict"] != true {
		t.Errorf("строгий режим не включён: %v", fn["strict"])
	}

	params, _ := fn["parameters"].(map[string]any)
	if params["additionalProperties"] != false {
		t.Errorf("лишние поля не запрещены: %v", params["additionalProperties"])
	}
	required, _ := params["required"].([]any)
	props, _ := params["properties"].(map[string]any)
	if len(required) == 0 || len(required) != len(props) {
		t.Errorf("обязательных полей %d, полей схемы %d", len(required), len(props))
	}

	choice, _ := g.got["tool_choice"].(map[string]any)
	named, _ := choice["function"].(map[string]any)
	if choice["type"] != "function" || named["name"] != schema.ToolName {
		t.Errorf("выбор инструмента не задан жёстко: %v", g.got["tool_choice"])
	}

	// Правила едут отдельным системным сообщением, задание — пользовательским.
	messages, _ := g.got["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("сообщений %d, хотели 2", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	second, _ := messages[1].(map[string]any)
	if first["role"] != "system" || second["role"] != "user" {
		t.Errorf("роли сообщений: %v, %v", first["role"], second["role"])
	}
	rules, _ := first["content"].(string)
	for _, want := range []string{"quoted", "derived", "missing", "computed"} {
		if !strings.Contains(rules, want) {
			t.Errorf("в правилах нет %q", want)
		}
	}
	task, _ := second["content"].(string)
	if !strings.Contains(task, "s-1") {
		t.Error("в задании нет номера источника — ссылаться модели не на что")
	}
}

// TestGatewayErrors: у шлюзов свои способы сообщать об отказе, и все они должны
// доходить до человека объяснением. Молчаливый пустой разбор читается как «в
// источниках ничего нет» — это худший из возможных ответов на сбой.
func TestGatewayErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		g      fakeGateway
		expect string
	}{
		{
			name:   "отказ кодом состояния",
			g:      fakeGateway{status: 401, body: `{"error":{"message":"unauthorized client detected"}}`},
			expect: "unauthorized client detected",
		},
		{
			// Часть шлюзов отвечает 200 и кладёт ошибку в тело. Принять это за
			// успех значит записать в срез пустоту как результат разбора.
			name:   "ошибка в теле при коде 200",
			g:      fakeGateway{body: `{"error":{"message":"недостаточно средств"}}`},
			expect: "недостаточно средств",
		},
		{
			name:   "ни одного варианта ответа",
			g:      fakeGateway{body: `{"choices":[]}`},
			expect: "ни одного варианта",
		},
		{
			name:   "не тот формат конверта",
			g:      fakeGateway{body: `не json вовсе`},
			expect: "конверт",
		},
		{
			name:   "модель ответила текстом",
			g:      fakeGateway{text: "Извините, я не могу это разобрать."},
			expect: schema.ToolName,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := tc.g
			_, err := g.analyst(t).Extract(context.Background(), input())
			if err == nil {
				t.Fatal("отказ шлюза принят за разбор")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("ошибка %q не объясняет причину (%q)", err, tc.expect)
			}
		})
	}
}

func TestNewRequiresSettings(t *testing.T) {
	t.Parallel()

	full := Options{BaseURL: "https://шлюз/v1", APIKey: "sk-test", Model: "claude-sonnet-5"}
	for _, tc := range []struct {
		name string
		drop func(*Options)
	}{
		{"без адреса", func(o *Options) { o.BaseURL = "" }},
		{"без ключа", func(o *Options) { o.APIKey = "" }},
		// Модель не подставляется по умолчанию: за подстановку платит заказчик.
		{"без модели", func(o *Options) { o.Model = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			o := full
			tc.drop(&o)
			if _, err := New(o); err == nil {
				t.Error("аналитик создан с неполными настройками")
			}
		})
	}

	// Лишняя косая черта в адресе — обычная опечатка в .env, и ронять из-за неё
	// запуск незачем.
	a, err := New(Options{BaseURL: "https://шлюз/v1/", APIKey: "sk", Model: "m"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.base != "https://шлюз/v1" {
		t.Errorf("адрес = %q", a.base)
	}
}

func TestNoSources(t *testing.T) {
	t.Parallel()

	g := &fakeGateway{args: map[string]any{}}
	a := g.analyst(t)

	in := input()
	in.Sources = nil
	if _, err := a.Extract(context.Background(), in); err == nil {
		t.Fatal("разбор без источников прошёл")
	}
	// За вызов, в котором нечего разбирать, платит заказчик.
	if g.calls != 0 {
		t.Errorf("походов к шлюзу %d, хотели 0", g.calls)
	}
}
