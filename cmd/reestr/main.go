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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst/manual"
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

	svc := service.New(st, manual.New(), log)

	empty, err := st.Empty(ctx)
	if err != nil {
		return err
	}
	if *seed && empty {
		if err := fill(ctx, st, svc, log); err != nil {
			return fmt.Errorf("заполнение хранилища: %w", err)
		}
	}

	assets, err := ui(*dir)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: httpapi.New(svc, assets, log),
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
