// Package jsonstore хранит реестр в одном JSON-файле.
//
// Выбор осознанный: на этом этапе важнее, чтобы сервис запускался одной
// командой и данные можно было открыть текстовым редактором, чем чтобы он
// держал нагрузку. Всё состояние живёт в памяти под мьютексом, файл — способ
// пережить перезапуск.
//
// Когда файла станет мало, рядом появится store/sqlite или store/postgres:
// сервис знает только интерфейс store.Store и разницы не заметит.
package jsonstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// Store реализует store.Store. Проверка на этапе компиляции: если интерфейс
// разойдётся с реализацией, сборка упадёт здесь, а не в сервисе.
var _ store.Store = (*Store)(nil)

// state — то, что попадает в файл.
type state struct {
	Tasks     []domain.Task       `json:"tasks"`
	ChatLinks []domain.ChatLink   `json:"chatLinks"`
	Messages  []domain.RawMessage `json:"rawMessages"`
	Sources   []domain.Source     `json:"sources"`
	Facts     []domain.Fact       `json:"facts"`
	Processes []domain.Process    `json:"processes"`
	Slices    []domain.Slice      `json:"slices"`
	Incidents []domain.Incident   `json:"incidents"`
}

// Store — хранилище реестра в JSON-файле.
type Store struct {
	path string

	mu sync.RWMutex
	st state
}

// Open открывает хранилище по пути к файлу, создавая его при необходимости.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("каталог данных: %w", err)
		}
	}

	s := &Store{path: path}

	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return s, nil // пустой реестр, файл появится при первой записи
	case err != nil:
		return nil, fmt.Errorf("чтение %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.st); err != nil {
		return nil, fmt.Errorf("разбор %s: %w", path, err)
	}
	return s, nil
}

// Empty сообщает, пуст ли реестр. Нужно, чтобы при первом запуске положить
// демонстрационные данные и не затирать ими работу при следующем.
//
// Ошибку не возвращает никогда: состояние уже в памяти. Она есть в сигнатуре
// потому, что у хранилища в базе тот же вопрос требует запроса, а разные
// сигнатуры сделали бы интерфейс невыполнимым для одной из реализаций.
func (s *Store) Empty(_ context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.st.Tasks) == 0, nil
}

// persist пишет состояние на диск. Вызывается под удержанной блокировкой
// записи.
//
// Запись идёт во временный файл и затем переименованием: так прерванный
// процесс оставит прежний файл целым, а не половину нового.
func (s *Store) persist() error {
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return fmt.Errorf("сериализация состояния: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("запись %s: %w", tmp, err)
	}
	if err := replace(tmp, s.path); err != nil {
		return fmt.Errorf("замена %s: %w", s.path, err)
	}
	return nil
}

// replace подменяет целевой файл временным, повторяя попытку при отказе.
//
// На Windows переименование поверх существующего файла упирается в сторонние
// процессы: антивирус и индексатор держат только что записанный файл первые
// миллисекунды, и os.Rename возвращает «Access is denied». Отказ временный,
// поэтому здесь пауза и повтор — иначе чужое сканирование роняет сервер на
// старте, посреди заполнения хранилища.
func replace(tmp, path string) error {
	const attempts = 10

	var err error
	for i := range attempts {
		if err = os.Rename(tmp, path); err == nil {
			return nil
		}
		// Временного файла нет — это ошибка в коде, и повтор её не вылечит.
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(i+1) * 10 * time.Millisecond)
	}
	return err
}

// CreateTask сохраняет новую задачу.
func (s *Store) CreateTask(_ context.Context, t domain.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.st.Tasks {
		if existing.ID == t.ID {
			return fmt.Errorf("задача %s: %w", t.ID, store.ErrExists)
		}
	}
	s.st.Tasks = append(s.st.Tasks, t)
	return s.persist()
}

// Task возвращает задачу по идентификатору.
func (s *Store) Task(_ context.Context, id string) (domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.st.Tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return domain.Task{}, fmt.Errorf("задача %s: %w", id, store.ErrNotFound)
}

// Tasks возвращает все задачи, свежие первыми.
func (s *Store) Tasks(_ context.Context) ([]domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]domain.Task, len(s.st.Tasks))
	copy(out, s.st.Tasks)
	sort.SliceStable(out, func(i, j int) bool { return out[i].OpenedAt.After(out[j].OpenedAt) })
	return out, nil
}

