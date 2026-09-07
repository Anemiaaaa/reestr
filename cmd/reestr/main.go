// Команда reestr поднимает локальный сервер реестра задач.
//
// Хранилище выбирается настройкой DATABASE_URL: с ней — PostgreSQL, без неё —
// один JSON-файл. Второй путь оставлен намеренно: инструмент должен запускаться
// одной командой на чужой машине, где базы нет, — иначе показать его нельзя.
// Интерфейс вложен в бинарник, поэтому сборки фронтенда тоже не требуется.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/analyst/gateway"
	"github.com/Anemiaaaa/reestr/internal/analyst/manual"
	"github.com/Anemiaaaa/reestr/internal/auth"
	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/config"
	"github.com/Anemiaaaa/reestr/internal/httpapi"
	"github.com/Anemiaaaa/reestr/internal/service"
	"github.com/Anemiaaaa/reestr/internal/store"
	"github.com/Anemiaaaa/reestr/internal/store/jsonstore"
	"github.com/Anemiaaaa/reestr/internal/store/pgstore"
	"github.com/Anemiaaaa/reestr/web"
)

func main() {
	if err := run(); err != nil {
		slog.Default().Error("сервер остановлен с ошибкой", "ошибка", err)
		os.Exit(1)
	}
}

func run() error {
	// Настройки читаются до разбора флагов: значения из .env становятся
	// значениями по умолчанию, а флаг остаётся способом перебить их на один
	// запуск.
	if err := config.Load(".env"); err != nil {
		return err
	}

	var (
		// Адрес по умолчанию — только петля, а не все интерфейсы. В реестре лежит
		// аудит реального заказчика с оборотами и сметой; такое не выставляют в
		// сеть по невнимательности.
		addr = flag.String("addr", config.Env("REESTR_ADDR", "127.0.0.1:8080"),
			"адрес, на котором слушать")
		dsn = flag.String("db", config.Env("DATABASE_URL", ""),
			"строка подключения к PostgreSQL; пусто — работать на файле")
		data = flag.String("data", filepath.Join("data", "reestr.json"),
			"файл с данными, если база не задана")
		// Каталог с интерфейсом вместо вложенного в бинарник: правку в css видно
		// после обновления страницы, без пересборки.
		dir     = flag.String("web", "", "каталог с интерфейсом вместо вложенного")
		seed    = flag.Bool("seed", true, "заполнить пустое хранилище разбором АУРА")
		verbose = flag.Bool("v", false, "подробный лог")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, where, closeStore, err := openStore(ctx, *dsn, *data)
	if err != nil {
		return err
	}
	defer closeStore()

	svc := service.New(st, chooseAnalyst(log), log)
	// Вебхук не обязателен: без него реестр работает целиком, теряя только
	// список чатов при создании задачи. Адрес в лог не пишется — в нём токен.
	if hook := config.Env("BITRIX_WEBHOOK", ""); hook != "" {
		svc.Portal(bitrix.New(hook, log))
		log.Info("портал Bitrix24 подключён")
	}

	empty, err := st.Empty(ctx)
	if err != nil {
		return err
	}
	if *seed && empty {
		if err := fill(ctx, st, svc, log); err != nil {
			return fmt.Errorf("заполнение хранилища: %w", err)
		}
	}

	// Ночная пересборка живёт столько же, сколько сервер: контекст запуска
	// отменяется сигналом, и незавершённый проход обрывается вместе с ним.
	if r, err := rebuilder(svc, log); err != nil {
		// Опечатка в расписании — ошибка запуска, а не повод молча работать без
		// него. Реестр, который «почему-то не обновляется по ночам», разбирают
		// неделю.
		return err
	} else if r != nil {
		go r.Run(ctx)
	}

	users, err := logins(log, *addr)
	if err != nil {
		return err
	}

	assets, err := ui(*dir)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: httpapi.New(svc, assets, log, users),
		// Единственный таймаут, который здесь уместен: чтение запроса ограничено,
		// а на сам обмен лимита нет — источник в сотню килобайт грузится столько,
		// сколько грузится.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("сервер запущен", "адрес", "http://"+*addr, "хранилище", where)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("остановка")
	}

	// Контекст запуска уже отменён сигналом, поэтому у выключения свой, со сроком.
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return fmt.Errorf("остановка сервера: %w", err)
	}
	return <-errc
}

