// Package service собирает срез из источников.
//
// Здесь проходит главная граница системы. Аналитик (позже — модель) читает
// источники и возвращает утверждения со ссылками на них. Всё остальное —
// готовность, просрочки, возраст блокера, количество пробелов — считается
// здесь, кодом, из тех же данных. Поэтому два запуска на одних источниках дают
// одинаковые числа, и любую цифру в срезе можно проверить руками.
//
// Срез не редактируется. Его пересобирают: источники и факты только
// добавляются, версия увеличивается, прежние версии остаются лежать. Так видно,
// что изменилось между двумя отчётами и почему.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Anemiaaaa/reestr/internal/analyst"
	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
	"github.com/Anemiaaaa/reestr/internal/store"
)

// ErrInvalid возвращается, когда во входных данных не хватает обязательного
// поля.
var ErrInvalid = errors.New("некорректные данные")

// ErrNoChanges возвращается пересборкой, когда материал задачи не менялся с
// прошлой версии. Срез при этом возвращается — прежний, целый и годный.
//
// Ошибкой это названо потому, что вызывающий обязан заметить разницу: он просил
// собрать заново, а собрано не было. Молчаливое «вот вам прежняя версия»
// человек прочитал бы как «разбор ничего нового не нашёл», а это другое
// утверждение.
var ErrNoChanges = errors.New("материал не менялся")

// materialChanged отвечает, появился ли у задачи новый материал после сборки
// версии.
//
// Источники только добавляются, поэтому вопрос сводится к двум: не стало ли их
// больше и нет ли среди них загруженного позже сборки. Второе проверяется
// отдельно от первого не для надёжности, а потому что источник мог быть
// загружен и удалён из подачи — сравнение одних чисел это пропустило бы.
func materialChanged(sources []domain.Source, latest domain.Slice) bool {
	if len(sources) != latest.Considered {
		return true
	}
	for _, src := range sources {
		if src.UploadedAt.After(latest.BuiltAt) {
			return true
		}
	}
	return false
}

// Service — прикладной слой реестра.
type Service struct {
	store   store.Store
	analyst analyst.Analyst
	portal  *bitrix.Client
	now     func() time.Time
	log     *slog.Logger
}

// New собирает сервис. Часы отдельным полем, а не вызовом time.Now по месту:
// срез весь состоит из расстояний между датами, и тесту нужно уметь встать в
// нужный день.
func New(st store.Store, an analyst.Analyst, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:   st,
		analyst: an,
		now:     func() time.Time { return time.Now().UTC() },
		log:     log,
	}
}

// Clock подменяет часы сервиса.
func (s *Service) Clock(f func() time.Time) { s.now = f }

// Portal подключает портал Bitrix24.
//
// Отдельным вызовом, а не параметром New, по той же причине, что и Clock:
// портал нужен двум методам из двадцати, и большинству вызовов сервиса — в том
// числе всем проверкам — он не нужен вовсе. Без него реестр работает целиком,
// теряя только автоматический источник.
func (s *Service) Portal(c *bitrix.Client) { s.portal = c }

// PortalConfigured сообщает, настроен ли портал. Проверка на nil здесь, а не у
// вызывающего: сервис собирают и без портала, и это нормальный режим.
func (s *Service) PortalConfigured() bool { return s.portal != nil && s.portal.Configured() }

// Now сообщает текущее время сервиса.
func (s *Service) Now() time.Time { return s.now() }

// Analyst сообщает, кто разбирает источники.
func (s *Service) Analyst() string { return s.analyst.Name() }

// Tasks возвращает все задачи.
func (s *Service) Tasks(ctx context.Context) ([]domain.Task, error) {
	return s.store.Tasks(ctx)
}

// Task возвращает задачу по идентификатору.
func (s *Service) Task(ctx context.Context, id string) (domain.Task, error) {
	return s.store.Task(ctx, id)
}

// CreateTask создаёт задачу, дополняя незаполненные поля.
func (s *Service) CreateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		return domain.Task{}, fmt.Errorf("название задачи пустое: %w", ErrInvalid)
	}
	t.Project = strings.TrimSpace(t.Project)
	t.Author = strings.TrimSpace(t.Author)
	t.Assignee = strings.TrimSpace(t.Assignee)

	if t.ID == "" {
		t.ID = newID("t")
	}
	if t.OpenedAt.IsZero() {
		t.OpenedAt = s.now()
	}

	if err := s.store.CreateTask(ctx, t); err != nil {
		return domain.Task{}, err
	}
	s.log.Info("задача создана", "задача", t.ID, "название", t.Title)
	return t, nil
}

