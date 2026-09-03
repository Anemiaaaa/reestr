package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/Anemiaaaa/reestr/internal/auth"
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

// cookieName — имя куки с пропуском.
const cookieName = "reestr_session"

// openPaths — то, что доступно без входа.
//
// Список короткий и закрытый намеренно. Всё, чего в нём нет, требует пропуска:
// новая ручка попадает под защиту сама, а не после того, как о ней вспомнят.
//
// Оформление страницы входа тут потому, что без него страница входа выглядит
// сломанной. Данных в css нет.
var openPaths = map[string]bool{
	"/login":       true,
	"/api/login":   true,
	"/api/logout":  true,
	"/api/health":  true,
	"/app.css":     true,
	"/favicon.ico": true,
}

// withAuth пускает дальше только тех, кто вошёл.
//
// Отказ выглядит по-разному для страницы и для API. Человеку, открывшему адрес
// в браузере, нужна страница входа, а не голое «401»; браузеру, спросившему
// данные из уже открытой страницы, нужен код, по которому он поймёт, что
// сессия кончилась, и покажет вход сам. Ответить одинаково значит показать
// одному из двух бесполезное.
func withAuth(a *auth.Auth, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if openPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		c, err := r.Cookie(cookieName)
		if err == nil {
			if _, ok := a.Verify(c.Value); ok {
				next.ServeHTTP(w, r)
				return
			}
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "нужно войти",
			})
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// setSession кладёт пропуск в куку.
//
// HttpOnly — чтобы пропуск нельзя было прочитать из скрипта: любая чужая
// строка, попавшая на страницу, иначе уносит сессию. SameSite=Lax — чтобы
// запрос со стороннего сайта не выполнялся от имени вошедшего.
//
// Secure ставится только на HTTPS. На http://127.0.0.1 такая кука браузером
// отбрасывается, и локальный запуск перестал бы пускать внутрь; за адресом
// смотрим по заголовку прокси, потому что до сервера доходит уже расшифрованный
// запрос.
func setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((24 * time.Hour).Seconds()),
	})
}

// clearSession стирает куку.
func clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func secure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
