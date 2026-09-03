// Package claude — разбор источников моделью через API Anthropic.
//
// Транспорт и ничего больше. Что спрашивать у модели и чему из ответа верить,
// знает analyst/schema; здесь только то, как это доехало до Anthropic и
// обратно.
//
// Рядом лежит analyst/gateway — тот же разбор через шлюз формата OpenAI. Две
// реализации не дублируют друг друга: общего у них схема, промпт и проверка,
// то есть всё, где может завестись ошибка по существу. Разное — форма запроса.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/analyst/schema"
)

// maxTokens — потолок ответа.
//
// Ответ длинный: разделов много, у каждого значения цитата. Скупой потолок
// обрубает JSON на середине, и вместо разбора получается ошибка формата,
// неотличимая от сбоя модели.
const maxTokens = 16000

// Options — настройки подключения к модели.
type Options struct {
	// BaseURL — адрес API. Вынесен в настройку намеренно: переход со шлюза на
	// прямой Anthropic должен быть правкой одной строки, а не кода.
	BaseURL string
	APIKey  string

	// Model — идентификатор модели. Пустая строка — ошибка, а не повод
	// подставить какую-нибудь: за подстановку платит заказчик, и молча выбирать
	// модель за него нельзя.
	Model string

	Log *slog.Logger
}

// Analyst — разбор источников моделью Anthropic.
type Analyst struct {
	client anthropic.Client
	model  string
	log    *slog.Logger
}

// Проверка на этапе компиляции.
var _ analyst.Analyst = (*Analyst)(nil)

// New собирает аналитика на модели.
func New(o Options) (*Analyst, error) {
	if strings.TrimSpace(o.APIKey) == "" {
		return nil, fmt.Errorf("не задан ключ модели")
	}
	if strings.TrimSpace(o.Model) == "" {
		return nil, fmt.Errorf("не задана модель")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}

	opts := []option.RequestOption{option.WithAPIKey(o.APIKey)}
	if base := strings.TrimSpace(o.BaseURL); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}

	return &Analyst{
		client: anthropic.NewClient(opts...),
		model:  strings.TrimSpace(o.Model),
		log:    o.Log,
	}, nil
}

// Name возвращает имя реализации. Модель названа поимённо: читатель среза
// должен видеть не «модель», а какую именно — от версии зависят и качество
// разбора, и цена.
func (a *Analyst) Name() string { return "модель " + a.model }

// Extract разбирает источники задачи.
func (a *Analyst) Extract(ctx context.Context, in analyst.Input) (analyst.Output, error) {
	// Пустой вход до модели не доходит. Платить за вызов, в котором нечего
	// разбирать, незачем, а ответ на него был бы выдумкой от первого до
	// последнего поля.
	if len(in.Sources) == 0 {
		return analyst.Output{}, fmt.Errorf("нет источников для разбора")
	}

	tool := anthropic.ToolParam{
		Name:        schema.ToolName,
		Description: anthropic.String(schema.ToolDescription),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: schema.Properties(),
			// Строгий режим требует и запрета лишних полей, и списка
			// обязательных.
			ExtraFields: map[string]any{
				"additionalProperties": false,
				"required":             schema.Required(),
			},
		},
		Strict: anthropic.Bool(true),
	}

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: schema.SystemPrompt}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(schema.UserPrompt(in))),
		},
		Tools: []anthropic.ToolUnionParam{{OfTool: &tool}},
		// Выбор инструмента задан жёстко: разбор нужен схемой, а не рассказом о
		// задаче. Без этого модель время от времени отвечает текстом, и разбор
		// падает на пустом месте.
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: schema.ToolName},
		},
	})
	if err != nil {
		return analyst.Output{}, fmt.Errorf("вызов модели: %w", err)
	}

	raw, err := answerJSON(resp)
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

// answerJSON достаёт из ответа аргументы вызова инструмента.
func answerJSON(resp *anthropic.Message) (json.RawMessage, error) {
	for _, block := range resp.Content {
		use, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || use.Name != schema.ToolName {
			continue
		}
		return json.RawMessage(use.JSON.Input.Raw()), nil
	}
	// Модель ответила не тем, чем просили. Отдельная ошибка, а не «пустой
	// разбор»: пустой разбор читается как «в источниках ничего нет», и разница
	// между «нечего сказать» и «не сработало» потерялась бы.
	return nil, fmt.Errorf("модель не вызвала инструмент %s (причина остановки %q)",
		schema.ToolName, resp.StopReason)
}