// PortalChats отдаёт чаты портала, пригодные для закрепления за задачей.
//
// Без настроенного портала — пустой список и никакой ошибки. Различать «портала
// нет» и «портал не ответил» должен транспорт: для человека это два разных
// сообщения, а закрепление чата не обязательно ни в одном из случаев.
func (s *Service) PortalChats(ctx context.Context, limit int) ([]bitrix.Chat, error) {
	if !s.PortalConfigured() {
		return nil, nil
	}
	all, err := s.portal.Chats(ctx, limit)
	if err != nil {
		return nil, err
	}

	out := make([]bitrix.Chat, 0, len(all))
	for _, c := range all {
		if c.Selectable() {
			out = append(out, c)
		}
	}
	return out, nil
}

// PortalTasks отдаёт задачи портала, пригодные для закрепления.
//
// Метод есть, потому что чаты задач в PortalChats не попадают вообще:
// im.recent.list показывает недавние чаты владельца вебхука, а чат задачи в эту
// ленту не входит. Найти его можно только через карточку задачи.
//
// Без настроенного портала — пустой список и никакой ошибки, ровно как в
// PortalChats: различать «портала нет» и «портал не ответил» должен транспорт.
func (s *Service) PortalTasks(ctx context.Context, query string, limit int) ([]bitrix.Task, error) {
	if !s.PortalConfigured() {
		return nil, nil
	}
	return s.portal.Tasks(ctx, query, limit)
}

// PortalTask отдаёт задачу портала, у которой есть чат, пригодный к
// закреплению.
//
// Отдельно от закрепления намеренно. Задача реестра создаётся и закрепляется
// разными вызовами, а спросить у портала нужно раньше их обоих: узнав об
// отсутствии чата после CreateTask, транспорт остался бы с созданной задачей и
// с ошибкой в ответе, и повторная отправка формы завела бы дубль.
//
// Чат берётся у портала здесь, а не приходит из браузера, по той же причине, по
// которой оттуда не приходит подпись: браузер называет задачу, а какой у неё
// чат — знает портал. Присланному номеру чата пришлось бы верить на слово.
func (s *Service) PortalTask(ctx context.Context, portalTaskID string) (bitrix.Task, error) {
	portalTaskID = strings.TrimSpace(portalTaskID)
	if portalTaskID == "" {
		return bitrix.Task{}, fmt.Errorf("не указана задача портала: %w", ErrInvalid)
	}
	if !s.PortalConfigured() {
		return bitrix.Task{}, fmt.Errorf("Bitrix24 не настроен: %w", ErrInvalid)
	}

	portal, err := s.portal.TaskChat(ctx, portalTaskID)
	if err != nil {
		// «Нет такой задачи» — про присланный номер, а не про портал: он ответил
		// исправно. Без этой ветки транспорт назвал бы опечатку сбоем шлюза.
		if errors.Is(err, bitrix.ErrTaskNotFound) {
			return bitrix.Task{}, fmt.Errorf("%w: %w", err, ErrInvalid)
		}
		return bitrix.Task{}, err
	}
	// У задачи портала чат заводится не при постановке, а при первом событии по
	// ней. Отказ здесь честнее пустой связи: закреплять нечего, и подтяжке потом
	// было бы нечего читать.
	if portal.DialogID() == "" {
		return bitrix.Task{}, fmt.Errorf("у задачи %s нет чата: %w", portalTaskID, ErrInvalid)
	}
	return portal, nil
}

