package claude

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

// fakeModel — подставная модель. Отдаёт заготовленный ответ и запоминает
// запрос, чтобы его можно было рассмотреть.
type fakeModel struct {
	// reply — то, что окажется в аргументах вызова инструмента. Пусто —
	// ответить текстом, как если бы модель схему проигнорировала.
	reply any

	text string

	got     map[string]any
	calls   int
	headers http.Header
}

func (m *fakeModel) analyst(t *testing.T) *Analyst {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls++
		m.headers = r.Header.Clone()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("тело запроса не прочитано: %v", err)
		}
		if err := json.Unmarshal(body, &m.got); err != nil {
			t.Errorf("тело запроса не разобрано: %v", err)
		}

		var content []map[string]any
		if m.reply != nil {
			content = append(content, map[string]any{
				"type": "tool_use", "id": "tu_1", "name": schema.ToolName, "input": m.reply,
			})
		} else {
			content = append(content, map[string]any{"type": "text", "text": m.text})
		}

		out, _ := json.Marshal(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant",
			"model": "claude-opus-4-8", "content": content,
			"stop_reason": "tool_use",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 20},
		})
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(out); err != nil {
			t.Errorf("ответ не записан: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a, err := New(Options{BaseURL: srv.URL, APIKey: "sk-test", Model: "claude-opus-4-8"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestExtract(t *testing.T) {
	t.Parallel()

	m := &fakeModel{reply: map[string]any{
		"stage": map[string]any{
			"text": "в работе", "origin": "quoted", "sourceId": "s-1",
			"quote": "Доступ к тестовому контуру пока не дали",
		},
		"goalAsStated": map[string]any{
			"text": "интеграция с 1С", "origin": "quoted", "sourceId": "s-1",
			"quote": "без интеграции с 1С принимать не будем",
		},
		"goalClarified": map[string]any{"origin": "missing", "note": "уточнить у заказчика"},
		"blockers": []any{map[string]any{
			"summary": "нет доступа к контуру", "kind": "no_access", "since": "2026-06-08",
			"evidence": map[string]any{
				"text": "доступа нет", "origin": "quoted", "sourceId": "s-1",
				"quote": "Доступ к тестовому контуру пока не дали",
			},
		}},
	}}

	out, err := m.analyst(t).Extract(context.Background(), input())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	switch {
	case out.Stage.Text != "в работе" || out.Stage.Origin != domain.OriginQuoted:
		t.Errorf("этап: %+v", out.Stage)
	case out.GoalClarified.Origin != domain.OriginMissing:
		t.Errorf("уточнённая цель: %+v", out.GoalClarified)
	case len(out.Blockers) != 1 || out.Blockers[0].Kind != domain.BlockerNoAccess:
		t.Errorf("блокеры: %+v", out.Blockers)
	}
}

// TestRequestShape: разбор нужен схемой, а не рассказом о задаче. Если из
// запроса пропадёт инструмент, его строгость или жёсткий выбор, модель начнёт
// отвечать текстом — и разбор сломается не здесь, а на живом портале.
func TestRequestShape(t *testing.T) {
	t.Parallel()

	m := &fakeModel{reply: map[string]any{}}
	if _, err := m.analyst(t).Extract(context.Background(), input()); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	tools, _ := m.got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("инструментов %d, хотели 1: %v", len(tools), m.got["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != schema.ToolName {
		t.Errorf("имя инструмента = %v", tool["name"])
	}
	if tool["strict"] != true {
		t.Errorf("строгий режим не включён: %v", tool["strict"])
	}

	// Строгий режим требует и запрета лишних полей, и списка обязательных.
	inputSchema, _ := tool["input_schema"].(map[string]any)
	if inputSchema["additionalProperties"] != false {
		t.Errorf("лишние поля не запрещены: %v", inputSchema["additionalProperties"])
	}
	required, _ := inputSchema["required"].([]any)
	props, _ := inputSchema["properties"].(map[string]any)
	if len(required) != len(props) {
		t.Errorf("обязательных полей %d, полей схемы %d: пропущенный раздел нельзя "+
			"отличить от пустого", len(required), len(props))
	}

	choice, _ := m.got["tool_choice"].(map[string]any)
	if choice["type"] != "tool" || choice["name"] != schema.ToolName {
		t.Errorf("выбор инструмента не задан жёстко: %v", m.got["tool_choice"])
	}

	// Правила разбора едут в системном промпте. Без них модель выдумывает
	// цитаты, и проверка отбрасывает почти всё — вызов оплачен, срез пуст.
	system, _ := m.got["system"].([]any)
	if len(system) == 0 {
		t.Fatalf("системного промпта нет: %v", m.got["system"])
	}
	first, _ := system[0].(map[string]any)
	rules, _ := first["text"].(string)
	for _, want := range []string{"quoted", "derived", "missing", "computed", "ГГГГ-ММ-ДД"} {
		if !strings.Contains(rules, want) {
			t.Errorf("в правилах нет %q", want)
		}
	}
}

// TestPromptCarriesSources: модель ссылается на источники по номерам, и номера
// обязаны быть в задании. Иначе сослаться ей не на что, и любая ссылка окажется
// выдуманной.
func TestPromptCarriesSources(t *testing.T) {
	t.Parallel()

	m := &fakeModel{reply: map[string]any{}}
	if _, err := m.analyst(t).Extract(context.Background(), input()); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	messages, _ := m.got["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("сообщений %d, хотели 1", len(messages))
	}
	msg, _ := messages[0].(map[string]any)
	blocks, _ := msg["content"].([]any)
	block, _ := blocks[0].(map[string]any)
	task, _ := block["text"].(string)

	for _, want := range []string{"s-1", "Договорились перенести срок на 20 июня", "2026-08-19"} {
		if !strings.Contains(task, want) {
			t.Errorf("в задании нет %q", want)
		}
	}
}

// TestModelAnsweredWithText: модель ответила не тем, чем просили. Это отдельная
// ошибка, а не пустой разбор: пустой разбор читается как «в источниках ничего
// нет», и разница между «нечего сказать» и «не сработало» потерялась бы.
func TestModelAnsweredWithText(t *testing.T) {
	t.Parallel()

	m := &fakeModel{text: "Извините, я не могу это разобрать."}
	_, err := m.analyst(t).Extract(context.Background(), input())
	if err == nil {
		t.Fatal("текстовый ответ принят за разбор")
	}
	if !strings.Contains(err.Error(), schema.ToolName) {
		t.Errorf("ошибка не называет инструмент: %v", err)
	}
}

// TestNoSources: за вызов, в котором нечего разбирать, платит заказчик, а ответ
// на него был бы выдумкой от первого до последнего поля.
func TestNoSources(t *testing.T) {
	t.Parallel()

	m := &fakeModel{reply: map[string]any{}}
	a := m.analyst(t)

	in := input()
	in.Sources = nil
	if _, err := a.Extract(context.Background(), in); err == nil {
		t.Fatal("разбор без источников прошёл")
	}
	if m.calls != 0 {
		t.Errorf("походов к модели %d, хотели 0", m.calls)
	}
}

func TestNewRequiresKeyAndModel(t *testing.T) {
	t.Parallel()

	if _, err := New(Options{Model: "claude-opus-4-8"}); err == nil {
		t.Error("аналитик создан без ключа")
	}
	// Модель не подставляется по умолчанию намеренно: за подстановку платит
	// заказчик, и выбирать её за него молча нельзя.
	if _, err := New(Options{APIKey: "sk-test"}); err == nil {
		t.Error("аналитик создан без модели")
	}
}

func TestName(t *testing.T) {
	t.Parallel()

	a, err := New(Options{APIKey: "sk-test", Model: "claude-opus-4-8"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Модель названа поимённо: от версии зависят и качество разбора, и цена.
	if !strings.Contains(a.Name(), "claude-opus-4-8") {
		t.Errorf("имя реализации = %q", a.Name())
	}
}

// Отмену контекста здесь не проверяем намеренно. Extract передаёт ctx в SDK
// сквозь, своего поведения у нас на этом пути нет, а подставной сервер,
// ждущий разрыва соединения, вешает весь прогон.

// input — минимальная задача с одним источником. Здесь проверяется форма
// запроса, а не качество разбора, поэтому источник короткий: содержательные
// случаи разбирает analyst/schema.
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
