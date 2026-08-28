package domain

import "time"

// Stage — этап задачи из шаблона среза.
type Stage string

const (
	StageNotStarted Stage = "not_started"
	StageInProgress Stage = "in_progress"
	StageReview     Stage = "review"
	StageBlocked    Stage = "blocked"
	StageDone       Stage = "done"
)

// Label возвращает подпись этапа для интерфейса.
func (s Stage) Label() string {
	switch s {
	case StageNotStarted:
		return "не начато"
	case StageInProgress:
		return "в работе"
	case StageReview:
		return "на проверке"
	case StageBlocked:
		return "заблокировано"
	case StageDone:
		return "готово"
	}
	return string(s)
}

// Task — задача, по которой собирается срез.
//
// Здесь только то, что задаёт сам PM при постановке. Всё остальное — статус,
// готовность, блокеры — не поля задачи, а результат разбора источников, и
// живёт в срезе.
type Task struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
	Title    string    `json:"title"`
	Author   string    `json:"author"`   // постановщик
	Assignee string    `json:"assignee"` // ответственный специалист
	OpenedAt time.Time `json:"openedAt"`
	Deadline time.Time `json:"deadline"`
	Budget   int       `json:"budget,omitempty"` // согласованная смета, ₽
}

// DaysLeft — сколько дней осталось до срока на дату now. Отрицательное
// значение означает просрочку.
func (t Task) DaysLeft(now time.Time) int {
	if t.Deadline.IsZero() {
		return 0
	}
	return DaysBetween(now, t.Deadline)
}

// SourceKind — вид загруженного материала.
//
// Вид отвечает на вопрос «что это за материал», а не «откуда он взялся».
// Сообщение из чата Bitrix и вставленное руками письмо — одна и та же
// переписка; откуда материал попал в реестр, говорит Source.External. Если
// смешать эти два вопроса в одном поле, рядом с видом «переписка» появится вид
// «переписка из Bitrix», и на первой же интеграции они разойдутся.
type SourceKind string

const (
	KindCorrespondence SourceKind = "correspondence" // лента задачи, чат, письма
	KindSpec           SourceKind = "spec"           // техническое задание
	KindAudit          SourceKind = "audit"          // аудит, обследование
	KindBridge         SourceKind = "bridge"         // сводка с капитанского мостика
	KindDoc            SourceKind = "doc"            // прочий документ
	KindNote           SourceKind = "note"           // пояснение от PM
)

// Label возвращает подпись вида источника для интерфейса.
func (k SourceKind) Label() string {
	switch k {
	case KindCorrespondence:
		return "переписка"
	case KindSpec:
		return "ТЗ"
	case KindAudit:
		return "аудит"
	case KindBridge:
		return "мостик"
	case KindDoc:
		return "документ"
	case KindNote:
		return "пояснение"
	}
	return string(k)
}

// Valid сообщает, известен ли такой вид источника.
func (k SourceKind) Valid() bool {
	switch k {
	case KindCorrespondence, KindSpec, KindAudit, KindBridge, KindDoc, KindNote:
		return true
	}
	return false
}

// SystemBitrix — имя портала в ExternalRef.System и ChatLink.System.
//
// Константа, а не строка по месту: имя системы участвует и в ключе оригинала, и
// в ключе закреплённого чата. Опечатка в одном из двух мест развела бы подтяжку
// с реестром молча — сообщения приходили бы, а дубль опознать было бы нечем.
const SystemBitrix = "bitrix24"

// ExternalRef — адрес материала в системе, из которой он пришёл.
//
// Нужен для двух вещей. Первая: повторная подтяжка того же чата не должна
// заводить второй экземпляр сообщения, а опознать дубль можно только по
// идентификатору оригинала. Вторая: из среза нужно уметь вернуться к
// первоисточнику — ссылка на сообщение в Bitrix убеждает заказчика лучше любой
// цитаты.
//
// Пустая структура означает, что материал вставили руками и внешнего оригинала
// у него нет.
type ExternalRef struct {
	System    string `json:"system,omitempty"`    // SystemBitrix и подобные
	ChatID    string `json:"chatId,omitempty"`    // диалог, откуда взято сообщение
	MessageID string `json:"messageId,omitempty"` // идентификатор сообщения
	URL       string `json:"url,omitempty"`       // ссылка на оригинал, если есть
}

// Zero сообщает, что у материала нет внешнего оригинала.
func (r ExternalRef) Zero() bool {
	return r.System == "" && r.ChatID == "" && r.MessageID == ""
}

// Key — ключ, по которому источник опознаётся при повторной подтяжке. Пустой
// ключ означает, что искать дубль не по чему, и хранилище примет запись как
// новую.
func (r ExternalRef) Key() string {
	if r.System == "" || r.MessageID == "" {
		return ""
	}
	return r.System + ":" + r.ChatID + ":" + r.MessageID
}

// Source — единица входа: материал, по которому собирается срез.
//
// Источники только добавляются. Загруженный материал не правят и не удаляют,
// иначе ссылки из среза начнут врать — а ссылка на источник здесь и есть
// единственное основание доверять срезу.
type Source struct {
	ID string `json:"id"`

	// TaskID — задача, о которой говорит материал. Пусто означает общий
	// источник: сводка с капитанского мостика говорит сразу о нескольких
	// задачах, и привязать её к одной значит потерять остальные.
	TaskID string `json:"taskId,omitempty"`

	// ParentID — источник, из которого выделен этот фрагмент.
	//
	// Здесь заложен шов для будущего разбора мостика. Сегодня PM вырезает из
	// сводки нужный кусок сам, и фрагмент получает ParentID общего источника.
	// Когда разбор станет автоматическим, он будет складывать такие же записи —
	// остальной код не заметит разницы.
	ParentID string `json:"parentId,omitempty"`

	Kind  SourceKind `json:"kind"`
	Title string     `json:"title"`
	Body  string     `json:"body"`

	// Author — кто написал материал, если известно: автор сообщения в чате,
	// PM у сводки с мостика. Пусто — автора установить не удалось; это
	// значение, а не повод подставить «неизвестно».
	Author string `json:"author,omitempty"`

	// OccurredAt — дата события в материале: когда написано сообщение, когда
	// прошла планёрка. UploadedAt — когда материал попал в реестр.
	//
	// Различать обязательно: аудит, написанный в июне и загруженный в августе,
	// говорит о июне. Нулевой OccurredAt означает, что дату события назвать
	// нечем, — и тогда её нельзя выдавать за дату загрузки.
	OccurredAt time.Time `json:"occurredAt,omitempty"`
	UploadedAt time.Time `json:"uploadedAt"`

	// External — адрес оригинала в системе-источнике. Пусто у материала,
	// вставленного руками.
	External ExternalRef `json:"external,omitempty"`
}

// Size — объём материала в символах.
func (s Source) Size() int { return len([]rune(s.Body)) }

// Shared сообщает, что источник не привязан к одной задаче.
func (s Source) Shared() bool { return s.TaskID == "" }

// Automatic сообщает, что материал подтянут из внешней системы, а не вставлен
// руками. Читателю среза это важно: у автоматической выгрузки нет отбора,
// который делает человек.
func (s Source) Automatic() bool { return !s.External.Zero() }

// At — дата, по которой источник встаёт в хронологию: дата события, а если она
// неизвестна — дата загрузки. Отдельный метод, чтобы это решение было принято
// один раз, а не по-своему в каждом месте, где источники надо упорядочить.
func (s Source) At() time.Time {
	if !s.OccurredAt.IsZero() {
		return s.OccurredAt
	}
	return s.UploadedAt
}