// PinChat закрепляет за задачей чат внешней системы.
//
// Связью, а не списком строк: полей выбора уже четыре, и позиционные аргументы
// на четвёртом перестают читаться. Курсор и дата закрепления из аргумента не
// берутся — их выставляет либо сам метод, либо подтяжка.
//
// Курсор синхронизации здесь не выставляется и не сдвигается: закрепление — это
// выбор человека, а курсор принадлежит подтяжке. Повторный выбор того же чата
// уточняет подпись и не заставляет перечитывать переписку заново.
func (s *Service) PinChat(ctx context.Context, in domain.ChatLink) (domain.ChatLink, error) {
	link := domain.ChatLink{
		TaskID:         strings.TrimSpace(in.TaskID),
		System:         strings.TrimSpace(in.System),
		DialogID:       strings.TrimSpace(in.DialogID),
		Title:          strings.TrimSpace(in.Title),
		ExternalTaskID: strings.TrimSpace(in.ExternalTaskID),
		LinkedAt:       s.now(),
	}
	if link.System == "" {
		link.System = domain.SystemBitrix
	}
	if link.Zero() {
		return domain.ChatLink{}, fmt.Errorf("нужны и задача, и чат: %w", ErrInvalid)
	}

	// Подпись приходит из браузера, а у поля есть обещание: в нём нет токенов
	// доступа (см. domain.ChatLink.Title). Держит обещание тот, кто пишет поле, —
	// иначе оно держится только до первого нового вызывающего. Токен в подписи
	// стоит отдельной строки в логе: значит, он где-то отрисовался на экране.
	if safe, hit := bitrix.Redact(link.Title); hit {
		link.Title = safe
		s.log.Warn("в подписи чата был токен доступа",
			"задача", link.TaskID, "чат", link.DialogID)
	}

	// Проверять существование задачи отдельно не нужно: LinkChat возвращает
	// store.ErrNotFound сам, и лишний поход в хранилище только добавил бы гонку.
	if err := s.store.LinkChat(ctx, link); err != nil {
		return domain.ChatLink{}, err
	}
	s.log.Info("чат закреплён",
		"задача", link.TaskID, "система", link.System, "чат", link.DialogID,
		"задача портала", link.ExternalTaskID)
	return link, nil
}

// TaskChats возвращает чаты, закреплённые за задачей, в порядке закрепления.
func (s *Service) TaskChats(ctx context.Context, taskID string) ([]domain.ChatLink, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.ChatLinks(ctx, taskID)
}

// Sources возвращает источники задачи.
func (s *Service) Sources(ctx context.Context, taskID string) ([]domain.Source, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.Sources(ctx, taskID)
}

// AddSource добавляет источник к задаче.
//
// Срез после этого не пересобирается сам: сборка — отдельное решение
// пользователя, потому что она меняет числа в отчёте, который он мог уже
// прочитать.
func (s *Service) AddSource(ctx context.Context, src domain.Source) (domain.Source, error) {
	src.Title = strings.TrimSpace(src.Title)
	src.Author = strings.TrimSpace(src.Author)
	if strings.TrimSpace(src.Body) == "" {
		return domain.Source{}, fmt.Errorf("текст источника пустой: %w", ErrInvalid)
	}
	if !src.Kind.Valid() {
		return domain.Source{}, fmt.Errorf("вид источника %q неизвестен: %w", src.Kind, ErrInvalid)
	}
	if src.Title == "" {
		src.Title = src.Kind.Label()
	}
	if src.ID == "" {
		src.ID = newID("s")
	}
	if src.UploadedAt.IsZero() {
		src.UploadedAt = s.now()
	}

	// OccurredAt намеренно не подставляется из UploadedAt. Неизвестная дата
	// события — это значение: срез должен сказать «когда это было, неизвестно»,
	// а не выдать день загрузки за день разговора.

	if src.ParentID != "" {
		if _, err := s.store.Source(ctx, src.ParentID); err != nil {
			return domain.Source{}, fmt.Errorf("источник-родитель %s: %w", src.ParentID, err)
		}
	}

	if err := s.store.AddSource(ctx, src); err != nil {
		return domain.Source{}, err
	}
	s.log.Info("источник загружен",
		"задача", src.TaskID, "источник", src.ID, "вид", src.Kind, "знаков", src.Size())
	return src, nil
}

// SourceUsage возвращает версии срезов, которые опираются на источник.
//
// Нужно, чтобы загруженный материал не был односторонней записью в журнале: PM
// вправе спросить, куда пошёл кусок переписки и на что он повлиял.
func (s *Service) SourceUsage(ctx context.Context, sourceID string) ([]domain.SliceRef, error) {
	if _, err := s.store.Source(ctx, sourceID); err != nil {
		return nil, err
	}
	return s.store.SlicesUsing(ctx, sourceID)
}

// Slice возвращает последний собранный срез, а если его ещё нет — собирает.
func (s *Service) Slice(ctx context.Context, taskID string) (domain.Slice, error) {
	sl, err := s.store.LatestSlice(ctx, taskID)
	switch {
	case err == nil:
		return sl, nil
	case errors.Is(err, store.ErrNotFound):
		return s.Rebuild(ctx, taskID)
	}
	return domain.Slice{}, err
}

