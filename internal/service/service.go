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
	"sync"
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

	// owner — имя того, от кого выдан вебхук. Спрашивается у портала один раз
	// за запуск: сменить владельца вебхука на ходу нельзя, а список чатов
	// открывают часто, и лишний вызов на каждое открытие ни к чему.
	ownerMu  sync.Mutex
	owner    string
	ownerAsk bool
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

// DeleteTask убирает задачу со всем, что к ней относится.
//
// Единственное место, где реестр расстаётся с материалом. Правило «только
// добавление» охраняет выводы: срез обязан объясняться тем, из чего собран, и
// правка задним числом переписала бы уже показанную заказчику версию. Здесь
// выводов не остаётся вовсе — уходит вся задача, — и охранять нечего.
//
// Случаи журнала инцидентов остаются: случай относится к человеку и дню, а
// задача в нём — место, где это произошло. Уйди они вместе с задачей, удаление
// заведённой по ошибке задачи стирало бы основание для разговора о KPI.
func (s *Service) DeleteTask(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("не указана задача: %w", ErrInvalid)
	}

	// Название читаем до удаления: после него сказать в логе, что именно ушло,
	// будет нечем, а это ровно та запись, которую ищут, когда задача пропала.
	t, err := s.store.Task(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteTask(ctx, id); err != nil {
		return err
	}
	s.log.Info("задача удалена", "задача", id, "название", t.Title)
	return nil
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

// PortalOwner сообщает, от чьего имени выдан вебхук.
//
// Нужен ради одной надписи над списком чатов, и надпись эта важнее, чем
// кажется. Портал показывает реестру переписку одного человека — того, кто
// выдал вебхук. Чаты коллег в неё не попадают, и прочитать их по номеру тоже
// нельзя: портал отвечает отказом в доступе. Пока не сказано, чьи это чаты,
// отсутствие своей переписки в списке выглядит поломкой реестра, а не
// настройкой портала.
//
// Ошибка не возвращается: подпись — украшение списка, и если портал имени не
// дал, список должен открыться без него.
func (s *Service) PortalOwner(ctx context.Context) string {
	if !s.PortalConfigured() {
		return ""
	}

	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	if s.ownerAsk {
		return s.owner
	}

	u, err := s.portal.Me(ctx)
	if err != nil {
		// Не запоминаем неудачу: портал мог не ответить разово, а следующее
		// открытие списка спросит снова.
		s.log.Warn("не удалось узнать владельца вебхука", "ошибка", err)
		return ""
	}
	s.owner, s.ownerAsk = u.Name, true
	return s.owner
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
		Kind:           in.Kind,
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

// PinPortalChat закрепляет за задачей чат портала, названный человеком.
//
// Отдельно от PinChat, потому что подпись и род чата берутся у портала, а не с
// формы. Браузер называет чат номером — какой это чат и как он подписан, знает
// портал; присланному названию пришлось бы верить на слово, а оно попадает в
// срез и в список источников.
//
// Ради этого метода всё и затевалось: переписку с клиентом ведут в
// контакт-центре, и до сих пор прикрепить её к задаче было нечем — чат
// закреплялся только один и только при создании задачи, из карточки портала.
func (s *Service) PinPortalChat(ctx context.Context, taskID, dialogID string) (domain.ChatLink, error) {
	taskID = strings.TrimSpace(taskID)
	dialogID = strings.TrimSpace(dialogID)

	switch {
	case taskID == "":
		return domain.ChatLink{}, fmt.Errorf("не указана задача: %w", ErrInvalid)
	case dialogID == "":
		return domain.ChatLink{}, fmt.Errorf("не указан чат: %w", ErrInvalid)
	case !s.PortalConfigured():
		return domain.ChatLink{}, fmt.Errorf("Bitrix24 не настроен: %w", ErrInvalid)
	}

	chat, err := s.portal.Chat(ctx, dialogID)
	if err != nil {
		// «Нет такого чата» — про присланный номер, а не про портал: он ответил
		// исправно. Без этой ветки транспорт назвал бы опечатку сбоем шлюза.
		// То же и с отказом в доступе: чат существует, портал ответил исправно,
		// просто ведёт эту переписку кто-то другой. Это разбирается в портале, и
		// сказать об этом надо словами, а не кодом 502.
		if errors.Is(err, bitrix.ErrChatNotFound) || errors.Is(err, bitrix.ErrChatForbidden) {
			return domain.ChatLink{}, fmt.Errorf("%w: %w", err, ErrInvalid)
		}
		return domain.ChatLink{}, err
	}

	return s.PinChat(ctx, domain.ChatLink{
		TaskID:   taskID,
		System:   domain.SystemBitrix,
		DialogID: chat.DialogID,
		Title:    chat.Title,
		Kind:     chatKind(chat),
	})
}

// chatKind переводит род чата портала в род реестра.
//
// Перевод живёт здесь, а не в пакете bitrix: тот говорит на языке портала и о
// домене реестра не знает. И не в домене: он не знает про портал.
func chatKind(c bitrix.Chat) domain.ChatKind {
	switch c.Kinded() {
	case bitrix.HintLines:
		return domain.ChatLines
	case bitrix.HintTask:
		return domain.ChatTask
	case bitrix.HintGroup:
		return domain.ChatGroup
	}
	return domain.ChatPrivate
}

// UnpinChat снимает чат с задачи.
//
// Перенесённые сообщения и заведённые из них источники остаются на месте:
// снятие связи означает «больше отсюда не читаем», а не «этого не было». Срез,
// собранный на этой переписке, обязан продолжать объясняться ею.
func (s *Service) UnpinChat(ctx context.Context, taskID, dialogID string) error {
	taskID = strings.TrimSpace(taskID)
	dialogID = strings.TrimSpace(dialogID)
	if taskID == "" || dialogID == "" {
		return fmt.Errorf("нужны и задача, и чат: %w", ErrInvalid)
	}

	if err := s.store.UnlinkChat(ctx, taskID, domain.SystemBitrix, dialogID); err != nil {
		return err
	}
	s.log.Info("чат снят с задачи", "задача", taskID, "чат", dialogID)
	return nil
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

// BlankSlice собирает срез задачи без разбора: одна карточка и сплошные
// пробелы.
//
// Нужен там, где разбор не удался. Открытие задачи запускает первую сборку, и,
// если модель не ответила, страница падала целиком: задачу нельзя было ни
// посмотреть, ни поправить, ни удалить — она запиралась чужим сбоем. А ведь
// именно тогда до неё и надо добраться: посмотреть источники, снять чат,
// удалить заведённую по ошибке.
//
// Версия нулевая, и в хранилище такой срез не пишется: он не сборка, а способ
// показать страницу. Записав его, мы завели бы в истории версию, которой не
// было.
func (s *Service) BlankSlice(ctx context.Context, taskID string) (domain.Slice, error) {
	task, err := s.store.Task(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}
	sources, err := s.store.Sources(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}
	return assemble(task, sources, nil, analyst.Output{}, 0, s.now(), "разбора не было"), nil
}

// Rebuild пересобирает срез задачи из её источников.
//
// Порядок важен: факты и схемы сначала ложатся в журнал, и только потом журнал
// читается целиком. Поэтому срез собирается из всей накопленной истории задачи,
// а не только из последнего разбора.
func (s *Service) Rebuild(ctx context.Context, taskID string) (domain.Slice, error) {
	return s.rebuild(ctx, taskID, false)
}

// Reanalyse разбирает задачу заново, даже если материал не менялся.
//
// Отдельно от Rebuild, потому что различаются они не поведением, а тем, кто
// просит. Ночная пересборка идёт по всем задачам без спроса, и там пропуск
// неизменившегося — главная защита счёта. Здесь кнопку нажал человек, и у него
// есть причина, о которой реестр знать не может: правила разбора поменялись,
// модель поменялась, прошлый ответ оказался неверным. Отвечать на это «материал
// не менялся» значит отказывать в единственном действии, ради которого кнопка и
// нужна, — и выглядит это как сломанная кнопка.
func (s *Service) Reanalyse(ctx context.Context, taskID string) (domain.Slice, error) {
	return s.rebuild(ctx, taskID, true)
}

func (s *Service) rebuild(ctx context.Context, taskID string, force bool) (domain.Slice, error) {
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
	case err == nil && !force && !materialChanged(sources, latest):
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

	// Правки ложатся поверх разбора, а не наоборот. Тот, кто ведёт задачу, знает
	// положение дел лучше переписки — переписка отстаёт, — и пересборка не
	// вправе стирать сказанное человеком. Иначе он правил бы одно и то же по
	// кругу.
	corrections, err := s.store.Corrections(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}

	sl := assemble(task, sources, facts, out, version, now, s.analyst.Name())
	applyCorrections(&sl, corrections, s.log)
	sl.SourceIDs = sl.UsedSources()

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
		// Поздний факт перекрывает ранний — кроме сказанного человеком: оно
		// старше любого разбора. Без этого исключения первая же пересборка
		// стирала бы правку, и человек правил бы одно и то же по кругу.
		if prev, ok := byField[f.Field]; ok &&
			prev.Origin == domain.OriginStated && f.Value.Origin != domain.OriginStated {
			continue
		}
		byField[f.Field] = f.Value
	}

	sl := domain.Slice{
		TaskID:   task.ID,
		Version:  version,
		BuiltAt:  now,
		Analyst:  analystName,
		Passport: passport(task, byField),
		Goal: domain.Goal{
			// Цель и этап приходят от аналитика, но проходят через факты: иначе
			// правка человека держалась бы ровно до следующего разбора.
			AsStated:   value(byField, "goal.asStated", out.GoalAsStated),
			Clarified:  value(byField, "goal.clarified", out.GoalClarified),
			Criteria:   out.Criteria,
			OutOfScope: out.OutOfScope,
		},
		Status: domain.Status{
			Stage:      value(byField, "status.stage", out.Stage),
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
	share, done, total := domain.Readiness(ms)

	// Подпись обязана объяснять цифру, а не повторять её. При равных этапах
	// объяснение — счёт этапов; при разных счёт этапов ничего не объясняет —
	// «0,9 из 2 этапов» при 45 % и при 90 % выглядело бы одинаково, — и вместо
	// него называется объём работы.
	if domain.Weighted(ms) {
		return domain.Computed(ru.Percent(share), fmt.Sprintf(
			"закрыто %s из %s объёма работ; этапов %d, они разного размера",
			ru.Fixed(done, 1), ru.Fixed(total, 1), len(ms)))
	}
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

// Correct записывает содержимое поля, названное человеком, и собирает с ним
// новую версию среза.
//
// Правка не переписывает версию, а заводит следующую. Причина не в
// осторожности: срез — основание для разговора с заказчиком, и «в прошлый раз
// тут стояло другое» должно оставаться проверяемым. Переписанная задним числом
// версия сделала бы историю версий бесполезной именно там, где она нужна.
//
// Модель при этом не зовётся. Правка — не новый материал, а другое знание о том
// же: разбор ничего не добавит, а стоит денег.
//
// Правка ложится в отдельный журнал и переживает пересборку: assemble
// накладывает её поверх разбора. Без этого правка держалась бы до первой
// пересборки, и человек правил бы одно и то же по кругу.
func (s *Service) Correct(ctx context.Context, taskID, field string, in domain.Edit, author string) (domain.Slice, error) {
	field = strings.TrimSpace(field)
	author = strings.TrimSpace(author)

	f, ok := domain.EditableField(field)
	if !ok {
		return domain.Slice{}, fmt.Errorf("поле %q не правится: %w", field, ErrInvalid)
	}

	latest, err := s.store.LatestSlice(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}

	now := s.now()
	doc, err := domain.BuildCorrection(field, in, editNote(author, now))
	if err != nil {
		return domain.Slice{}, fmt.Errorf("%w: %w", err, ErrInvalid)
	}

	c := domain.Correction{
		ID: newID("c"), TaskID: taskID, Field: field,
		Doc: doc, Author: author, At: now,
	}
	if err := s.store.AddCorrection(ctx, c); err != nil {
		return domain.Slice{}, err
	}

	version, err := s.store.NextSliceVersion(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}

	sl := latest
	if err := sl.Apply(c); err != nil {
		return domain.Slice{}, fmt.Errorf("%w: %w", err, ErrInvalid)
	}
	if f.Kind == domain.EditMilestones {
		recount(&sl)
	}
	if err := s.saveEdited(ctx, &sl, version, now, author); err != nil {
		return domain.Slice{}, err
	}
	s.log.Info("срез поправлен",
		"задача", taskID, "версия", version, "поле", f.Field, "кто", author)
	return sl, nil
}

// EditedFields перечисляет поля задачи, которые правили руками.
//
// Берётся из журнала правок, а не выводится из среза по происхождению значений.
// У критерия, этапа и файла происхождения нет вовсе — их правку по срезу узнать
// нельзя, и «вернуть как было» для них просто не появилось бы.
func (s *Service) EditedFields(ctx context.Context, taskID string) ([]string, error) {
	list, err := s.store.Corrections(ctx, taskID)
	if err != nil {
		return nil, err
	}

	latest := domain.LatestCorrections(list)
	// Порядок — как в списке правимых полей: он совпадает с порядком разделов
	// среза, и перечисление не будет прыгать от обхода карты.
	out := make([]string, 0, len(latest))
	for _, f := range domain.Editable() {
		if _, ok := latest[f.Field]; ok {
			out = append(out, f.Field)
		}
	}
	return out, nil
}

// DropCorrections снимает правки поля и собирает срез заново — уже без них.
//
// Это возврат к тому, что сказал разбор: человек передумал править. Пустое поле
// означает «снять все правки задачи».
//
// Пересборка здесь не нужна и не делается: снятая правка обнажает то, что
// разбор говорил и раньше, и оно лежит в фактах. Звать модель заново значило бы
// платить за ответ, который уже есть.
func (s *Service) DropCorrections(ctx context.Context, taskID, field, author string) (domain.Slice, int, error) {
	field = strings.TrimSpace(field)
	if field != "" {
		if _, ok := domain.EditableField(field); !ok {
			return domain.Slice{}, 0, fmt.Errorf("поле %q не правится: %w", field, ErrInvalid)
		}
	}

	task, err := s.store.Task(ctx, taskID)
	if err != nil {
		return domain.Slice{}, 0, err
	}
	latest, err := s.store.LatestSlice(ctx, taskID)
	if err != nil {
		return domain.Slice{}, 0, err
	}

	dropped, err := s.store.DropCorrections(ctx, taskID, field)
	if err != nil {
		return domain.Slice{}, 0, err
	}
	if dropped == 0 {
		// Снимать было нечего — новую версию заводить не за что.
		return latest, 0, nil
	}

	// Опора — последняя версия, которую собрал разбор. Собирать поверх
	// поправленной нельзя: в ней правка уже применена, и снять её было бы
	// неоткуда — «вернуть как было» вернуло бы то же самое.
	base, err := s.lastBuilt(ctx, taskID, latest)
	if err != nil {
		return domain.Slice{}, 0, err
	}

	// Собираем из фактов заново, но без разбора: разбор уже был, его выводы
	// лежат в журнале фактов, и повторный вызов модели ничего не добавит.
	facts, err := s.store.Facts(ctx, taskID)
	if err != nil {
		return domain.Slice{}, 0, err
	}
	sources, err := s.store.Sources(ctx, taskID)
	if err != nil {
		return domain.Slice{}, 0, err
	}
	left, err := s.store.Corrections(ctx, taskID)
	if err != nil {
		return domain.Slice{}, 0, err
	}
	version, err := s.store.NextSliceVersion(ctx, taskID)
	if err != nil {
		return domain.Slice{}, 0, err
	}

	now := s.now()
	// Разбор берётся из опорной версии: структуры — этапы, блокеры, вопросы —
	// приходят полями analyst.Output, и другого места, где они лежат, нет.
	sl := assemble(task, sources, facts, outputOf(base), version, now, base.Analyst)
	applyCorrections(&sl, left, s.log)

	if err := s.saveEdited(ctx, &sl, version, now, author); err != nil {
		return domain.Slice{}, 0, err
	}
	s.log.Info("правки сняты", "задача", taskID, "поле", field, "снято", dropped)
	return sl, dropped, nil
}

// saveEdited сохраняет срез как версию, собранную человеком.
//
// Указатель, а не значение: метод проставляет номер версии и подпись, и по
// значению они остались бы в копии. Вызывающий вернул бы наружу срез без них —
// в хранилище лежала бы новая версия, а на экране прежняя.
func (s *Service) saveEdited(ctx context.Context, sl *domain.Slice, version int, now time.Time, author string) error {
	sl.Version = version
	sl.BuiltAt = now
	// Разбор остаётся за моделью: правка меняет одно поле из двадцати, и стереть
	// её имя значило бы приписать человеку остальные девятнадцать.
	sl.EditedBy = author
	if author == "" {
		sl.EditedBy = "вручную"
	}
	sl.SourceIDs = sl.UsedSources()
	return s.store.SaveSlice(ctx, *sl)
}

// editNote — подпись под правкой: кто и когда.
func editNote(author string, now time.Time) string {
	if author == "" {
		return "правка от " + domain.FormatDate(now)
	}
	return "правку внёс " + author + " " + domain.FormatDate(now)
}

// applyCorrections кладёт правки на срез, поздние поверх ранних.
//
// Непонятая правка не роняет сборку: она осталась от прежней версии схемы, и
// уронить из-за неё весь срез значило бы сделать задачу неоткрываемой. В журнал
// такое попадает предупреждением — чинить это всё равно человеку.
func applyCorrections(sl *domain.Slice, list []domain.Correction, log *slog.Logger) {
	fixed := domain.LatestCorrections(list)
	for _, c := range fixed {
		if err := sl.Apply(c); err != nil {
			log.Warn("правка не наложилась",
				"задача", sl.TaskID, "поле", c.Field, "причина", err)
		}
	}

	// Готовность пересчитывается после правки этапов, а не остаётся прежней.
	//
	// Иначе поправленный план и процент над ним расходились бы прямо на экране:
	// человек ставит этапам 90 и 60, а в сводке по-прежнему 45 — ровно то
	// расхождение, ради которого готовность вообще считается программой, а не
	// берётся у модели. Правится тут именно план: сам процент в списке правимых
	// полей не значится, и это намеренно — спор с заказчиком идёт о числе, и оно
	// обязано пересчитываться из того, что под ним написано.
	if _, ok := fixed["status.milestones"]; ok {
		recount(sl)
	}
}

// recount пересчитывает то, что зависит от поправленного плана.
//
// Пока это одна готовность, и функция всё равно отдельная: правка накладывается
// в двух местах — при самой правке и при сборке среза с уже накопленными
// правками, — и пересчёт, написанный в одном из них, рано или поздно разошёлся
// бы со вторым.
func recount(sl *domain.Slice) {
	sl.Status.Readiness = readiness(sl.Status.Milestones)
}

// lastBuilt находит последнюю версию, собранную разбором, а не правкой.
//
// Нужна там, где срез пересобирают без модели. В поправленной версии правка уже
// применена, и собрать из неё «как было» нельзя: она сама и есть «как стало».
// Если разбора в истории не осталось, опорой служит последняя версия — тогда
// правка не снимется до конца, и это честнее, чем стереть её вместе со всем
// содержимым раздела.
func (s *Service) lastBuilt(ctx context.Context, taskID string, latest domain.Slice) (domain.Slice, error) {
	refs, err := s.store.SliceVersions(ctx, taskID)
	if err != nil {
		return domain.Slice{}, err
	}
	// Свежие первыми — первая же неправленая и есть искомая.
	for _, ref := range refs {
		sl, err := s.store.SliceVersion(ctx, taskID, ref.Version)
		if err != nil {
			return domain.Slice{}, err
		}
		if !edited(sl) {
			return sl, nil
		}
	}
	return latest, nil
}

// edited отвечает, собрана ли версия правкой.
//
// Кроме EditedBy проверяется и подпись аналитика: у версий, сделанных до
// появления этого поля, правка помечалась именем «правка: кто-то» в Analyst.
// Без второй проверки такая версия сходила бы за собранную разбором, и
// «вернуть как было» возвращало бы к чужой правке, а не к разбору.
func edited(sl domain.Slice) bool {
	return sl.EditedBy != "" || strings.HasPrefix(sl.Analyst, "правка")
}

// outputOf восстанавливает структуры разбора из собранной версии.
//
// Нужен там, где срез пересобирается без вызова модели: этапы, блокеры, риски и
// вопросы приходят полями analyst.Output и в журнале фактов не лежат — факты
// хранят одиночные значения. Прошлая версия — единственное место, где эти
// структуры сохранились.
func outputOf(sl domain.Slice) analyst.Output {
	return analyst.Output{
		GoalAsStated:  sl.Goal.AsStated,
		GoalClarified: sl.Goal.Clarified,
		Criteria:      sl.Goal.Criteria,
		OutOfScope:    sl.Goal.OutOfScope,
		Stage:         sl.Status.Stage,
		Milestones:    sl.Status.Milestones,
		Done:          sl.Status.Done,
		Left:          sl.Status.Left,
		Blockers:      sl.Blockers,
		Risks:         sl.Risks,
		PMActions:     sl.PMActions.Needed,
		Questions:     sl.Questions,
		Artifacts:     sl.Artifacts,
		Shifts:        sl.Passport.Shifts,
	}
}

// DeleteSliceVersion убирает версию среза.
//
// Единственное изъятие в реестре, который иначе только пополняется, и оно
// оговорено: срез — не запись о событии, а собранная картина, и неудачная
// сборка ничего не свидетельствует. Материал остаётся на месте: источники и
// факты не трогаются, и по ним картину собирают заново.
//
// Последнюю версию удалить можно: тогда текущей становится предыдущая. Если
// версий не остаётся вовсе, задача открывается пересборкой — тем же путём, что
// и только что созданная.
func (s *Service) DeleteSliceVersion(ctx context.Context, taskID string, version int) error {
	if version <= 0 {
		return fmt.Errorf("версия %d: %w", version, ErrInvalid)
	}
	if _, err := s.store.Task(ctx, taskID); err != nil {
		return err
	}
	if err := s.store.DeleteSlice(ctx, taskID, version); err != nil {
		return err
	}
	s.log.Info("версия среза удалена", "задача", taskID, "версия", version)
	return nil
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
	if err := s.checkIncident(&in); err != nil {
		return domain.Incident{}, err
	}

	if in.ID == "" {
		in.ID = newID("i")
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = s.now()
	}

	if err := s.store.AddIncident(ctx, in); err != nil {
		return domain.Incident{}, err
	}
	s.log.Info("случай записан",
		"случай", in.ID, "сотрудник", in.Employee, "проект", in.Project, "блок", in.Block)
	return in, nil
}

// checkIncident приводит запись в порядок и проверяет обязательное.
//
// Один набор правил на запись и на правку. Разойдясь, они дали бы журнал, в
// который нельзя внести то, что в нём уже лежит, — или, хуже, наоборот.
func (s *Service) checkIncident(in *domain.Incident) error {
	in.Employee = strings.TrimSpace(in.Employee)
	in.Project = strings.TrimSpace(in.Project)
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.Text = strings.TrimSpace(in.Text)
	in.ManagerNote = strings.TrimSpace(in.ManagerNote)
	in.RecordedBy = strings.TrimSpace(in.RecordedBy)

	// Дата эскалации без самой эскалации — противоречие, и хранить его значило
	// бы показывать в журнале «эскалации не было, эскалировано 5 сентября».
	if !in.Escalated {
		in.EscalatedAt = time.Time{}
	}

	switch {
	case in.Employee == "":
		return fmt.Errorf("не указан сотрудник: %w", ErrInvalid)
	case in.Project == "":
		// «Дата, задача, что произошло» — требование самой системы оплаты.
		// Случай без работы, в которой он произошёл, специалисту нечем показать.
		return fmt.Errorf("не указан проект: %w", ErrInvalid)
	case in.Text == "":
		return fmt.Errorf("не описано, что произошло: %w", ErrInvalid)
	case !in.Block.Valid():
		// Ровно один блок из закрытого списка. Правило «не применяем двойное
		// наказание» проверяемо только пока блок один и известен.
		return fmt.Errorf("не указан блок KPI: %w", ErrInvalid)
	case in.At.IsZero():
		// Дата события не подставляется из даты внесения: случай могли
		// зафиксировать через неделю, а относится он к своему дню.
		// Незаполненная дата — пробел, который видно, а подставленная —
		// выдуманное число.
		return fmt.Errorf("не указана дата случая: %w", ErrInvalid)
	}
	return nil
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

// IncidentsIn возвращает случаи задачи за период, свежие первыми.
//
// Оценку ставят за месяц, и журнал целиком для этого не годится: к третьему
// месяцу в нём будет полсотни записей, из которых к разговору относится
// десяток. Границы включительные по дате события — «за сентябрь» значит с
// первого по тридцатое, а не «по тридцатое ноль часов».
func (s *Service) IncidentsIn(ctx context.Context, taskID string, from, to time.Time) ([]domain.Incident, error) {
	all, err := s.Incidents(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if from.IsZero() && to.IsZero() {
		return all, nil
	}

	out := make([]domain.Incident, 0, len(all))
	for _, in := range all {
		// Случай без даты не попадает ни в один период. Это не потеря: дата у
		// случая обязательна, и запись без неё в журнал не вносилась.
		if in.At.IsZero() {
			continue
		}
		day := in.At.Truncate(24 * time.Hour)
		if !from.IsZero() && day.Before(from.Truncate(24*time.Hour)) {
			continue
		}
		if !to.IsZero() && day.After(to.Truncate(24*time.Hour)) {
			continue
		}
		out = append(out, in)
	}
	return out, nil
}

// UpdateIncident переписывает запись журнала.
//
// Подпись и дата внесения не меняются: их ставил сервер при первой записи, и
// правка не отменяет того, что случай зафиксировал такой-то тогда-то.
func (s *Service) UpdateIncident(ctx context.Context, in domain.Incident) (domain.Incident, error) {
	if strings.TrimSpace(in.ID) == "" {
		return domain.Incident{}, fmt.Errorf("не указан случай: %w", ErrInvalid)
	}
	if err := s.checkIncident(&in); err != nil {
		return domain.Incident{}, err
	}
	if err := s.store.UpdateIncident(ctx, in); err != nil {
		return domain.Incident{}, err
	}
	s.log.Info("случай поправлен", "случай", in.ID, "сотрудник", in.Employee)
	return in, nil
}

// DeleteIncident убирает запись журнала.
//
// Убирают, а не помечают отозванной, потому что журнал — рабочий документ
// руководителя. Внесённая по ошибке запись не должна оставаться в нём вечным
// напоминанием о промахе того, кто её внёс, — а к оценке она всё равно не
// относится.
func (s *Service) DeleteIncident(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("не указан случай: %w", ErrInvalid)
	}
	if err := s.store.DeleteIncident(ctx, id); err != nil {
		return err
	}
	s.log.Info("случай удалён", "случай", id)
	return nil
}
