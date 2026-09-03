// Package bitrix — клиент к REST API Bitrix24 через входящий вебхук.
//
// Пакет знает только про Bitrix: он отдаёт чаты, сообщения и имена сотрудников
// в своих собственных типах и ничего не знает ни про задачи реестра, ни про
// источники. Превращение сообщения в источник — работа сервиса, не клиента.
//
// Форма ответов не угадана, а снята с живого портала. Это оказалось важно:
// три вещи в этом API устроены не так, как выглядят по названиям, и каждая
// сломала бы синхронизацию молча. Они отмечены в комментариях там, где важны.
package bitrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotConfigured — вебхук не задан. Это не поломка: сервис обязан работать без
// Bitrix. Список чатов тогда пуст, задача всё равно создаётся, а интерфейс
// объясняет, что интеграция не настроена, вместо того чтобы показывать ошибку.
var ErrNotConfigured = errors.New("bitrix: вебхук не задан")

const (
	// timeout на один вызов. Портал отвечает за секунду-полторы, но на выборке
	// сообщений бывает медленнее, и обрывать его на пятой секунде — значит
	// получать пустой чат там, где данные есть.
	timeout = 30 * time.Second

	// attempts — сколько раз повторять то, что имеет смысл повторять.
	attempts = 3
)

// retryDelay — шаг паузы между повторами. Переменная, а не константа, только для
// тестов: проверять логику повторов, отсиживая настоящие паузы, незачем.
var retryDelay = 2 * time.Second

// Client — вебхук и HTTP-клиент к нему.
//
// Адрес вебхука содержит токен, равносильный паролю, поэтому он не попадает ни
// в логи, ни в текст ошибок: логируется имя метода, а не URL. Единственное
// место, где адрес виден, — сам HTTP-запрос.
type Client struct {
	base string
	http *http.Client
	log  *slog.Logger
}

// New собирает клиент из адреса вебхука. Пустой адрес — допустимое состояние:
// клиент создаётся, но каждый вызов отвечает ErrNotConfigured.
func New(webhook string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		base: strings.TrimRight(strings.TrimSpace(webhook), "/"),
		http: &http.Client{Timeout: timeout},
		log:  log,
	}
}

// Configured говорит, есть ли с чем работать. Проверять это должен вызывающий,
// до вызова: «интеграция не настроена» — это состояние интерфейса, а не ошибка
// выполнения.
func (c *Client) Configured() bool { return c.base != "" }

// Origin — адрес портала без пути: «https://портал.bitrix24.ru».
//
// Нужен, чтобы из среза можно было вернуться к первоисточнику: ссылка на
// сообщение в Bitrix убеждает заказчика лучше любой цитаты. Отдаётся именно
// origin, а не base: в base лежит путь вида /rest/1/ТОКЕН/, и он равносилен
// паролю к порталу. Схема и хост — не секрет, а вот всё после них секрет, и
// отрезаются они здесь, в одном месте, а не у каждого, кому понадобилась
// ссылка.
//
// Пустая строка означает, что адрес разобрать не удалось или вебхук не задан.
// Ссылки на оригинал тогда просто не будет — это лучше, чем ссылка, собранная
// из огрызка адреса.
func (c *Client) Origin() string {
	if c.base == "" {
		return ""
	}
	u, err := url.Parse(c.base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// MessageURL — ссылка на сообщение в веб-интерфейсе портала.
//
// Пустая строка, если портал неизвестен или чат с сообщением не названы: адрес
// «никуда» в срезе хуже отсутствия адреса — по нему сходят и вернутся ни с чем.
func (c *Client) MessageURL(dialogID, messageID string) string {
	origin := c.Origin()
	if origin == "" || dialogID == "" || messageID == "" {
		return ""
	}
	q := url.Values{"IM_DIALOG": {dialogID}, "IM_MESSAGE": {messageID}}
	return origin + "/online/?" + q.Encode()
}

// Error — ошибка, которую вернул сам Bitrix. Портал отвечает конвертом
// {"error":…,"error_description":…} и обычно ставит подходящий код HTTP (403 на
// закрытый диалог, 404 на неизвестный метод), но конверт информативнее кода,
// поэтому разбирается он, а код только сохраняется.
type Error struct {
	Code        string
	Description string
	Status      int
}

func (e *Error) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("bitrix: %s (HTTP %d)", e.Code, e.Status)
	}
	return fmt.Sprintf("bitrix: %s — %s (HTTP %d)", e.Code, e.Description, e.Status)
}

// Retryable отделяет «попробуй позже» от «так не будет работать никогда».
// Повторять запрос при ACCESS_ERROR бессмысленно: прав от этого не появится.
func (e *Error) Retryable() bool {
	switch strings.ToUpper(e.Code) {
	case "QUERY_LIMIT_EXCEEDED", "OPERATION_TIME_LIMIT_SUSPEND", "INTERNAL_SERVER_ERROR":
		return true
	}
	return e.Status >= 500
}