// Rebuild пересобирает срез задачи из её источников.
//
// Порядок важен: факты и схемы сначала ложатся в журнал, и только потом журнал
// читается целиком. Поэтому срез собирается из всей накопленной истории задачи,
// а не только из последнего разбора.
func (s *Service) Rebuild(ctx context.Context, taskID string) (domain.Slice, error) {
	now := s.now()

	task, err := s.store.Task(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}
	sources, err := s.store.Sources(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}

	// Пересборка по неизменившемуся материалу новой версии не даёт.
	//
	// Это закрывает обе половины давнего дефекта разом. Каждое нажатие кнопки
	// добавляло в журнал фактов те же тридцать две строки заново и заводило
	// версию, ничем не отличающуюся от предыдущей: история версий заполнялась
	// пустыми различиями, а журнал фактов рос от кнопки, а не от событий.
	//
	// С подключённой моделью появился ещё один довод, которого раньше не было:
	// разбор стоит денег. Повтор по тому же материалу — это оплаченный вызов,
	// после которого в реестре ничего не меняется.
	//
	// Признак изменения — материал, а не результат. Сравнивать сам срез с
	// прошлым бесполезно: модель на одном и том же материале отвечает каждый раз
	// немного иначе, и «различие» находилось бы всегда.
	latest, err := s.store.LatestSlice(ctx, taskID)
	switch {
	case err == nil && !materialChanged(sources, latest):
		s.log.Info("пересборка пропущена: материал не менялся",
			"задача", taskID, "версия", latest.Version, "источников", len(sources))
		return latest, ErrNoChanges
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return domain.Slice{}, err
	}

	// Задачу без материала разбирать не зовём. Разбирать нечего, вызов модели
	// платный, а ответ на него был бы выдумкой от первого до последнего поля.
	//
	// Срез при этом собирается, а не отменяется: у задачи без источников он
	// состоит из одних пробелов, и это верное описание положения дел. Отказ
	// вместо среза означал бы, что только что созданную задачу нельзя открыть —
	// а это первое, что человек делает после создания.
	var out analyst.Output
	if len(sources) > 0 {
		out, err = s.analyst.Extract(ctx, analyst.Input{Task: task, Sources: sources, Now: now})
		if err != nil {
			return domain.Slice{}, fmt.Errorf("разбор источников: %w", err)
		}
	} else {
		s.log.Info("разбор пропущен: у задачи нет источников", "задача", taskID)
	}

	if err := s.store.AddFacts(ctx, out.Facts); err != nil {
		return domain.Slice{}, err
	}
	for _, p := range out.Processes {
		p.TaskID = taskID
		if err := s.store.PutProcess(ctx, p); err != nil {
			return domain.Slice{}, err
		}
	}

	facts, err := s.store.Facts(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}
	version, err := s.store.NextSliceVersion(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}

	sl := assemble(task, sources, facts, out, version, now, s.analyst.Name())
	if err := s.store.SaveSlice(ctx, sl); err != nil {
		return domain.Slice{}, err
	}

	s.log.Info("срез собран",
		"задача", taskID,
		"версия", sl.Version,
		"источников", len(sources),
		"фактов", len(facts),
		"готовность", sl.Status.Readiness.Text,
		"пробелов", sl.Gaps())
	return sl, nil
}

// Processes возвращает схемы процессов задачи.
func (s *Service) Processes(ctx context.Context, taskID string) ([]domain.Process, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.Processes(ctx, taskID)
}

// Facts возвращает журнал фактов задачи.
func (s *Service) Facts(ctx context.Context, taskID string) ([]domain.Fact, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.Facts(ctx, taskID)
}

// Source возвращает источник по идентификатору.
func (s *Service) Source(ctx context.Context, id string) (domain.Source, error) {
	return s.store.Source(ctx, id)
}

