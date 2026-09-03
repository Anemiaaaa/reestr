package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/Anemiaaaa/reestr/internal/analyst"
)

// toolName — имя инструмента, которым модель обязана ответить.
//
// Ответ идёт инструментом, а не текстом, и выбор инструмента задан жёстко.
// Причина простая: текстовый ответ пришлось бы вырезать из markdown-обёртки и
// разбирать на удачу, а разбор на удачу ошибается ровно тогда, когда модель
// написала что-то непривычное, — то есть в самом интересном случае.
const toolName = "srez"

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

	// Model — идентификатор модели, например claude-opus-4-8. Пустая строка —
	// ошибка, а не повод подставить какую-нибудь: за подстановку платит
	// заказчик, и молча выбирать модель за него нельзя.
	Model string

	Log *slog.Logger
}

// Analyst — разбор источников моделью.
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
// должен видеть не «модель», а какую именно — от версии зависит и качество
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

	props := schemaProperties()
	tool := anthropic.ToolParam{
		Name:        toolName,
		Description: anthropic.String("Вернуть разбор источников задачи."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: props,
			// Строгий режим требует и запрета лишних полей, и списка
			// обязательных. Обязательны все разделы: пустой список — это
			// утверждение «ничего не нашлось», а пропущенный раздел — молчание,
			// и различать их важнее, чем экономить на длине ответа.
			ExtraFields: map[string]any{
				"additionalProperties": false,
				"required":             sortedKeys(props),
			},
		},
		Strict: anthropic.Bool(true),
	}

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{{
			Text: systemPrompt,
		}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt(in))),
		},
		Tools: []anthropic.ToolUnionParam{{OfTool: &tool}},
		// Выбор инструмента задан жёстко: разбор нужен схемой, а не рассказом о
		// задаче. Без этого модель время от времени отвечает текстом, и разбор
		// падает на пустом месте.
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: toolName},
		},
	})
	if err != nil {
		return analyst.Output{}, fmt.Errorf("вызов модели: %w", err)
	}

	raw, err := answerJSON(resp)
	if err != nil {
		return analyst.Output{}, err
	}

	var got answer
	if err := json.Unmarshal(raw, &got); err != nil {
		return analyst.Output{}, fmt.Errorf("разбор ответа модели: %w", err)
	}

	out, rejected := convert(got, in)
	a.report(in.Task.ID, rejected)
	return out, nil
}

// answerJSON достаёт из ответа аргументы вызова инструмента.
func answerJSON(resp *anthropic.Message) (json.RawMessage, error) {
	for _, block := range resp.Content {
		use, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || use.Name != toolName {
			continue
		}
		return json.RawMessage(use.JSON.Input.Raw()), nil
	}
	// Модель ответила не тем, чем просили. Отдельная ошибка, а не «пустой
	// разбор»: пустой разбор читается как «в источниках ничего нет», и разница
	// между «нечего сказать» и «не сработало» потерялась бы.
	return nil, fmt.Errorf("модель не вызвала инструмент %s (причина остановки %q)",
		toolName, resp.StopReason)
}

// report пишет в лог, что из ответа не приняли.
//
// Отклонения не молчат по той же причине, по которой они вообще собираются: по
// ним видно, врёт модель редко или постоянно и на каких полях. Молчаливая
// фильтрация выглядела бы как безупречный разбор, который просто мало что нашёл.
func (a *Analyst) report(taskID string, rejected []Rejection) {
	if len(rejected) == 0 {
		return
	}
	reasons := make([]string, 0, len(rejected))
	for _, r := range rejected {
		reasons = append(reasons, r.String())
	}
	a.log.Warn("часть ответа модели не принята",
		"задача", taskID, "отклонено", len(rejected),
		"причины", strings.Join(reasons, "; "))
}

// userPrompt собирает задание: карточку задачи и источники с их номерами.
//
// Номера источников здесь и есть то, чем модель будет ссылаться. Поэтому они
// выводятся заметно и рядом с текстом: ссылка на источник — единственное
// основание доверять срезу, и промахнуться в ней нельзя.
func userPrompt(in analyst.Input) string {
	var b strings.Builder

	b.WriteString("# Задача\n\n")
	fmt.Fprintf(&b, "Проект: %s\n", or(in.Task.Project, "не назван"))
	fmt.Fprintf(&b, "Название: %s\n", or(in.Task.Title, "не названо"))
	fmt.Fprintf(&b, "Постановщик: %s\n", or(in.Task.Author, "не назван"))
	fmt.Fprintf(&b, "Исполнитель: %s\n", or(in.Task.Assignee, "не назван"))
	fmt.Fprintf(&b, "Поставлена: %s\n", orDate(in.Task.OpenedAt))
	fmt.Fprintf(&b, "Срок по карточке: %s\n", orDate(in.Task.Deadline))
	fmt.Fprintf(&b, "\nСегодня: %s\n", in.Now.Format(dateLayout))

	b.WriteString("\n# Источники\n")
	for _, s := range in.Sources {
		fmt.Fprintf(&b, "\n## Источник %s\n", s.ID)
		fmt.Fprintf(&b, "Вид: %s\n", s.Kind.Label())
		if s.Author != "" {
			fmt.Fprintf(&b, "Автор: %s\n", s.Author)
		}
		if !s.OccurredAt.IsZero() {
			fmt.Fprintf(&b, "Дата: %s\n", s.OccurredAt.Format(dateLayout))
		}
		fmt.Fprintf(&b, "\n%s\n", s.Body)
	}

	b.WriteString("\n# Что сделать\n\nРазбери источники и верни результат вызовом инструмента " +
		toolName + ". Ссылайся только на номера источников из списка выше.\n")
	return b.String()
}

func or(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// orDate печатает дату в том же виде, в каком модель обязана их возвращать.
// Незаполненная дата называется прямо: «не заполнено» и выдуманное число —
// разные утверждения, и подставлять второе вместо первого нельзя.
func orDate(t time.Time) string {
	if t.IsZero() {
		return "не заполнено"
	}
	return t.Format(dateLayout)
}
