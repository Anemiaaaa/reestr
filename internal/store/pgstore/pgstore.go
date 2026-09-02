// Пакет pgstore — хранилище реестра в PostgreSQL.
//
// Реализует тот же интерфейс store.Store, что и файловое хранилище, и обязан
// вести себя ровно так же: одинаковый порядок выдачи, одинаковые ошибки,
// одинаковое отношение к пустым значениям. Сервис не должен уметь отличать одно
// от другого — иначе выбор хранилища перестанет быть настройкой и станет
// решением, влияющим на поведение.
//
// Отличий от файлового хранилища два, и оба намеренные:
//
//   - версия среза не может быть записана дважды: пара «задача, номер» —
//     первичный ключ. Файловое хранилище это ограничение тоже соблюдает, его
//     туда добавили ради совпадения;
//   - нулевое время хранится как NULL. «Дата не заполнена» и «01.01.0001» —
//     разные утверждения, и срез обязан их различать.
//
// Методов правки и удаления здесь нет по той же причине, что и в интерфейсе:
// источники и факты — журнал, срез из них выводится. Правка задним числом
// переписала бы уже собранные версии.
package pgstore

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
)

//go:embed schema.sql
var schemaFS embed.FS

// Store — хранилище реестра в PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// Проверка на этапе сборки: расхождение с интерфейсом должно ломать сборку, а
// не проявляться на живом сервисе.
var _ store.Store = (*Store)(nil)