// envelope — общая обёртка любого ответа REST. Result разбирается вторым шагом,
// потому что его форма у каждого метода своя.
type envelope struct {
	Result json.RawMessage `json:"result"`
	Next   *int            `json:"next"`
	Total  *int            `json:"total"`
	Error  string          `json:"error"`
	Desc   string          `json:"error_description"`
}

// Call вызывает метод REST и раскладывает result в out. Пустой out допустим:
// иногда нужен только факт успеха.
func (c *Client) Call(ctx context.Context, method string, params url.Values, out any) error {
	_, err := c.call(ctx, method, params, out)
	return err
}

// call — тот же вызов, но отдаёт конверт целиком: у части методов в нём лежит
// смещение следующей страницы.
func (c *Client) call(ctx context.Context, method string, params url.Values, out any) (envelope, error) {
	if !c.Configured() {
		return envelope{}, ErrNotConfigured
	}

	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			// Пауза растёт, но остаётся отменяемой: ночная пересборка должна
			// прерываться по сигналу, а не досиживать паузу до конца.
			delay := time.Duration(attempt-1) * retryDelay
			select {
			case <-ctx.Done():
				return envelope{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		env, err := c.once(ctx, method, params, out)
		if err == nil {
			return env, nil
		}
		last = err

		if !retryable(err) || ctx.Err() != nil {
			return envelope{}, err
		}
		c.log.Warn("повтор вызова Bitrix", "метод", method, "попытка", attempt, "ошибка", err)
	}
	return envelope{}, fmt.Errorf("bitrix: %s не ответил за %d попытки: %w", method, attempts, last)
}

// once — одна попытка: запрос, разбор конверта, разбор result.
func (c *Client) once(ctx context.Context, method string, params url.Values, out any) (envelope, error) {
	if params == nil {
		params = url.Values{}
	}
	body := strings.NewReader(params.Encode())

	// POST, а не GET: параметры бывают длинными (списки идентификаторов), и
	// упираться в ограничение на длину URL незачем. Токен от этого не
	// скрывается — он в пути вебхука, — поэтому URL не логируется вообще.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method+".json", body)
	if err != nil {
		return envelope{}, fmt.Errorf("bitrix: запрос %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		// Ошибку транспорта отдаём без URL: в тексте ошибки от http.Client
		// адрес есть, а в нём токен.
		return envelope{}, fmt.Errorf("bitrix: %s недоступен: %w", method, scrub(err))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return envelope{}, fmt.Errorf("bitrix: чтение ответа %s: %w", method, err)
	}
	c.log.Debug("вызов Bitrix", "метод", method, "код", resp.StatusCode,
		"байт", len(raw), "заняло", time.Since(started).Round(time.Millisecond))

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// Портал умеет отвечать HTML — например, страницей «портал
		// заблокирован». Сообщение об ошибке должно это показывать.
		return envelope{}, fmt.Errorf("bitrix: %s ответил не JSON (HTTP %d): %s",
			method, resp.StatusCode, excerpt(raw))
	}
	if env.Error != "" {
		return envelope{}, &Error{Code: env.Error, Description: env.Desc, Status: resp.StatusCode}
	}
	if resp.StatusCode/100 != 2 {
		return envelope{}, &Error{Code: "HTTP", Description: excerpt(raw), Status: resp.StatusCode}
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return envelope{}, fmt.Errorf("bitrix: разбор ответа %s: %w", method, err)
		}
	}
	return env, nil
}

// retryable решает, имеет ли смысл повторять. Сетевые обрывы и таймауты — да;
// отказ в правах — нет.
func retryable(err error) bool {
	var be *Error
	if errors.As(err, &be) {
		return be.Retryable()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Всё, что осталось, — транспорт: обрыв, отказ соединения, таймаут клиента.
	return !errors.Is(err, ErrNotConfigured)
}

// scrub убирает адрес вебхука из текста ошибки транспорта. Токен не должен
// оказаться ни в логе, ни в ответе API, ни на экране.
func scrub(err error) error {
	if err == nil {
		return nil
	}
	if clean, hidden := Redact(err.Error()); hidden {
		return errors.New(clean)
	}
	return err
}

// excerpt отдаёт начало ответа для сообщения об ошибке: целиком чужой HTML в
// логе не нужен.
func excerpt(raw []byte) string {
	const max = 200

	s := strings.TrimSpace(string(raw))
	if len(s) > max {
		s = s[:max] + "…"
	}
	clean, _ := Redact(s)
	return clean
}

// parseTime разбирает дату Bitrix. Портал отдаёт ISO с поясом («+03:00»), но
// встречаются и пустая строка, и объект вместо даты.
//
// На неразобранном значении возвращается нулевое время, а не ошибка, и это
// осознанное решение: по правилу домена нулевая дата означает «дату назвать
// нечем». Сообщение без даты — это сообщение, у которого нет даты; терять из-за
// этого весь чат нельзя.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
