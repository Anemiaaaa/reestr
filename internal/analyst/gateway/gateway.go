// Package gateway — разбор источников моделью через шлюз формата OpenAI.
//
// Формат выбран не из любви к OpenAI, а потому что на нём говорят все
// доступные из России агрегаторы: HydraAI, Polza, AllTokens, llmgw, GigaChat.
// Написав этот транспорт один раз, реестр перестаёт зависеть от конкретного
// поставщика — а зависимость от одного поставщика уже однажды обошлась дорого,
// когда прежний шлюз перестал принимать ключ.
//
// Клиент здесь свой, а не из SDK. Причина та же, что у клиента Bitrix24: нужен
// один метод одного эндпоинта, и тянуть ради него зависимость с моделью всего
// API дороже, чем написать сорок строк. Anthropic SDK в проекте есть, но он
// говорит на другом формате и здесь бесполезен.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/analyst/schema"
)

const (
	// timeout на один вызов. Разбор длинный: модель читает всю переписку и
	// пишет ответ на несколько тысяч токенов. Обрывать её на тридцатой секунде
	// значит платить за вызов и не получать ответа.
	timeout = 5 * time.Minute

	// maxTokens — потолок ответа. Скупой потолок обрубает JSON на середине, и
	// вместо разбора получается ошибка формата, неотличимая от сбоя модели.
	maxTokens = 16000
)

// Options — настройки подключения к шлюзу.
type Options struct {
	// BaseURL — адрес шлюза вместе с версией пути, например
	// https://apihydraai.ru/v1. Обязателен: у шлюзов нет общего адреса по
	// умолчанию, и угадывать его нечем.
	BaseURL string
	APIKey  string

	// Model — идентификатор модели у этого шлюза. Имена у шлюзов свои
	// («claude-sonnet-5» вместо «claude-sonnet-5-20260514»), поэтому значение
	// приходит настройкой, а не константой.
	Model string

	Log *slog.Logger
}

// Analyst — разбор источников моделью через шлюз.
type Analyst struct {
	base  string
	key   string
	model string
	http  *http.Client
	log   *slog.Logger
}

// Проверка на этапе компиляции.
var _ analyst.Analyst = (*Analyst)(nil)

// New собирает аналитика на шлюзе.
func New(o Options) (*Analyst, error) {
	base := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	switch {
	case base == "":
		return nil, fmt.Errorf("не задан адрес шлюза")
	case strings.TrimSpace(o.APIKey) == "":
		return nil, fmt.Errorf("не задан ключ шлюза")
	case strings.TrimSpace(o.Model) == "":
		// Модель не подставляется по умолчанию: за подстановку платит заказчик,
		// и молча выбирать её за него нельзя.
		return nil, fmt.Errorf("не задана модель")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}

	return &Analyst{
		base:  base,
		key:   strings.TrimSpace(o.APIKey),
		model: strings.TrimSpace(o.Model),
		http:  &http.Client{Timeout: timeout},
		log:   o.Log,
	}, nil
}

// Name возвращает имя реализации: читатель среза должен видеть, какая модель
// его собрала — от версии зависят и качество разбора, и цена.
func (a *Analyst) Name() string { return "модель " + a.model }