// Open подключается к базе, проверяет связь и применяет схему.
//
// Схема применяется при каждом запуске: все выражения в ней idempotent. Это
// сознательно простое решение — пока база одна, отдельный инструмент миграций
// добавил бы шаг развёртывания, не решив ни одной задачи.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("разбор строки подключения: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("подключение к базе: %w", err)
	}

	// Соединение проверяется сразу: сервис, поднявшийся с недоступной базой,
	// сломается на первом запросе PM, а не на старте, — и разбираться придётся
	// уже по жалобе.
	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(ping); err != nil {
		pool.Close()
		return nil, fmt.Errorf("база не отвечает: %w", err)
	}

	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("чтение схемы: %w", err)
	}
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		pool.Close()
		return nil, fmt.Errorf("применение схемы: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close закрывает пул соединений.
func (s *Store) Close() { s.pool.Close() }

// Empty сообщает, что в реестре ещё нет ни одной задачи. По этому признаку
// сервис решает, нужно ли наполнять пустую базу примером.
func (s *Store) Empty(ctx context.Context) (bool, error) {
	var empty bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM tasks)`).Scan(&empty)
	if err != nil {
		return false, fmt.Errorf("проверка пустоты реестра: %w", err)
	}
	return empty, nil
}

// --- задачи ---

const taskCols = `id, project, title, author, assignee, opened_at, deadline, budget`

func scanTask(row pgx.Row) (domain.Task, error) {
	var (
		opened, deadline *time.Time
		out              domain.Task
	)
	err := row.Scan(&out.ID, &out.Project, &out.Title, &out.Author, &out.Assignee,
		&opened, &deadline, &out.Budget)
	if err != nil {
		return domain.Task{}, err
	}
	out.OpenedAt = timeOf(opened)
	out.Deadline = timeOf(deadline)
	return out, nil
}

func (s *Store) CreateTask(ctx context.Context, t domain.Task) error {
	const q = `INSERT INTO tasks (` + taskCols + `)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	           ON CONFLICT (id) DO NOTHING`

	tag, err := s.pool.Exec(ctx, q, t.ID, t.Project, t.Title, t.Author, t.Assignee,
		nullTime(t.OpenedAt), nullTime(t.Deadline), t.Budget)
	if err != nil {
		return fmt.Errorf("запись задачи %s: %w", t.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("задача %s: %w", t.ID, store.ErrExists)
	}
	return nil
}

func (s *Store) Task(ctx context.Context, id string) (domain.Task, error) {
	const q = `SELECT ` + taskCols + ` FROM tasks WHERE id = $1`

	out, err := scanTask(s.pool.QueryRow(ctx, q, id))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Task{}, fmt.Errorf("задача %s: %w", id, store.ErrNotFound)
	case err != nil:
		return domain.Task{}, fmt.Errorf("чтение задачи %s: %w", id, err)
	}
	return out, nil
}

func (s *Store) Tasks(ctx context.Context) ([]domain.Task, error) {
	// Свежими сверху. seq добавлен вторым ключом, чтобы задачи, открытые одним
	// днём, выводились в одном порядке от запроса к запросу: список задач не
	// должен перетасовываться сам по себе.
	const q = `SELECT ` + taskCols + ` FROM tasks
	           ORDER BY opened_at DESC NULLS LAST, seq`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("чтение задач: %w", err)
	}
	defer rows.Close()

	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("чтение задач: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- закреплённые чаты ---

const chatLinkCols = `task_id, system, dialog_id, title, external_task_id, last_message_id, last_sync_at, linked_at`

func (s *Store) LinkChat(ctx context.Context, link domain.ChatLink) error {
	// Задача проверяется отдельным запросом, чтобы её отсутствие пришло как
	// ErrNotFound, а не как нарушение внешнего ключа: снаружи это ответ «нет
	// такой задачи», а не сбой базы.
	if _, err := s.Task(ctx, link.TaskID); err != nil {
		return err
	}

	// Курсора и даты закрепления в списке обновляемых полей нет намеренно.
	// Повторный выбор того же чата — уточнение подписи: он не должен ни
	// заставлять перечитывать переписку с начала, ни переписывать дату, когда
	// чат выбрали впервые. Задача портала обновляется вместе с подписью и по той
	// же причине: обе пришли от человека, выбравшего чат, а не от подтяжки.
	const q = `INSERT INTO chat_links (` + chatLinkCols + `)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	           ON CONFLICT (task_id, system, dialog_id) DO UPDATE SET
	               title = EXCLUDED.title,
	               external_task_id = EXCLUDED.external_task_id`

	_, err := s.pool.Exec(ctx, q, link.TaskID, link.System, link.DialogID, link.Title,
		link.ExternalTaskID, link.LastMessageID, nullTime(link.LastSyncAt), nullTime(link.LinkedAt))
	if err != nil {
		return fmt.Errorf("закрепление чата %s за задачей %s: %w", link.DialogID, link.TaskID, err)
	}
	return nil
}

func (s *Store) ChatLinks(ctx context.Context, taskID string) ([]domain.ChatLink, error) {
	const q = `SELECT ` + chatLinkCols + ` FROM chat_links
	           WHERE task_id = $1 ORDER BY seq`

	rows, err := s.pool.Query(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("чтение чатов задачи %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []domain.ChatLink
	for rows.Next() {
		var (
			l              domain.ChatLink
			synced, linked *time.Time
		)
		err := rows.Scan(&l.TaskID, &l.System, &l.DialogID, &l.Title,
			&l.ExternalTaskID, &l.LastMessageID, &synced, &linked)
		if err != nil {
			return nil, fmt.Errorf("чтение чатов задачи %s: %w", taskID, err)
		}
		l.LastSyncAt = timeOf(synced)
		l.LinkedAt = timeOf(linked)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) AdvanceChatCursor(ctx context.Context, taskID, system, dialogID string, lastMessageID int, syncedAt time.Time) error {
	const q = `UPDATE chat_links SET last_message_id = $4, last_sync_at = $5
	           WHERE task_id = $1 AND system = $2 AND dialog_id = $3`

	tag, err := s.pool.Exec(ctx, q, taskID, system, dialogID, lastMessageID, nullTime(syncedAt))
	if err != nil {
		return fmt.Errorf("курсор чата %s задачи %s: %w", dialogID, taskID, err)
	}
	// Ни одной затронутой строки — связи нет. Отдельный SELECT для этого не
	// нужен: UPDATE уже сказал всё, что требовалось.
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("чат %s задачи %s: %w", dialogID, taskID, store.ErrNotFound)
	}
	return nil
}

// --- источники ---

const sourceCols = `id, task_id, parent_id, kind, title, body, author,
	occurred_at, uploaded_at, ext_system, ext_chat_id, ext_msg_id, ext_url`

func scanSource(row pgx.Row) (domain.Source, error) {
	var (
		out                domain.Source
		occurred, uploaded *time.Time
	)
	err := row.Scan(&out.ID, &out.TaskID, &out.ParentID, &out.Kind, &out.Title,
		&out.Body, &out.Author, &occurred, &uploaded,
		&out.External.System, &out.External.ChatID, &out.External.MessageID, &out.External.URL)
	if err != nil {
		return domain.Source{}, err
	}
	out.OccurredAt = timeOf(occurred)
	out.UploadedAt = timeOf(uploaded)
	return out, nil
}

func (s *Store) AddSource(ctx context.Context, src domain.Source) error {
	// Задача проверяется до вставки, а не внешним ключом: у общего источника
	// TaskID пуст, и ключ запретил бы его вовсе.
	if src.TaskID != "" {
		if _, err := s.Task(ctx, src.TaskID); err != nil {
			return err
		}
	}

	const q = `INSERT INTO sources (` + sourceCols + `, ext_key)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

	_, err := s.pool.Exec(ctx, q, src.ID, src.TaskID, src.ParentID, src.Kind,
		src.Title, src.Body, src.Author,
		nullTime(src.OccurredAt), nullTime(src.UploadedAt),
		src.External.System, src.External.ChatID, src.External.MessageID, src.External.URL,
		src.External.Key())
	if err != nil {
		// Уникальный индекс по ключу оригинала — защита от повторной подтяжки
		// того же сообщения из чата. Это ожидаемый исход, а не сбой.
		if isUnique(err) {
			if key := src.External.Key(); key != "" {
				return fmt.Errorf("источник %s: %w", key, store.ErrExists)
			}
			return fmt.Errorf("источник %s: %w", src.ID, store.ErrExists)
		}
		return fmt.Errorf("запись источника %s: %w", src.ID, err)
	}
	return nil
}