// assemble складывает срез из разбора и журнала фактов.
//
// Функция чистая: ни хранилища, ни времени внутри — только то, что передали.
// Поэтому её проверяет обычный табличный тест, без поднятия сервиса.
//
// Правило распределения ответственности, чтобы оно не расползлось:
//   - структуры (этапы, блокеры, риски, вопросы) приходят полями analyst.Output;
//   - одиночные значения, у которых есть основа в карточке (паспорт, следующая
//     точка контроля), берутся из фактов и перебивают карточку;
//   - числа не приходят ниоткуда — они считаются здесь.
func assemble(
	task domain.Task,
	sources []domain.Source,
	facts []domain.Fact,
	out analyst.Output,
	version int,
	now time.Time,
	analystName string,
) domain.Slice {
	byField := make(map[string]domain.Value, len(facts))
	for _, f := range facts {
		byField[f.Field] = f.Value // поздний факт перекрывает ранний
	}

	sl := domain.Slice{
		TaskID:   task.ID,
		Version:  version,
		BuiltAt:  now,
		Analyst:  analystName,
		Passport: passport(task, byField),
		Goal: domain.Goal{
			AsStated:   out.GoalAsStated,
			Clarified:  out.GoalClarified,
			Criteria:   out.Criteria,
			OutOfScope: out.OutOfScope,
		},
		Status: domain.Status{
			Stage:      out.Stage,
			Readiness:  readiness(out.Milestones),
			Milestones: sortMilestones(out.Milestones),
			Done:       out.Done,
			Left:       out.Left,
		},
		Blockers: sortBlockers(out.Blockers),
		Risks:    sortRisks(out.Risks),
		PMActions: domain.PMActions{
			Needed:    out.PMActions,
			NextCheck: value(byField, "pmActions.nextCheck", domain.Missing("следующая точка контроля не назначена")),
		},
		Questions: out.Questions,
		Artifacts: out.Artifacts,
	}

	sl.Passport.Shifts = out.Shifts

	// Источники версии — только те, на которые срез действительно ссылается.
	// Схемы процессов живут рядом со срезом, но опираются на тот же материал,
	// поэтому их основания учитываются здесь же: иначе аудит, из которого
	// нарисована схема «как есть», не попал бы в список ни одной версии.
	used := sl.UsedSources()
	seen := make(map[string]bool, len(used))
	for _, id := range used {
		seen[id] = true
	}
	for _, p := range out.Processes {
		if id := p.Evidence.SourceID; id != "" && !seen[id] {
			seen[id] = true
			used = append(used, id)
		}
	}
	sl.SourceIDs = used
	sl.Considered = len(sources)
	return sl
}

// passport кладёт факты поверх полей карточки.
//
// Карточка — не источник: она говорит, что в неё вписали, а не что происходит.
// Ради этого расхождения журнал фактов и нужен: постановщик в карточке один, а
// требования ставит другой, и срез должен показать второго, объяснив почему.
func passport(task domain.Task, byField map[string]domain.Value) domain.Passport {
	return domain.Passport{
		Title:    value(byField, "passport.title", card(task.Title)),
		Author:   value(byField, "passport.author", card(task.Author)),
		Assignee: value(byField, "passport.assignee", card(task.Assignee)),
		OpenedAt: value(byField, "passport.openedAt", cardDate(task.OpenedAt)),
		Deadline: value(byField, "passport.deadline", cardDate(task.Deadline)),
	}
}

// value достаёт факт по имени поля, а если его нет — отдаёт основу.
func value(byField map[string]domain.Value, field string, base domain.Value) domain.Value {
	if v, ok := byField[field]; ok && v.Known() {
		return v
	}
	return base
}

// card — значение из поля карточки задачи.
func card(text string) domain.Value {
	if strings.TrimSpace(text) == "" {
		return domain.Missing("поле карточки не заполнено")
	}
	return domain.Value{Text: text, Origin: domain.OriginQuoted, Note: "поле карточки задачи"}
}

// cardDate — дата из поля карточки задачи.
func cardDate(t time.Time) domain.Value {
	if t.IsZero() {
		return domain.Missing("поле карточки не заполнено")
	}
	return card(domain.FormatDate(t))
}

// readiness считает готовность по этапам плана.
//
// Считает всегда здесь и никогда не берёт у аналитика. Модель может ошибиться в
// арифметике незаметно, а спор с заказчиком идёт как раз о проценте: он должен
// пересчитываться из тех же этапов кем угодно и сходиться.
func readiness(ms []domain.Milestone) domain.Value {
	if len(ms) == 0 {
		return domain.Missing("плана нет: этапы в источниках не найдены")
	}
	share, done := domain.Readiness(ms)
	return domain.Computed(ru.Percent(share), fmt.Sprintf("%s из %s плана закрыто",
		ru.Fixed(done, 1), ru.CountOf(len(ms), "этапа", "этапов")))
}

// sortMilestones выстраивает этапы по сроку: план читают как календарь.
func sortMilestones(ms []domain.Milestone) []domain.Milestone {
	out := make([]domain.Milestone, len(ms))
	copy(out, ms)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Due.Before(out[j].Due) })
	return out
}

