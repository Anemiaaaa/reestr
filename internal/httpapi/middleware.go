package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// counter запоминает код и размер ответа, чтобы их можно было записать в лог.
// http.ResponseWriter сам об отданном ответе ничего не сообщает.
type counter struct {
	http.ResponseWriter
	code  int
	bytes int
}

func (c *counter) WriteHeader(code int) {
	c.code = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *counter) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK // обработчик писал в тело, не вызвав WriteHeader
	}
	n, err := c.ResponseWriter.Write(b)
	c.bytes += n
	return n, err
}

// withLogging пишет строку на каждый запрос к API.
//
// Файлы интерфейса из лога исключены: страница тянет за собой css и js, и три
// строки на каждое обновление вкладки только мешают читать остальное.
func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		c := &counter{ResponseWriter: w}
		next.ServeHTTP(c, r)

		level := slog.LevelInfo
		if c.code >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		log.Log(r.Context(), level, "запрос",
			"метод", r.Method,
			"путь", r.URL.Path,
			"код", c.code,
			"байт", c.bytes,
			"мс", time.Since(start).Milliseconds())
	})
}

// withRecover не даёт панике в обработчике уронить сервер.
//
// Стек уходит в лог целиком, клиенту — только код 500: паника означает ошибку в
// коде, и разбирать её надо по стеку, а не по тексту в браузере.
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			if p == http.ErrAbortHandler {
				panic(p) // условный знак «клиент ушёл», обрабатывается выше
			}
			log.Error("паника в обработчике",
				"путь", r.URL.Path,
				"причина", p,
				"стек", string(debug.Stack()))
			writeJSON(w, http.StatusInternalServerError,
				map[string]string{"error": "внутренняя ошибка сервера"})
		}()
		next.ServeHTTP(w, r)
	})
}
