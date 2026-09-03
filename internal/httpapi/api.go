package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
	"github.com/Anemiaaaa/reestr/internal/service"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// Server — HTTP-обвязка вокруг сервиса.
type Server struct {
	svc     *service.Service
	log     *slog.Logger
	handler http.Handler
}

// New собирает сервер. web — файлы интерфейса; откуда они взялись, из embed или
// с диска, транспорту знать не нужно.
func New(svc *service.Service, web fs.FS, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{svc: svc, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/bitrix/tasks", s.portalTasks)
	mux.HandleFunc("GET /api/bitrix/chats", s.chats)
	mux.HandleFunc("GET /api/tasks", s.tasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.task)
	mux.HandleFunc("GET /api/tasks/{id}/board", s.board)
	mux.HandleFunc("GET /api/tasks/{id}/slice", s.slice)
	mux.HandleFunc("POST /api/tasks/{id}/slice/rebuild", s.rebuild)
	mux.HandleFunc("POST /api/tasks/{id}/pull", s.pull)
	mux.HandleFunc("GET /api/tasks/{id}/sources", s.taskSources)
	mux.HandleFunc("POST /api/tasks/{id}/sources", s.addSource)
	mux.HandleFunc("GET /api/tasks/{id}/processes", s.processes)
	mux.HandleFunc("GET /api/tasks/{id}/facts", s.facts)
	mux.HandleFunc("GET /api/sources/{id}", s.source)
	if web != nil {
		mux.Handle("GET /", http.FileServerFS(web))
	}

	s.handler = withRecover(log, withLogging(log, mux))
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
	list, err := s.svc.PortalTasks(r.Context(), 50)
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

	srcs := newSources(list)
	out := board{
		Task:     newTask(t, now),
		Slice:    newSlice(sl, t, list, now),
		Diagrams: newDiagrams(procs, srcs),
		Sources:  make([]source, 0, len(list)),
		Chats:    newLinks(links),
		Facts:    len(facts),
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
	writeJSON(w, code, newSlice(sl, t, list, s.svc.Now()))
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
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	var req sourceRequest
	if err := readJSON(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	src, err := s.svc.AddSource(r.Context(), domain.Source{
		TaskID: r.PathValue("id"),
		Kind:   domain.SourceKind(req.Kind),
		Title:  req.Title,
		Body:   req.Body,
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