// chooseAnalyst выбирает, кто разбирает источники: модель, если настроена,
// иначе ручной разбор.
//
// Отказ от модели здесь не ошибка запуска. Реестр без модели работает целиком —
// он просто показывает разбор, собранный руками, и говорит об этом в срезе
// именем аналитика. Падать из-за ненастроенной необязательной интеграции значит
// требовать ключ от того, кто пришёл посмотреть список задач.
//
// Ошибку настройки при этом видно: она пишется в лог предупреждением. Молча
// откатиться на ручной разбор было бы хуже отказа — человек ждал бы от среза
// свежего разбора и не понял, почему видит старый.
func chooseAnalyst(log *slog.Logger) analyst.Analyst {
	key := config.Env("ANTHROPIC_API_KEY", "")
	model := config.Env("ANTHROPIC_MODEL", "")
	if key == "" || model == "" {
		log.Info("модель не настроена, разбор ручной")
		return manual.New()
	}

	a, err := gateway.New(gateway.Options{
		BaseURL: config.Env("ANTHROPIC_BASE_URL", ""),
		APIKey:  key,
		Model:   model,
		Log:     log,
	})
	if err != nil {
		// Адрес шлюза и модель в лог попадают, ключ — нет.
		log.Warn("модель настроена неверно, разбор ручной", "ошибка", err)
		return manual.New()
	}
	log.Info("разбор моделью", "модель", model)
	return a
}

// openStore открывает хранилище: базу, если задана строка подключения, иначе
// файл. Возвращает ещё и описание для лога — по нему при разборе жалобы сразу
// видно, где лежали данные.
func openStore(ctx context.Context, dsn, data string) (store.Store, string, func(), error) {
	if dsn != "" {
		st, err := pgstore.Open(ctx, dsn)
		if err != nil {
			return nil, "", nil, fmt.Errorf("хранилище в базе: %w", err)
		}
		return st, "postgresql", st.Close, nil
	}

	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		return nil, "", nil, fmt.Errorf("каталог для данных: %w", err)
	}
	st, err := jsonstore.Open(data)
	if err != nil {
		return nil, "", nil, fmt.Errorf("хранилище %s: %w", data, err)
	}
	return st, data, func() {}, nil
}

// ui выбирает, откуда брать файлы интерфейса.
func ui(dir string) (fs.FS, error) {
	if dir == "" {
		return web.Files(), nil
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return nil, fmt.Errorf("каталог интерфейса %s: %w", dir, err)
	}
	return os.DirFS(dir), nil
}

// fill кладёт в пустое хранилище разбор АУРА, чтобы сервер сразу было что
// открыть.
//
// Задача и источники пишутся в хранилище напрямую, минуя сервис. Сервис
// подставляет значения по умолчанию — в том числе дату постановки, если её нет,
// — а в этой задаче её и нет: в переписке она не названа, и это один из пробелов
// среза. Пусть остаётся пробелом, а не превращается в сегодняшнее число.
func fill(ctx context.Context, st store.Store, svc *service.Service, log *slog.Logger) error {
	task, sources := manual.Seed()

	if err := st.CreateTask(ctx, task); err != nil {
		return err
	}
	for _, src := range sources {
		if err := st.AddSource(ctx, src); err != nil {
			return err
		}
	}
	if _, err := svc.Rebuild(ctx, task.ID); err != nil {
		return err
	}
	log.Info("хранилище заполнено", "задача", task.ID, "источников", len(sources))
	return nil
}