// LinkChat закрепляет за задачей чат внешней системы. Повторное закрепление
// того же чата обновляет только подпись.
func (s *Store) LinkChat(ctx context.Context, link domain.ChatLink) error {
	// Задача проверяется до взятия блокировки: Task берёт её сам, а повторный
	// захват того же мьютекса — это тупик.
	if _, err := s.Task(ctx, link.TaskID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.st.ChatLinks {
		if existing.TaskID != link.TaskID || existing.System != link.System || existing.DialogID != link.DialogID {
			continue
		}
		// Присваиваются поля выбора, а не связь целиком. Курсор и дата
		// закрепления принадлежат не тому, кто выбирает чат: перезапись связью из
		// аргумента откатила бы курсор в ноль, и вся переписка приехала бы вторым
		// экземпляром.
		s.st.ChatLinks[i].Title = link.Title
		s.st.ChatLinks[i].ExternalTaskID = link.ExternalTaskID
		return s.persist()
	}

	s.st.ChatLinks = append(s.st.ChatLinks, link)
	return s.persist()
}

// ChatLinks возвращает чаты задачи в порядке закрепления. Сортировки нет
// намеренно: в файле связи лежат в том порядке, в каком их добавляли, и это уже
// нужный порядок.
func (s *Store) ChatLinks(_ context.Context, taskID string) ([]domain.ChatLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.ChatLink
	for _, l := range s.st.ChatLinks {
		if l.TaskID == taskID {
			out = append(out, l)
		}
	}
	return out, nil
}

// AdvanceChatCursor сдвигает курсор синхронизации закреплённого чата.
func (s *Store) AdvanceChatCursor(_ context.Context, taskID, system, dialogID string, lastMessageID int, syncedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.st.ChatLinks {
		if existing.TaskID != taskID || existing.System != system || existing.DialogID != dialogID {
			continue
		}
		s.st.ChatLinks[i].LastMessageID = lastMessageID
		s.st.ChatLinks[i].LastSyncAt = syncedAt
		return s.persist()
	}
	// Проверять существование задачи отдельно незачем: связи без задачи не
	// бывает, и её отсутствие — тот же ответ «закреплять курсор некуда».
	return fmt.Errorf("чат %s задачи %s: %w", dialogID, taskID, store.ErrNotFound)
}

// --- сырые сообщения ---

// AddRawMessages переносит сообщения чата и отвечает, сколько из них новые.
func (s *Store) AddRawMessages(_ context.Context, msgs []domain.RawMessage) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	// Проверка до блокировки и до первой записи: пачка либо ложится целиком,
	// либо не ложится вовсе.
	for _, m := range msgs {
		if m.Key() == "" {
			return 0, fmt.Errorf("сообщение без адреса оригинала: %+v", m.External)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Известные ключи собираются один раз, а не ищутся перебором на каждое
	// сообщение: подтяжка приносит сотню за раз, и перебор по перебору дал бы
	// квадрат на ровном месте.
	known := make(map[string]bool, len(s.st.Messages))
	for _, m := range s.st.Messages {
		known[m.Key()] = true
	}

	added := 0
	for _, m := range msgs {
		key := m.Key()
		// Повтор внутри самой пачки тоже отсекается: портал вправе прислать
		// сообщение дважды в одном ответе, и в базе это поймал бы первичный ключ.
		if known[key] {
			continue
		}
		known[key] = true
		s.st.Messages = append(s.st.Messages, m)
		added++
	}

	if added == 0 {
		// Записывать файл незачем: состояние не изменилось, а лишняя запись —
		// лишний повод его повредить.
		return 0, nil
	}
	if err := s.persist(); err != nil {
		return 0, err
	}
	return added, nil
}

// RawMessages возвращает сообщения диалога: по дате события, при равных датах —
// в порядке переноса. Порядок переноса здесь и есть порядок в срезе файла,
// поэтому сортировка устойчивая.
func (s *Store) RawMessages(_ context.Context, system, dialogID string) ([]domain.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.RawMessage
	for _, m := range s.st.Messages {
		if m.External.System == system && m.External.ChatID == dialogID {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OccurredAt.Before(out[j].OccurredAt)
	})
	return out, nil
}

// AddSource добавляет источник. Источник без задачи допустим: это общая
// сводка, фрагменты из которой ссылаются на неё через ParentID.
func (s *Store) AddSource(ctx context.Context, src domain.Source) error {
	// Задача проверяется до взятия блокировки: Task берёт её сам, а повторный
	// захват того же мьютекса — это тупик.
	if src.TaskID != "" {
		if _, err := s.Task(ctx, src.TaskID); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if key := src.External.Key(); key != "" {
		for _, existing := range s.st.Sources {
			if existing.External.Key() == key {
				return fmt.Errorf("источник %s: %w", key, store.ErrExists)
			}
		}
	}

	s.st.Sources = append(s.st.Sources, src)
	return s.persist()
}

// Source возвращает источник по идентификатору.
func (s *Store) Source(_ context.Context, id string) (domain.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, src := range s.st.Sources {
		if src.ID == id {
			return src, nil
		}
	}
	return domain.Source{}, fmt.Errorf("источник %s: %w", id, store.ErrNotFound)
}

// Sources возвращает источники задачи хроникой: по дате материала, а при равных
// датах — в порядке поступления. Материал без даты идёт первым: его дату ещё
// предстоит выяснить, а внизу списка, среди свежего, он выглядел бы как самое
// позднее событие.
//
// Порядок поступления сохраняет SliceStable: в файле источники лежат в том
// порядке, в каком их добавляли, и для равных дат сортировка его не тронет.
func (s *Store) Sources(_ context.Context, taskID string) ([]domain.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.Source
	for _, src := range s.st.Sources {
		if src.TaskID == taskID {
			out = append(out, src)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UploadedAt.Before(out[j].UploadedAt) })
	return out, nil
}

// SourceByExternal возвращает источник по адресу оригинала во внешней системе.
func (s *Store) SourceByExternal(_ context.Context, key string) (domain.Source, error) {
	if key == "" {
		return domain.Source{}, fmt.Errorf("внешний адрес пуст: %w", store.ErrNotFound)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, src := range s.st.Sources {
		if src.External.Key() == key {
			return src, nil
		}
	}
	return domain.Source{}, fmt.Errorf("источник %s: %w", key, store.ErrNotFound)
}

// SlicesUsing возвращает версии срезов, ссылающиеся на источник, от старых к
// свежим.
func (s *Store) SlicesUsing(_ context.Context, sourceID string) ([]domain.SliceRef, error) {
	if sourceID == "" {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.SliceRef
	for _, sl := range s.st.Slices {
		for _, id := range sl.SourceIDs {
			if id == sourceID {
				out = append(out, sl.Ref())
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TaskID != out[j].TaskID {
			return out[i].TaskID < out[j].TaskID
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// AddFacts добавляет извлечённые факты. Задача должна существовать: факт без
// задачи — запись, к которой ни один срез не обратится.
func (s *Store) AddFacts(ctx context.Context, facts []domain.Fact) error {
	if len(facts) == 0 {
		return nil
	}

	// Задачи проверяются до взятия блокировки: Task берёт её сам, а повторный
	// захват того же мьютекса — это тупик. Проверка до вставки, а не после, ещё
	// и потому, что набор фактов — результат одного разбора: половина разбора в
	// хранилище хуже, чем ни одного.
	seen := make(map[string]bool, len(facts))
	for _, f := range facts {
		if seen[f.TaskID] {
			continue
		}
		if _, err := s.Task(ctx, f.TaskID); err != nil {
			return err
		}
		seen[f.TaskID] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.st.Facts = append(s.st.Facts, facts...)
	return s.persist()
}

// Facts возвращает факты задачи в порядке добавления.
func (s *Store) Facts(_ context.Context, taskID string) ([]domain.Fact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.Fact
	for _, f := range s.st.Facts {
		if f.TaskID == taskID {
			out = append(out, f)
		}
	}
	return out, nil
}

// PutProcess сохраняет схему процесса, заменяя прежнюю схему того же вида.
func (s *Store) PutProcess(ctx context.Context, p domain.Process) error {
	if _, err := s.Task(ctx, p.TaskID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.st.Processes {
		if existing.TaskID == p.TaskID && existing.Kind == p.Kind {
			s.st.Processes[i] = p
			return s.persist()
		}
	}
	s.st.Processes = append(s.st.Processes, p)
	return s.persist()
}

// Processes возвращает схемы задачи: сначала «как есть», затем «как будет».
func (s *Store) Processes(_ context.Context, taskID string) ([]domain.Process, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.Process
	for _, p := range s.st.Processes {
		if p.TaskID == taskID {
			out = append(out, p)
		}
	}
	// Сравнение обязано быть строгим: «как есть» меньше всего остального, но не
	// меньше самой себя. Без второго условия две схемы «как есть» оказались бы
	// меньше друг друга, а на противоречивом сравнении sort вправе выдать любой
	// порядок.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Kind == domain.ProcessAsIs && out[j].Kind != domain.ProcessAsIs
	})
	return out, nil
}

// SaveSlice сохраняет собранный срез как очередную версию.
func (s *Store) SaveSlice(ctx context.Context, sl domain.Slice) error {
	if _, err := s.Task(ctx, sl.TaskID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Версия с таким номером не переписывается. Срез самодостаточен: его читают
	// как свидетельство о том, что было известно на момент сборки, и подмена
	// версии задним числом обессмыслила бы сравнение «было → стало».
	for _, existing := range s.st.Slices {
		if existing.TaskID == sl.TaskID && existing.Version == sl.Version {
			return fmt.Errorf("срез задачи %s версии %d: %w", sl.TaskID, sl.Version, store.ErrExists)
		}
	}

	s.st.Slices = append(s.st.Slices, sl)
	return s.persist()
}

// LatestSlice возвращает последнюю собранную версию среза.
func (s *Store) LatestSlice(_ context.Context, taskID string) (domain.Slice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var latest domain.Slice
	found := false
	for _, sl := range s.st.Slices {
		if sl.TaskID == taskID && (!found || sl.Version > latest.Version) {
			latest, found = sl, true
		}
	}
	if !found {
		return domain.Slice{}, fmt.Errorf("срез задачи %s: %w", taskID, store.ErrNotFound)
	}
	return latest, nil
}

// NextSliceVersion возвращает номер, который получит следующая сборка.
func (s *Store) NextSliceVersion(_ context.Context, taskID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	max := 0
	for _, sl := range s.st.Slices {
		if sl.TaskID == taskID && sl.Version > max {
			max = sl.Version
		}
	}
	return max + 1, nil
}

// SliceVersion возвращает конкретную версию среза задачи.
func (s *Store) SliceVersion(_ context.Context, taskID string, version int) (domain.Slice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, sl := range s.st.Slices {
		if sl.TaskID == taskID && sl.Version == version {
			return sl, nil
		}
	}
	return domain.Slice{}, fmt.Errorf("версия %d задачи %s: %w", version, taskID, store.ErrNotFound)
}

// SliceVersions перечисляет версии среза задачи, свежие первыми.
func (s *Store) SliceVersions(_ context.Context, taskID string) ([]domain.SliceRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.SliceRef
	for _, sl := range s.st.Slices {
		if sl.TaskID == taskID {
			out = append(out, sl.Ref())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// --- журнал инцидентов ---

func (s *Store) AddIncident(ctx context.Context, in domain.Incident) error {
	// Случай без задачи реестра законен: место случая называет Project, а
	// ссылка на задачу лишь связывает запись со срезом.
	if in.TaskID != "" {
		if _, err := s.Task(ctx, in.TaskID); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.st.Incidents {
		if existing.ID == in.ID {
			return fmt.Errorf("случай %s: %w", in.ID, store.ErrExists)
		}
	}
	s.st.Incidents = append(s.st.Incidents, in)
	return s.persist()
}

// Incidents возвращает журнал: свежие случаи первыми. Пустой taskID означает
// «все задачи».
func (s *Store) Incidents(_ context.Context, taskID string) ([]domain.Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []domain.Incident
	for _, in := range s.st.Incidents {
		if taskID == "" || in.TaskID == taskID {
			out = append(out, in)
		}
	}
	// Устойчивая сортировка: при равных датах порядок остаётся тем, в каком
	// случаи вносили.
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}