// Extract разбирает источники задачи.
func (a *Analyst) Extract(ctx context.Context, in analyst.Input) (analyst.Output, error) {
	if len(in.Sources) == 0 {
		return analyst.Output{}, fmt.Errorf("нет источников для разбора")
	}

	body, err := json.Marshal(request{
		Model:     a.model,
		MaxTokens: maxTokens,
		Messages: []message{
			{Role: "system", Content: schema.SystemPrompt},
			{Role: "user", Content: schema.UserPrompt(in)},
		},
		Tools: []tool{{
			Type: "function",
			Function: function{
				Name:        schema.ToolName,
				Description: schema.ToolDescription,
				Parameters:  schema.JSONSchema(),
				// Строгий режим: шлюз обязан не пропускать ответ, не сходящийся
				// со схемой. Поддерживают его не все, поэтому на нём одном
				// ничего не держится — за формой ответа всё равно следит
				// проверка в analyst/schema.
				Strict: true,
			},
		}},
		// Выбор инструмента задан жёстко: разбор нужен схемой, а не рассказом о
		// задаче.
		ToolChoice: toolChoice{
			Type:     "function",
			Function: namedFunction{Name: schema.ToolName},
		},
	})
	if err != nil {
		return analyst.Output{}, fmt.Errorf("сборка запроса: %w", err)
	}

	raw, err := a.call(ctx, body)
	if err != nil {
		return analyst.Output{}, err
	}

	var got schema.Answer
	if err := json.Unmarshal(raw, &got); err != nil {
		return analyst.Output{}, fmt.Errorf("разбор ответа модели: %w", err)
	}

	out, rejected := schema.Convert(got, in)
	schema.Report(a.log, in.Task.ID, rejected)
	return out, nil
}

// call делает запрос и достаёт из ответа аргументы вызова инструмента.
func (a *Analyst) call(ctx context.Context, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("запрос к шлюзу: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.key)

	resp, err := a.http.Do(req)
	if err != nil {
		// Адрес шлюза в текст ошибки не попадает: ключ в заголовке, но адрес
		// вместе с ним уходит в логи, а логи читают не только свои.
		return nil, fmt.Errorf("шлюз не ответил: %w", err)
	}
	defer resp.Body.Close()

	// Тело читается целиком в любом случае: у ошибки шлюза оно объясняет
	// причину, и потерять его значит остаться с голым кодом состояния.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("чтение ответа шлюза: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("шлюз ответил %d: %s", resp.StatusCode, snippet(data))
	}

	var out response
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("разбор конверта шлюза: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		// Часть шлюзов отвечает 200 и кладёт ошибку в тело. Пропустить это
		// значит принять пустой разбор за успешный.
		return nil, fmt.Errorf("шлюз вернул ошибку: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("шлюз не вернул ни одного варианта ответа")
	}

	choice := out.Choices[0]
	for _, c := range choice.Message.ToolCalls {
		if c.Function.Name == schema.ToolName {
			return json.RawMessage(c.Function.Arguments), nil
		}
	}

	// Модель ответила не тем, чем просили. Отдельная ошибка, а не пустой
	// разбор: пустой разбор читается как «в источниках ничего нет», и разница
	// между «нечего сказать» и «не сработало» потерялась бы.
	return nil, fmt.Errorf("модель не вызвала инструмент %s (причина остановки %q, ответ: %s)",
		schema.ToolName, choice.FinishReason, snippet([]byte(choice.Message.Content)))
}

// snippet укорачивает чужой текст для сообщения об ошибке: целиком он бывает в
// мегабайт, а в логе нужен признак причины, а не дамп.
func snippet(b []byte) string {
	const max = 300

	s := strings.Join(strings.Fields(string(b)), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// --- форма запроса и ответа ---

type request struct {
	Model      string     `json:"model"`
	MaxTokens  int        `json:"max_tokens"`
	Messages   []message  `json:"messages"`
	Tools      []tool     `json:"tools"`
	ToolChoice toolChoice `json:"tool_choice"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type function struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

type toolChoice struct {
	Type     string        `json:"type"`
	Function namedFunction `json:"function"`
}

type namedFunction struct {
	Name string `json:"name"`
}

type response struct {
	Choices []choice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type choice struct {
	FinishReason string `json:"finish_reason"`
	Message      struct {
		Content   string     `json:"content"`
		ToolCalls []toolCall `json:"tool_calls"`
	} `json:"message"`
}

type toolCall struct {
	Function struct {
		Name string `json:"name"`
		// Arguments — JSON строкой, а не объектом: так задан формат OpenAI.
		// Разбирать её приходится вторым проходом, и это не наша прихоть.
		Arguments string `json:"arguments"`
	} `json:"function"`
}