// sortBlockers ставит вперёд самый старый блокер: он и есть главная новость.
func sortBlockers(bs []domain.Blocker) []domain.Blocker {
	out := make([]domain.Blocker, len(bs))
	copy(out, bs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// sortRisks ставит вперёд риск с большим влиянием на срок.
func sortRisks(rs []domain.Risk) []domain.Risk {
	out := make([]domain.Risk, len(rs))
	copy(out, rs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].DaysImpact > out[j].DaysImpact })
	return out
}

// SliceVersions возвращает историю версий среза задачи, свежие первыми.
func (s *Service) SliceVersions(ctx context.Context, taskID string) ([]domain.SliceRef, error) {
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return nil, err
	}
	return s.store.SliceVersions(ctx, taskID)
}

// SliceVersion возвращает конкретную версию среза.
func (s *Service) SliceVersion(ctx context.Context, taskID string, version int) (domain.Slice, error) {
	if version <= 0 {
		return domain.Slice{}, fmt.Errorf("версия %d: %w", version, ErrInvalid)
	}
	return s.store.SliceVersion(ctx, taskID, version)
}

// CompareVersions отвечает, что изменилось между двумя версиями среза.
//
// Порядок аргументов не важен: версии всегда сравниваются от старой к новой.
// «Было → стало» с перепутанными местами читалось бы как откат, которого не
// было, и человек сделал бы обратный вывод из верных данных.
func (s *Service) CompareVersions(ctx context.Context, taskID string, a, b int) (domain.Slice, domain.Slice, []domain.SliceChange, error) {
	if a == b {
		return domain.Slice{}, domain.Slice{}, nil,
			fmt.Errorf("сравнивать версию %d саму с собой незачем: %w", a, ErrInvalid)
	}
	if a > b {
		a, b = b, a
	}

	before, err := s.SliceVersion(ctx, taskID, a)
	if err != nil {
		return domain.Slice{}, domain.Slice{}, nil, err
	}
	after, err := s.SliceVersion(ctx, taskID, b)
	if err != nil {
		return domain.Slice{}, domain.Slice{}, nil, err
	}
	return before, after, domain.Compare(before, after), nil
}

// AddIncident записывает случай в журнал.
//
// Реестр здесь ничего не оценивает и зарплату не считает. Он проверяет то, что
// можно проверить машиной: заполнены ли дата, задача и описание, назван ли блок
// KPI. Оценку в процентах ставит человек — она про меру, а мера машине не
// видна.
func (s *Service) AddIncident(ctx context.Context, in domain.Incident) (domain.Incident, error) {
	in.Employee = strings.TrimSpace(in.Employee)
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.Text = strings.TrimSpace(in.Text)

	switch {
	case in.Employee == "":
		return domain.Incident{}, fmt.Errorf("не указан сотрудник: %w", ErrInvalid)
	case in.TaskID == "":
		// «Дата, задача, что произошло» — требование самой системы оплаты.
		// Случай без задачи специалисту нечем показать.
		return domain.Incident{}, fmt.Errorf("не указана задача: %w", ErrInvalid)
	case in.Text == "":
		return domain.Incident{}, fmt.Errorf("не описано, что произошло: %w", ErrInvalid)
	case !in.Block.Valid():
		// Ровно один блок из закрытого списка. Правило «не применяем двойное
		// наказание» проверяемо только пока блок один и известен.
		return domain.Incident{}, fmt.Errorf("не указан блок KPI: %w", ErrInvalid)
	}

	if in.ID == "" {
		in.ID = newID("i")
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = s.now()
	}
	// Дата события не подставляется из даты внесения: случай могли
	// зафиксировать через неделю, а относится он к своему дню. Незаполненная
	// дата — пробел, который видно, а подставленная — выдуманное число.
	if in.At.IsZero() {
		return domain.Incident{}, fmt.Errorf("не указана дата случая: %w", ErrInvalid)
	}

	if err := s.store.AddIncident(ctx, in); err != nil {
		return domain.Incident{}, err
	}
	s.log.Info("случай записан",
		"случай", in.ID, "сотрудник", in.Employee, "задача", in.TaskID, "блок", in.Block)
	return in, nil
}

// Incidents возвращает журнал случаев, свежие первыми. Пустой taskID — по всем
// задачам.
func (s *Service) Incidents(ctx context.Context, taskID string) ([]domain.Incident, error) {
	if taskID != "" {
		if _, err := s.store.Task(ctx, taskID); err != nil {
			return nil, err
		}
	}
	return s.store.Incidents(ctx, taskID)
}