// rebuilder собирает ночную пересборку из настроек.
//
// Пустое REESTR_REBUILD_AT означает «не пересобирать по ночам», и это законный
// режим: на чужой машине, куда реестр принесли показать, ночной проход не
// нужен, а с платной моделью он ещё и тратил бы деньги.
//
// Всё остальное — ошибка запуска. Расписание либо задано верно, либо его нет:
// реестр, который «почему-то не обновляется по ночам» из-за опечатки в поясе,
// разбирают неделю.
func rebuilder(svc *service.Service, log *slog.Logger) (*service.Rebuilder, error) {
	at := strings.TrimSpace(config.Env("REESTR_REBUILD_AT", ""))
	if at == "" {
		log.Info("ночная пересборка выключена")
		return nil, nil
	}

	t, err := time.Parse("15:04", at)
	if err != nil {
		return nil, fmt.Errorf("REESTR_REBUILD_AT=%q: нужно ЧЧ:ММ", at)
	}

	// Пояс берётся из настроек, а не из часов сервера. «20:00 по Москве» обязано
	// остаться восемью вечера и на сервере, живущем по Гринвичу, — а сервер
	// заказчика именно такой.
	name := config.Env("REESTR_TZ", "Europe/Moscow")
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("REESTR_TZ=%q: %w", name, err)
	}

	// Предел разборов за ночь. Десять по умолчанию, а не «сколько получится»:
	// разбор платный, и ночь, когда материал появился разом у всех задач,
	// списала бы весь остаток. Ноль означает «без предела» — осознанный выбор
	// того, кто настраивает, а не молчаливое умолчание.
	limit := 10
	if raw := strings.TrimSpace(config.Env("REESTR_NIGHTLY_LIMIT", "")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("REESTR_NIGHTLY_LIMIT=%q: нужно число", raw)
		}
		limit = n
	}

	log.Info("ночная пересборка включена",
		"время", at, "пояс", name, "предел разборов за ночь", limit)
	return service.NewRebuilder(svc, t.Hour(), t.Minute(), loc, limit), nil
}

// logins собирает вход из настройки REESTR_USERS вида «логин:пароль,логин:пароль».
//
// Пустая настройка выключает вход, и это законно ровно в одном случае: сервер
// слушает петлю. Тогда до него не дотянуться ни из сети, ни с соседней машины,
// и пароль защищал бы от самого хозяина ноутбука.
//
// Как только адрес не петлевой, отсутствие входа — ошибка запуска. В реестре
// лежат аудиты заказчиков с оборотами и сметами, а теперь ещё и журнал
// инцидентов по сотрудникам; выставить это в сеть по невнимательности нельзя, и
// «забыл настроить» не должно выглядеть как рабочий запуск.
func logins(log *slog.Logger, addr string) (*auth.Auth, error) {
	users := strings.TrimSpace(config.Env("REESTR_USERS", ""))
	if users == "" {
		if !loopback(addr) {
			return nil, fmt.Errorf(
				"адрес %s доступен снаружи, а REESTR_USERS не задан: вход обязателен", addr)
		}
		log.Warn("вход выключен: REESTR_USERS не задан", "адрес", addr)
		return nil, nil
	}

	a, err := auth.New(users, config.Env("REESTR_SECRET", ""))
	if err != nil {
		return nil, fmt.Errorf("REESTR_USERS: %w", err)
	}
	if err := a.Managers(config.Env("REESTR_MANAGERS", "")); err != nil {
		return nil, fmt.Errorf("REESTR_MANAGERS: %w", err)
	}
	log.Info("вход включён", "пользователи", strings.Join(a.Logins(), ", "))

	// О неразделённых ролях говорим вслух: в журнале инцидентов лежат записи о
	// работе конкретных людей, и «его видят все» человек должен узнать от
	// сервера, а не обнаружить, когда сотрудник прочитает про себя.
	if !a.ManagersSet() {
		log.Warn("REESTR_MANAGERS не задан: журнал инцидентов видят все, кто вошёл",
			"подсказка", "REESTR_MANAGERS=логин — оставить журнал одному руководителю")
	}

	// Пароль, совпадающий с логином, подбирается первой же попыткой. Об этом
	// говорим при каждом запуске, а не один раз в документации: настройка, о
	// которой напоминают, меняется, а та, о которой написали в README, живёт
	// годами.
	if weak := a.Weak(); len(weak) != 0 {
		log.Warn("пароль совпадает с логином — смените до публикации в сеть",
			"пользователи", strings.Join(weak, ", "))
	}
	// Без REESTR_SECRET подпись случайная, и перезапуск разлогинивает всех.
	if config.Env("REESTR_SECRET", "") == "" {
		log.Info("REESTR_SECRET не задан: после перезапуска придётся войти заново")
	}
	return a, nil
}

// loopback отвечает, слушает ли сервер только петлю.
//
// Пустой хост в адресе — это все интерфейсы, а не петля: «:8080» доступен
// соседям по сети. Ошибка разбора трактуется как «не петля»: сомнение здесь
// должно склонять к осторожности, а не к удобству.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