func (s *Store) Source(ctx context.Context, id string) (domain.Source, error) {
	const q = `SELECT ` + sourceCols + ` FROM sources WHERE id = $1`

	out, err := scanSource(s.pool.QueryRow(ctx, q, id))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Source{}, fmt.Errorf("источник %s: %w", id, store.ErrNotFound)
	case err != nil:
		return domain.Source{}, fmt.Errorf("чтение источника %s: %w", id, err)
	}
	return out, nil
}

// Sources отдаёт материалы, привязанные именно к этой задаче. Общие источники
// с пустой задачей сюда не попадают: их набор для каждой задачи свой и
// определяется не привязкой, а разбором.
//
// Порядок — хроника: по дате материала, затем по порядку поступления. Материал
// без даты идёт первым, потому что его дату ещё предстоит выяснить, и внизу
// списка, среди свежего, он выглядел бы как самое позднее событие.
func (s *Store) Sources(ctx context.Context, taskID string) ([]domain.Source, error) {
	const q = `SELECT ` + sourceCols + ` FROM sources WHERE task_id = $1
	           ORDER BY uploaded_at NULLS FIRST, seq`

	rows, err := s.pool.Query(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("чтение источников задачи %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []domain.Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("чтение источников задачи %s: %w", taskID, err)
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (s *Store) SourceByExternal(ctx context.Context, key string) (domain.Source, error) {
	if key == "" {
		return domain.Source{}, fmt.Errorf("внешний адрес пуст: %w", store.ErrNotFound)
	}

	const q = `SELECT ` + sourceCols + ` FROM sources WHERE ext_key = $1`

	out, err := scanSource(s.pool.QueryRow(ctx, q, key))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Source{}, fmt.Errorf("источник %s: %w", key, store.ErrNotFound)
	case err != nil:
		return domain.Source{}, fmt.Errorf("чтение источника %s: %w", key, err)
	}
	return out, nil
}

// SlicesUsing отвечает на обратный вопрос: в каких версиях этот материал
// пригодился. Без него источник — просто текст в базе, и непонятно, повлиял ли
// он на выводы.
func (s *Store) SlicesUsing(ctx context.Context, sourceID string) ([]domain.SliceRef, error) {
	if sourceID == "" {
		return nil, nil
	}

	const q = `SELECT sl.task_id, sl.version, sl.built_at
	           FROM slice_sources ss
	           JOIN slices sl ON sl.task_id = ss.task_id AND sl.version = ss.version
	           WHERE ss.source_id = $1
	           ORDER BY sl.task_id, sl.version`

	rows, err := s.pool.Query(ctx, q, sourceID)
	if err != nil {
		return nil, fmt.Errorf("поиск срезов по источнику %s: %w", sourceID, err)
	}
	defer rows.Close()

	var out []domain.SliceRef
	for rows.Next() {
		var (
			ref   domain.SliceRef
			built *time.Time
		)
		if err := rows.Scan(&ref.TaskID, &ref.Version, &built); err != nil {
			return nil, fmt.Errorf("поиск срезов по источнику %s: %w", sourceID, err)
		}
		ref.BuiltAt = timeOf(built)
		out = append(out, ref)
	}
	return out, rows.Err()
}

// --- факты ---

func (s *Store) AddFacts(ctx context.Context, facts []domain.Fact) error {
	if len(facts) == 0 {
		return nil
	}

	// Задачи проверяются до вставки, хотя внешний ключ на них уже есть. Ключ
	// сообщает о нарушении своим кодом состояния, и наружу ушла бы ошибка базы
	// вместо store.ErrNotFound — та же ситуация, что в файловом хранилище,
	// описывалась бы по-другому. Ключ остаётся как страховка, но ответ на
	// «такой задачи нет» даёт эта проверка.
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

	const q = `INSERT INTO facts (id, task_id, field, value, confidence, observed_at, created_at)
	           VALUES ($1, $2, $3, $4, $5, $6, $7)`

	batch := &pgx.Batch{}
	for _, f := range facts {
		batch.Queue(q, f.ID, f.TaskID, f.Field, f.Value, f.Confidence,
			nullTime(f.ObservedAt), nullTime(f.CreatedAt))
	}

	// Пачкой и в одной транзакции: набор фактов — результат одного разбора, и
	// половина разбора в базе хуже, чем ни одного.
	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("запись фактов: %w", err)
	}
	return nil
}

// Facts отдаёт факты в порядке извлечения: последовательность разбора сама по
// себе информация.
func (s *Store) Facts(ctx context.Context, taskID string) ([]domain.Fact, error) {
	const q = `SELECT id, task_id, field, value, confidence, observed_at, created_at
	           FROM facts WHERE task_id = $1 ORDER BY seq`

	rows, err := s.pool.Query(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("чтение фактов задачи %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []domain.Fact
	for rows.Next() {
		var (
			f                 domain.Fact
			observed, created *time.Time
		)
		err := rows.Scan(&f.ID, &f.TaskID, &f.Field, &f.Value, &f.Confidence, &observed, &created)
		if err != nil {
			return nil, fmt.Errorf("чтение фактов задачи %s: %w", taskID, err)
		}
		f.ObservedAt = timeOf(observed)
		f.CreatedAt = timeOf(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// --- схемы процессов ---

// PutProcess заменяет схему того же вида: у задачи не может быть двух схем «как
// есть». Новая версия схемы вытесняет прежнюю, а не копится рядом.
func (s *Store) PutProcess(ctx context.Context, p domain.Process) error {
	if _, err := s.Task(ctx, p.TaskID); err != nil {
		return err
	}

	const q = `INSERT INTO processes (task_id, kind, title, nodes, edges, changes, evidence)
	           VALUES ($1, $2, $3, $4, $5, $6, $7)
	           ON CONFLICT (task_id, kind) DO UPDATE SET
	               title = EXCLUDED.title,
	               nodes = EXCLUDED.nodes,
	               edges = EXCLUDED.edges,
	               changes = EXCLUDED.changes,
	               evidence = EXCLUDED.evidence`

	_, err := s.pool.Exec(ctx, q, p.TaskID, p.Kind, p.Title,
		jsonList(p.Nodes), jsonList(p.Edges), jsonList(p.Changes), p.Evidence)
	if err != nil {
		return fmt.Errorf("запись схемы %s задачи %s: %w", p.Kind, p.TaskID, err)
	}
	return nil
}

// Processes отдаёт «как есть» первым: сравнение читают в этом порядке, и обратный
// вывел бы результат раньше причины.
func (s *Store) Processes(ctx context.Context, taskID string) ([]domain.Process, error) {
	const q = `SELECT task_id, kind, title, nodes, edges, changes, evidence
	           FROM processes WHERE task_id = $1
	           ORDER BY CASE WHEN kind = 'as_is' THEN 0 ELSE 1 END, kind`

	rows, err := s.pool.Query(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("чтение схем задачи %s: %w", taskID, err)
	}
	defer rows.Close()

	var out []domain.Process
	for rows.Next() {
		var p domain.Process
		err := rows.Scan(&p.TaskID, &p.Kind, &p.Title, &p.Nodes, &p.Edges, &p.Changes, &p.Evidence)
		if err != nil {
			return nil, fmt.Errorf("чтение схем задачи %s: %w", taskID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- срезы ---

// SaveSlice сохраняет версию целиком и рядом — список использованных
// источников. Оба действия в одной транзакции: версия без списка источников не
// объясняет, на чём построена, а список без версии ни на что не указывает.
func (s *Store) SaveSlice(ctx context.Context, sl domain.Slice) error {
	if _, err := s.Task(ctx, sl.TaskID); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("запись среза задачи %s: %w", sl.TaskID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // после Commit откат — не ошибка

	const qSlice = `INSERT INTO slices
	                (task_id, version, doc, built_at, stage, readiness, analyst, considered)
	                VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err = tx.Exec(ctx, qSlice, sl.TaskID, sl.Version, sl, nullTime(sl.BuiltAt),
		sl.Status.Stage.Text, sl.Status.Readiness.Text, sl.Analyst, sl.Considered)
	if err != nil {
		if isUnique(err) {
			return fmt.Errorf("срез задачи %s версии %d: %w", sl.TaskID, sl.Version, store.ErrExists)
		}
		return fmt.Errorf("запись среза задачи %s: %w", sl.TaskID, err)
	}

	// Указатель на источник — множество, а не список: если срез сослался на
	// один материал дважды, повтор пропускается. Потерять номер второго
	// упоминания не страшно, а вот уронить из-за него всю версию — страшно.
	const qSource = `INSERT INTO slice_sources (task_id, version, source_id, ord)
	                 VALUES ($1, $2, $3, $4)
	                 ON CONFLICT (task_id, version, source_id) DO NOTHING`

	for i, id := range sl.SourceIDs {
		if _, err := tx.Exec(ctx, qSource, sl.TaskID, sl.Version, id, i); err != nil {
			return fmt.Errorf("запись источников среза задачи %s: %w", sl.TaskID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("запись среза задачи %s: %w", sl.TaskID, err)
	}
	return nil
}

func (s *Store) LatestSlice(ctx context.Context, taskID string) (domain.Slice, error) {
	const q = `SELECT doc FROM slices WHERE task_id = $1 ORDER BY version DESC LIMIT 1`

	var out domain.Slice
	err := s.pool.QueryRow(ctx, q, taskID).Scan(&out)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Slice{}, fmt.Errorf("срез задачи %s: %w", taskID, store.ErrNotFound)
	case err != nil:
		return domain.Slice{}, fmt.Errorf("чтение среза задачи %s: %w", taskID, err)
	}
	return out, nil
}

func (s *Store) NextSliceVersion(ctx context.Context, taskID string) (int, error) {
	const q = `SELECT COALESCE(MAX(version), 0) + 1 FROM slices WHERE task_id = $1`

	var next int
	if err := s.pool.QueryRow(ctx, q, taskID).Scan(&next); err != nil {
		return 0, fmt.Errorf("номер следующей версии задачи %s: %w", taskID, err)
	}
	return next, nil
}

// --- вспомогательное ---

// nullTime переводит нулевое время в NULL. «Дата не заполнена» и «01.01.0001» —
// разные утверждения: первое срез обязан показать пробелом, второе выглядело бы
// как заполненное поле.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// timeOf — обратный перевод: NULL становится нулевым временем.
func timeOf(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

// jsonList страхует пустой срез от превращения в JSON null: колонка объявлена
// NOT NULL, а пустой список узлов — законное состояние схемы.
func jsonList[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// isUnique отличает нарушение уникальности от прочих сбоев базы. Повторная
// подтяжка сообщения и повторная запись версии — ожидаемые исходы, у них своя
// ошибка store.ErrExists.
func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
