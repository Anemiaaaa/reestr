// Package claude — разбор источников моделью.
//
// Пакет устроен вокруг одной мысли: ответу модели нельзя верить на слово, и
// проверяет его Go, а не промпт. Промпт объясняет, что нужно; проверка решает,
// что принято. Правило, записанное только в промпте, — это пожелание, и первое
// же выдуманное значение попадёт в срез, который PM покажет заказчику.
//
// Отсюда деление пакета. Answer.go описывает форму ответа, convert.go проверяет
// его и переводит в analyst.Output, отбрасывая всё, что не сошлось.
package schema

import "strings"

// Value — значение с происхождением в том виде, в каком его возвращает модель.
//
// Повторяет domain.Value, но отдельным типом. Разница не в полях, а в доверии:
// domain.Value — уже проверенное значение, а это — заявление модели, которое
// ещё предстоит проверить. Один тип на оба состояния означал бы, что непроверенное
// значение можно случайно положить в срез, и компилятор не возразит.
type Value struct {
	Text string `json:"text"`

	// Origin — «quoted», «derived» или «missing».
	//
	// «computed» здесь нет намеренно, и это не упущение схемы. Проценты, суммы
	// и разницы дат считает Go: цифру, которую PM назовёт заказчику, нужно уметь
	// повторить и проверить, а не получать заново при каждом вызове модели.
	// Ответ с «computed» проверка отбрасывает.
	Origin string `json:"origin"`

	// SourceID — источник, на который опирается значение. Обязателен для
	// «quoted» и «derived».
	SourceID string `json:"sourceId,omitempty"`

	// Quote — дословная выдержка. Обязательна для «quoted» и проверяется на
	// вхождение в текст названного источника.
	Quote string `json:"quote,omitempty"`

	// Note — как получено значение («derived») или что спросить, чтобы его
	// узнать («missing»).
	Note string `json:"note,omitempty"`
}

// Empty сообщает, что поля в ответе не было вовсе.
//
// Отличать пропуск от негодного значения нужно ради читателя лога: пропущенное
// поле — это недоработка промпта или схемы, а негодное значение — попытка
// модели выдать желаемое за источник. Лечатся они по-разному.
func (v Value) Empty() bool {
	return strings.TrimSpace(v.Text) == "" &&
		strings.TrimSpace(v.Origin) == "" &&
		strings.TrimSpace(v.SourceID) == "" &&
		strings.TrimSpace(v.Quote) == "" &&
		strings.TrimSpace(v.Note) == ""
}

// Answer — весь ответ модели.
//
// Даты здесь строками «ГГГГ-ММ-ДД», а не числами дней: аналитик называет даты,
// а сколько до них осталось, считает сервис на дату сборки. Иначе срез, собранный
// вчера, соврал бы сегодня.
type Answer struct {
	Stage         Value   `json:"stage"`
	GoalAsStated  Value   `json:"goalAsStated"`
	GoalClarified Value   `json:"goalClarified"`
	OutOfScope    []Value `json:"outOfScope,omitempty"`
	Done          []Value `json:"done,omitempty"`
	Left          []Value `json:"left,omitempty"`

	Criteria   []criterion `json:"criteria,omitempty"`
	Milestones []milestone `json:"milestones,omitempty"`
	Shifts     []shift     `json:"shifts,omitempty"`
	Blockers   []blocker   `json:"blockers,omitempty"`
	Risks      []risk      `json:"risks,omitempty"`
	Questions  []question  `json:"questions,omitempty"`
	Artifacts  []artifact  `json:"artifacts,omitempty"`
	PMActions  []pmAction  `json:"pmActions,omitempty"`
}

type criterion struct {
	N    int    `json:"n"`
	Text string `json:"text"`
	Met  bool   `json:"met"`
	Note string `json:"note,omitempty"`
}

type milestone struct {
	Title string `json:"title"`

	// Due — «ГГГГ-ММ-ДД» либо пустая строка, если срок этапа не назван. Пустая
	// строка и «дата не названа» — одно и то же; подставлять сюда сегодняшнюю
	// дату нельзя.
	Due string `json:"due,omitempty"`

	// Progress — наблюдение «сделано вполовину», а не оценка готовности задачи:
	// её выведет сервис из этапов и фактов.
	Progress float64  `json:"progress"`
	Evidence []string `json:"evidence,omitempty"`
}

type shift struct {
	At      string `json:"at,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type blocker struct {
	Summary string `json:"summary"`

	// Kind — вид блокера из закрытого списка domain.BlockerKind. Незнакомое
	// значение проверка не выдумывает и не подменяет: блокер отбрасывается.
	Kind      string `json:"kind"`
	DependsOn string `json:"dependsOn,omitempty"`
	Since     string `json:"since,omitempty"`
	Evidence  Value  `json:"evidence"`
}

type risk struct {
	Summary    string `json:"summary"`
	DaysImpact int    `json:"daysImpact,omitempty"`
	Spread     string `json:"spread,omitempty"`
	Evidence   Value  `json:"evidence"`
}

type question struct {
	N       int    `json:"n"`
	Text    string `json:"text"`
	Unlocks string `json:"unlocks,omitempty"`
	Answer  Value  `json:"Answer"`
}

type artifact struct {
	Name string `json:"name"`

	// Present == false означает, что файл назван, но не приложен. Срез обязан
	// говорить об этом прямо, иначе читатель решит, что материал учтён.
	Present   bool   `json:"present"`
	Bytes     int    `json:"bytes,omitempty"`
	WouldGive string `json:"wouldGive,omitempty"`
}

type pmAction struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Why  string `json:"why,omitempty"`
}
