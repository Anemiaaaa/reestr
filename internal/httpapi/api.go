package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Anemiaaaa/reestr/internal/auth"
	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/report"
	"github.com/Anemiaaaa/reestr/internal/ru"
	"github.com/Anemiaaaa/reestr/internal/service"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// Server — HTTP-обвязка вокруг сервиса.
type Server struct {
	svc     *service.Service
	log     *slog.Logger
	handler http.Handler

	// auth и web нужны странице входа: она отдаётся из тех же файлов интерфейса
	// и решает, пускать ли дальше. Пустой auth означает «вход выключен» —
	// законный режим для локального запуска на петле.
	auth *auth.Auth
	web  fs.FS
}

// New собирает сервер. web — файлы интерфейса; откуда они взялись, из embed или
// с диска, транспорту знать не нужно.
func New(svc *service.Service, web fs.FS, log *slog.Logger, a *auth.Auth) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{svc: svc, log: log, auth: a, web: web}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/me", s.who)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /api/bitrix/tasks", s.portalTasks)
	mux.HandleFunc("GET /api/bitrix/chats", s.chats)
	mux.HandleFunc("GET /api/tasks", s.tasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.task)
	mux.HandleFunc("GET /api/tasks/{id}/board", s.board)
	mux.HandleFunc("GET /api/tasks/{id}/slice", s.slice)
	mux.HandleFunc("GET /api/tasks/{id}/slice.md", s.sliceText)
	mux.HandleFunc("POST /api/tasks/{id}/slice/rebuild", s.rebuild)
	mux.HandleFunc("POST /api/tasks/{id}/slice/correct", s.correct)
	mux.HandleFunc("DELETE /api/tasks/{id}/slice/corrections", s.dropCorrections)
	mux.HandleFunc("DELETE /api/tasks/{id}/slices/{version}", s.deleteVersion)
	mux.HandleFunc("POST /api/tasks/{id}/pull", s.pull)
	mux.HandleFunc("GET /api/tasks/{id}/slices", s.versions)
	mux.HandleFunc("GET /api/tasks/{id}/slices/compare", s.compare)
	mux.HandleFunc("GET /api/tasks/{id}/slices/{version}", s.version)
	mux.HandleFunc("GET /api/tasks/{id}/sources", s.taskSources)
	mux.HandleFunc("POST /api/tasks/{id}/sources", s.addSource)
	mux.HandleFunc("GET /api/tasks/{id}/processes", s.processes)
	mux.HandleFunc("GET /api/tasks/{id}/facts", s.facts)
	mux.HandleFunc("GET /api/incidents", s.incidents)
	mux.HandleFunc("POST /api/incidents", s.addIncident)
	mux.HandleFunc("GET /api/incidents.md", s.journalText)
	mux.HandleFunc("PUT /api/incidents/{id}", s.updateIncident)
	mux.HandleFunc("DELETE /api/incidents/{id}", s.deleteIncident)
	mux.HandleFunc("GET /api/sources/{id}", s.source)
	if web != nil {
		mux.Handle("GET /", http.FileServerFS(web))
	}

	handler := http.Handler(mux)
	// Вход оборачивает всё разом: новая ручка попадает под защиту сама, а не
	// после того, как о ней вспомнят.
	if a != nil {
		handler = withAuth(a, handler)
	}
	s.handler = withRecover(log, withLogging(log, handler))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// --- обработчики ---

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"analyst": s.svc.Analyst(),
		"bitrix":  s.svc.PortalConfigured(),
		"now":     s.svc.Now().Format(time.RFC3339),
	})
}

func (s *Server) chats(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.PortalChats(r.Context(), 50)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newChatOptions(s.svc.PortalConfigured(), list))
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Tasks(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.svc.Now()
	out := make([]task, 0, len(list))
	for _, t := range list {
		out = append(out, newTask(t, now))
	}
	writeJSON(w, http.StatusOK, out)
}

// taskRequest — задача, как её присылает форма.
type taskRequest struct {
	Project  string `json:"project"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Assignee string `json:"assignee"`
	OpenedAt date   `json:"openedAt"`
	Deadline date   `json:"deadline"`
	Budget   int    `json:"budget"`

	// BitrixTaskID — номер выбранной задачи портала. Пусто: задача реестра без
	// закрепления, это нормальный путь и с порталом, и без него.
	//
	// Номер задачи, а не чата: чат у задачи портала ровно один, и спрашивать про
	// него отдельно значило бы спрашивать про то, у чего нет выбора. Какой это
	// чат, у портала спросит сервис.
	BitrixTaskID string `json:"bitrixTaskId"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req taskRequest
	if err := readJSON(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()

	// У портала спрашиваем до создания, а не после. Отказ после CreateTask
	// оставил бы созданную задачу и ошибку в ответе разом, и повторная отправка
	// формы завела бы вторую такую же.
	var portal bitrix.Task
	if id := strings.TrimSpace(req.BitrixTaskID); id != "" {
		var err error
		if portal, err = s.svc.PortalTask(ctx, id); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	t, err := s.svc.CreateTask(ctx, domain.Task{
		Project:  req.Project,
		Title:    req.Title,
		Author:   req.Author,
		Assignee: req.Assignee,
		OpenedAt: req.OpenedAt.Time,
		Deadline: req.Deadline.Time,
		Budget:   req.Budget,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if portal.ID != "" {
		_, err := s.svc.PinChat(ctx, domain.ChatLink{
			TaskID:         t.ID,
			System:         domain.SystemBitrix,
			DialogID:       portal.DialogID(),
			Title:          portal.Title,
			ExternalTaskID: portal.ID,
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}

		// Постановка задачи заводится источником сразу: это материал, который
		// есть уже сейчас, и ждать от него отдельного нажатия незачем. Отказ
		// здесь задачу не отменяет — она создана и закреплена, а материал
		// можно добавить руками.
		if _, err := s.svc.SourceFromPortalTask(ctx, t.ID, portal); err != nil {
			s.log.Warn("постановка задачи портала не заведена источником",
				"задача", t.ID, "ошибка", err)
		}
	}
	writeJSON(w, http.StatusCreated, newTask(t, s.svc.Now()))
}

// portalTasks отдаёт задачи портала для выбора в форме.
//
// Ручка чатов рядом осталась намеренно: форма её больше не спрашивает, но
// обсуждение могут вести и в отдельном групповом чате, и тогда закреплять нужно
// именно его. Удалить рабочий путь ради того, чтобы его сегодня не показывают,
// значило бы написать его заново на первую же такую задачу.
func (s *Server) portalTasks(w http.ResponseWriter, r *http.Request) {
	// Без запроса — свежие задачи, чтобы форма открывалась сразу и было из чего
	// выбирать. С запросом — поиск по всему порталу: задач там почти десять
	// тысяч, и нужная чаще всего не из последней полусотни.
	list, err := s.svc.PortalTasks(r.Context(), r.URL.Query().Get("q"), 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newTaskOptions(s.svc.PortalConfigured(), list))
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	t, err := s.svc.Task(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newTask(t, s.svc.Now()))
}

// board — всё, что нужно странице задачи, одним ответом.
//
// Отдельный маршрут, а не четыре запроса из браузера: срез читают целиком, и
// части его должны быть из одной сборки. Иначе на экране окажется готовность из
// версии 3 рядом со схемой из версии 2.
type board struct {
	Task     task      `json:"task"`
	Slice    slice     `json:"slice"`
	Diagrams []diagram `json:"diagrams"`
	Sources  []source  `json:"sources"`
	Chats    []chat    `json:"chats"`
	Facts    int       `json:"facts"`

	// Versions — история сборок. Едет вместе с доской, а не отдельным запросом:
	// список короткий, а лишний поход в браузере — лишний повод показать
	// страницу наполовину собранной.
	Versions []sliceRef `json:"versions"`
}

func (s *Server) board(w http.ResponseWriter, r *http.Request) {
	ctx, id, now := r.Context(), r.PathValue("id"), s.svc.Now()

	t, err := s.svc.Task(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sl, err := s.svc.Slice(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.svc.Sources(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	procs, err := s.svc.Processes(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	facts, err := s.svc.Facts(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	links, err := s.svc.TaskChats(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	versions, err := s.svc.SliceVersions(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	edited, err := s.svc.EditedFields(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	srcs := newSources(list)
	out := board{
		Task:     newTask(t, now),
		Slice:    newSlice(sl, t, list, now, edited),
		Diagrams: newDiagrams(procs, srcs),
		Sources:  make([]source, 0, len(list)),
		Chats:    newLinks(links),
		Facts:    len(facts),
		Versions: newSliceRefs(versions),
	}
	for _, src := range list {
		out.Sources = append(out.Sources, newSource(src, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// pull переносит новые сообщения закреплённых чатов в реестр.
//
// Срез после этого не пересобирается. Перенос переписки и сборка среза —
// разные события: сообщение попадает в источники, только если на него сослался
// разбор, и решать это подтяжке нечем. Пересобрать срез человек нажмёт
// отдельно, увидев, что нового приехало.
func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.PullChats(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newPullReport(res))
}

func (s *Server) slice(w http.ResponseWriter, r *http.Request) {
	s.writeSlice(w, r, http.StatusOK, s.svc.Slice)
}

// rebuild пересобирает срез и отвечает, что из этого вышло.
//
// Ответ — короткая сводка, а не сам срез: страница всё равно перечитывает доску
// целиком, а человеку нужно понять, появилась версия или нет. Раньше кнопка
// молча заводила новую версию при любом нажатии, и история заполнялась
// одинаковыми записями.
func (s *Server) rebuild(w http.ResponseWriter, r *http.Request) {
	sl, err := s.svc.Rebuild(r.Context(), r.PathValue("id"))

	switch {
	case errors.Is(err, service.ErrNoChanges):
		writeJSON(w, http.StatusOK, rebuildReport{
			Version: sl.Version,
			Text: fmt.Sprintf("материал не менялся — версия %d осталась прежней",
				sl.Version),
		})
	case err != nil:
		s.fail(w, r, err)
	default:
		writeJSON(w, http.StatusOK, rebuildReport{
			Built:   true,
			Version: sl.Version,
			Text:    fmt.Sprintf("собрана версия %d", sl.Version),
		})
	}
}

// rebuildReport — итог пересборки для человека.
type rebuildReport struct {
	// Built отличает собранную версию от оставленной прежней. Без него «версия
	// 3» на экране ничего не говорит: она могла и появиться, и остаться.
	Built   bool   `json:"built"`
	Version int    `json:"version"`
	Text    string `json:"text"`
}

// writeSlice выполняет получение или пересборку среза и отдаёт представление.
//
// get — либо Slice, либо Rebuild: снаружи это один и тот же ответ, разница только
// в том, берётся срез готовым или собирается заново.
func (s *Server) writeSlice(
	w http.ResponseWriter,
	r *http.Request,
	code int,
	get func(context.Context, string) (domain.Slice, error),
) {
	ctx, id := r.Context(), r.PathValue("id")

	sl, err := get(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t, err := s.svc.Task(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.svc.Sources(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	edited, err := s.svc.EditedFields(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, code, newSlice(sl, t, list, s.svc.Now(), edited))
}

func (s *Server) taskSources(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Sources(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]source, 0, len(list))
	for _, src := range list {
		out = append(out, newSource(src, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// sourceRequest — источник, как его присылает форма.
type sourceRequest struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`

	// Author и OccurredAt заполняются у материала, который не сам про себя
	// рассказывает: у сводки с мостика автор и дата планёрки известны человеку,
	// а из текста их не вычитать. Пусто — обычный случай, и подставлять на их
	// место сегодняшний день и имя PM нельзя: срез отличает дату события от
	// даты загрузки.
	Author     string `json:"author"`
	OccurredAt date   `json:"occurredAt"`
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	if err := readJSON(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	src, err := s.svc.AddSource(r.Context(), domain.Source{
		TaskID:     r.PathValue("id"),
		Kind:       domain.SourceKind(req.Kind),
		Title:      req.Title,
		Body:       req.Body,
		Author:     req.Author,
		OccurredAt: req.OccurredAt.Time,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newSource(src, false))
}

func (s *Server) source(w http.ResponseWriter, r *http.Request) {
	src, err := s.svc.Source(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newSource(src, true))
}

func (s *Server) processes(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")

	procs, err := s.svc.Processes(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.svc.Sources(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newDiagrams(procs, newSources(list)))
}

// factRow — запись журнала фактов.
//
// Журнал отдаётся как есть, без сборки в срез: это отладочное окно, через
// которое видно, что именно аналитик утверждал и с какой уверенностью.
type factRow struct {
	ID    string `json:"id"`
	Field string `json:"field"`
	Value value  `json:"value"`
	// Доля и её подпись рядом: доля — чтобы журнал было чем сортировать из curl,
	// подпись — чтобы интерфейсу нечего было округлять.
	Confidence     float64 `json:"confidence"`
	ConfidenceText string  `json:"confidenceText"`
	ObservedAt     string  `json:"observedAt,omitempty"`
	CreatedAt      string  `json:"createdAt"`
}

func (s *Server) facts(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")

	list, err := s.svc.Facts(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	srcList, err := s.svc.Sources(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	srcs := newSources(srcList)

	out := make([]factRow, 0, len(list))
	for _, f := range list {
		row := factRow{
			ID:             f.ID,
			Field:          f.Field,
			Value:          newValue(f.Value, srcs),
			Confidence:     f.Confidence,
			ConfidenceText: ru.Percent(f.Confidence),
			CreatedAt:      f.CreatedAt.Format("02.01.2006, 15:04"),
		}
		if !f.ObservedAt.IsZero() {
			row.ObservedAt = domain.FormatDate(f.ObservedAt)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// --- вспомогательное ---

// dateLayout — вид даты, на котором сходятся поле input[type=date] и разбор
// запроса. Константа, а не литерал по месту: расхождение между тем, что уходит
// в браузер, и тем, что оттуда принимается, тихо обнулило бы дату.
const dateLayout = "2006-01-02"

// date принимает и «2026-08-31» из поля формы, и «31.08.2026» из рук человека, и
// полную метку времени от программы.
type date struct{ time.Time }

func (d *date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("дата должна быть строкой: %w", err)
	}
	if s == "" {
		d.Time = time.Time{}
		return nil
	}
	for _, layout := range []string{time.RFC3339, dateLayout, "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			d.Time = t.UTC()
			return nil
		}
	}
	return fmt.Errorf("дата %q не разобрана", s)
}

func (d date) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte(`""`), nil
	}
	return json.Marshal(d.Format(dateLayout))
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // иначе «→» и «₽» уезжают в \u-последовательности
	enc.SetIndent("", "  ")  // ответ читают руками через curl
	if err := enc.Encode(body); err != nil {
		// Заголовок уже отправлен, сказать клиенту больше нечего.
		slog.Default().Error("ответ не отправлен", "ошибка", err)
	}
}

// readJSON разбирает тело запроса, отбраковывая лишние поля: опечатка в имени
// поля должна возвращать ошибку, а не молча терять значение.
func readJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("тело запроса: %w: %w", err, service.ErrInvalid)
	}
	return nil
}

// fail переводит ошибку слоёв ниже в код ответа.
//
// Текст ошибки уходит клиенту как есть: инструмент локальный, читает его тот же
// человек, что запустил сервер, и подробность здесь полезнее скрытности.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrExists):
		code = http.StatusConflict
	case errors.Is(err, service.ErrInvalid):
		code = http.StatusBadRequest
	}
	var portal *bitrix.Error
	if errors.As(err, &portal) {
		code = http.StatusBadGateway
	}
	if code == http.StatusInternalServerError {
		s.log.Error("запрос не выполнен", "путь", r.URL.Path, "ошибка", err)
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// versions отдаёт историю версий среза задачи.
func (s *Server) versions(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.SliceVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newSliceRefs(list))
}

// version отдаёт конкретную версию среза целиком.
//
// Старая версия читается из хранилища, а не пересобирается: она обязана
// объясняться тем материалом, который был у неё на руках. Пересборка показала
// бы сегодняшние выводы под вчерашним номером.
func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		s.fail(w, r, fmt.Errorf("номер версии %q: %w", r.PathValue("version"), service.ErrInvalid))
		return
	}

	ctx, id := r.Context(), r.PathValue("id")
	sl, err := s.svc.SliceVersion(ctx, id, v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t, err := s.svc.Task(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.svc.Sources(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	edited, err := s.svc.EditedFields(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newSlice(sl, t, list, s.svc.Now(), edited))
}

// sliceText отдаёт срез разметкой Markdown — тем, что можно вставить в чат,
// приложить к письму или сохранить файлом.
//
// Готовый срез, а не пересборка: выгружают то, что человек видит на экране, и
// звать модель ради выгрузки значило бы платить за неё и получить другой текст.
func (s *Server) sliceText(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")

	sl, err := s.svc.Slice(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t, err := s.svc.Task(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.svc.Sources(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	body := report.Markdown(sl, t, list, s.svc.Now())

	// Имя файла — из названия задачи и номера версии: в папке загрузок должно
	// быть видно, что это за срез, без открытия.
	name := fmt.Sprintf("%s v%d.md", fileName(t.Title), sl.Version)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	// Кодировка имени по RFC 5987: кириллица в обычном filename ломается.
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(name))
	_, _ = io.WriteString(w, body)
}

// fileName делает из названия задачи имя файла, годное для любой файловой
// системы. Пустое название заменяется словом: файл «.md» не сохранить.
func fileName(title string) string {
	bad := func(r rune) bool {
		return strings.ContainsRune(`\/:*?"<>|`, r) || r < 0x20
	}
	out := strings.TrimSpace(strings.Map(func(r rune) rune {
		if bad(r) {
			return ' '
		}
		return r
	}, title))
	// Windows не даёт точку в конце имени, а длинные названия задач в портале
	// бывают в две строки.
	out = strings.TrimRight(out, ". ")
	if len([]rune(out)) > 80 {
		out = strings.TrimSpace(string([]rune(out)[:80]))
	}
	if out == "" {
		return "Срез"
	}
	return out
}

// correctRequest — правка одного поля среза.
//
// Автора здесь нет по той же причине, что и в записи журнала: подпись ставит
// сервер из пропуска. Правка в срезе — это утверждение о задаче наравне с
// цитатой из переписки, и чьё оно, выдумывать нельзя.
type correctRequest struct {
	Field string      `json:"field"`
	Edit  domain.Edit `json:"edit"`
}

// correct записывает содержимое поля, названное человеком, и собирает с ним
// новую версию среза.
func (s *Server) correct(w http.ResponseWriter, r *http.Request) {
	var req correctRequest
	if err := readJSON(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}

	author := s.viewer(r)
	s.writeSlice(w, r, http.StatusOK, func(ctx context.Context, id string) (domain.Slice, error) {
		return s.svc.Correct(ctx, id, req.Field, req.Edit, author)
	})
}

// dropCorrections снимает правки поля и возвращает срез без них.
//
// Поле приходит запросом, а не путём: пустое означает «все правки задачи», а
// пустой отрезок пути читался бы как опечатка в адресе.
func (s *Server) dropCorrections(w http.ResponseWriter, r *http.Request) {
	field := strings.TrimSpace(r.URL.Query().Get("field"))
	author := s.viewer(r)

	s.writeSlice(w, r, http.StatusOK, func(ctx context.Context, id string) (domain.Slice, error) {
		sl, _, err := s.svc.DropCorrections(ctx, id, field, author)
		return sl, err
	})
}

// deleteVersion убирает версию среза.
func (s *Server) deleteVersion(w http.ResponseWriter, r *http.Request) {
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		s.fail(w, r, fmt.Errorf("номер версии %q: %w", r.PathValue("version"), service.ErrInvalid))
		return
	}
	if err := s.svc.DeleteSliceVersion(r.Context(), r.PathValue("id"), v); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// compare отвечает, что изменилось между двумя версиями среза.
func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	a, errA := strconv.Atoi(r.URL.Query().Get("a"))
	b, errB := strconv.Atoi(r.URL.Query().Get("b"))
	if errA != nil || errB != nil {
		s.fail(w, r, fmt.Errorf("нужны номера двух версий: %w", service.ErrInvalid))
		return
	}

	before, after, changes, err := s.svc.CompareVersions(r.Context(), r.PathValue("id"), a, b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newDiff(before, after, changes))
}

// incidentRequest — случай, как его присылает форма.
// Поля «кто зафиксировал» здесь нет намеренно: подпись сервер ставит сам, из
// пропуска. Журнал — основание для разговора о деньгах, и принимать подпись с
// формы значило бы позволить подписаться чужим именем.
type incidentRequest struct {
	Employee    string `json:"employee"`
	Project     string `json:"project"`
	TaskID      string `json:"taskId"`
	At          date   `json:"at"`
	Block       string `json:"block"`
	Text        string `json:"text"`
	External    bool   `json:"external"`
	Escalated   bool   `json:"escalated"`
	EscalatedAt date   `json:"escalatedAt"`
	ManagerNote string `json:"managerNote"`
}

// incidents отдаёт журнал случаев вместе со списком блоков KPI.
//
// Блоки едут в том же ответе, что и журнал: список закрытый и короткий, а
// отдельный запрос за ним означал бы, что форму нельзя показать, пока не
// ответили два раза.
func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	if !s.manager(r) {
		s.forbid(w, r)
		return
	}

	ctx := r.Context()
	from, to, err := period(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	list, err := s.svc.IncidentsIn(ctx, strings.TrimSpace(r.URL.Query().Get("task")), from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	titles, err := s.taskTitles(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newJournal(list, titles, from, to))
}

// period читает границы отрезка из запроса. Пустые — «показать всё».
func period(r *http.Request) (time.Time, time.Time, error) {
	read := func(key string) (time.Time, error) {
		raw := strings.TrimSpace(r.URL.Query().Get(key))
		if raw == "" {
			return time.Time{}, nil
		}
		var d date
		if err := d.UnmarshalJSON([]byte(strconv.Quote(raw))); err != nil {
			return time.Time{}, fmt.Errorf("%s=%q: %w", key, raw, service.ErrInvalid)
		}
		return d.Time, nil
	}

	from, err := read("from")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := read("to")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	// Перепутанные местами границы — не ошибка ввода, а описка: «с десятого по
	// первое» человек имел в виду наоборот. Меняем и работаем, а не отказываем.
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		from, to = to, from
	}
	return from, to, nil
}

// taskTitles — справочник названий задач для подстановки в журнал.
func (s *Server) taskTitles(ctx context.Context) (map[string]string, error) {
	tasks, err := s.svc.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	titles := make(map[string]string, len(tasks))
	for _, t := range tasks {
		titles[t.ID] = t.Title
	}
	return titles, nil
}

// updateIncident переписывает запись журнала.
func (s *Server) updateIncident(w http.ResponseWriter, r *http.Request) {
	if !s.manager(r) {
		s.forbid(w, r)
		return
	}

	var req incidentRequest
	if err := readJSON(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}

	// Подпись и дата внесения сюда не приходят и не принимаются: их поставил
	// сервер при первой записи, и правка не отменяет того, что случай
	// зафиксировал такой-то тогда-то.
	in, err := s.svc.UpdateIncident(r.Context(), domain.Incident{
		ID:          r.PathValue("id"),
		Employee:    req.Employee,
		Project:     req.Project,
		TaskID:      req.TaskID,
		At:          req.At.Time,
		Block:       domain.KPIBlock(req.Block),
		Text:        req.Text,
		External:    req.External,
		Escalated:   req.Escalated,
		EscalatedAt: req.EscalatedAt.Time,
		ManagerNote: req.ManagerNote,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newIncidents([]domain.Incident{in}, nil)[0])
}

// deleteIncident убирает запись журнала.
func (s *Server) deleteIncident(w http.ResponseWriter, r *http.Request) {
	if !s.manager(r) {
		s.forbid(w, r)
		return
	}
	if err := s.svc.DeleteIncident(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// journalText отдаёт журнал разметкой — то, с чем идут на разговор о KPI.
func (s *Server) journalText(w http.ResponseWriter, r *http.Request) {
	if !s.manager(r) {
		s.forbid(w, r)
		return
	}

	ctx := r.Context()
	from, to, err := period(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	list, err := s.svc.IncidentsIn(ctx, strings.TrimSpace(r.URL.Query().Get("task")), from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	titles, err := s.taskTitles(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	name := "Журнал инцидентов"
	if p := periodText(from, to); p != "" {
		name += " " + p
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+url.PathEscape(fileName(name)+".md"))
	_, _ = io.WriteString(w, report.Journal(list, titles, periodText(from, to)))
}

func (s *Server) addIncident(w http.ResponseWriter, r *http.Request) {
	if !s.manager(r) {
		s.forbid(w, r)
		return
	}

	var req incidentRequest
	if err := readJSON(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}

	in, err := s.svc.AddIncident(r.Context(), domain.Incident{
		Employee:    req.Employee,
		Project:     req.Project,
		TaskID:      req.TaskID,
		At:          req.At.Time,
		Block:       domain.KPIBlock(req.Block),
		Text:        req.Text,
		External:    req.External,
		Escalated:   req.Escalated,
		EscalatedAt: req.EscalatedAt.Time,
		ManagerNote: req.ManagerNote,
		RecordedBy:  s.viewer(r),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newIncidents([]domain.Incident{in}, nil)[0])
}

// --- вход ---

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// login проверяет пару и выдаёт пропуск.
//
// Ответ на неверную пару один и тот же независимо от того, что именно не
// сошлось. Сообщение «такого логина нет» бесплатно отдаёт подбирающему список
// заведённых входов, после чего перебирать остаётся только пароль.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.fail(w, r, fmt.Errorf("вход не настроен: %w", service.ErrInvalid))
		return
	}

	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.auth.Check(req.Login, req.Password) {
		s.log.Warn("неудачная попытка входа", "логин", req.Login)
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "неверный логин или пароль",
		})
		return
	}

	setSession(w, r, s.auth.Issue(strings.TrimSpace(req.Login)))
	s.log.Info("вход", "логин", req.Login)
	writeJSON(w, http.StatusOK, map[string]string{"login": strings.TrimSpace(req.Login)})
}

// logout стирает пропуск. Работает и без действующей сессии: кнопка «выйти»
// должна выходить, а не спорить.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	clearSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// loginPage отдаёт страницу входа.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	// Вошедшего на страницу входа не пускаем: он пришёл сюда по старой закладке
	// и ждёт реестр, а не форму.
	if c, err := r.Cookie(cookieName); err == nil && s.auth != nil {
		if _, ok := s.auth.Verify(c.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	http.ServeFileFS(w, r, s.web, "login.html")
}

// viewer возвращает логин по пропуску или пусто, если вход не настроен.
//
// Отдельный метод, потому что подпись под записью журнала берётся только
// отсюда: у неё не должно быть второго источника, который однажды разойдётся с
// этим.
func (s *Server) viewer(r *http.Request) string {
	if s.auth == nil {
		return ""
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	login, _ := s.auth.Verify(c.Value)
	return login
}

// manager отвечает, руководитель ли вошедший.
//
// Без настроенного входа — да. Это локальный запуск на петле, где за
// компьютером сидит один человек, и прятать от него журнал не от кого.
func (s *Server) manager(r *http.Request) bool {
	if s.auth == nil {
		return true
	}
	return s.auth.Manager(s.viewer(r))
}

// forbid отказывает в доступе к журналу.
//
// Отдельным ответом, а не 404: делать вид, что журнала не существует, значит
// оставить человека гадать, сломалось у него что-то или так задумано.
func (s *Server) forbid(w http.ResponseWriter, r *http.Request) {
	s.log.Warn("отказ в доступе к журналу", "кто", s.viewer(r), "путь", r.URL.Path)
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "журнал инцидентов доступен только руководителю",
	})
}

// who сообщает, кто вошёл и что ему видно. Нужен интерфейсу, чтобы показать имя,
// кнопку выхода и решить, рисовать ли журнал.
func (s *Server) who(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"login":   s.viewer(r),
		"manager": s.manager(r),
	})
}
