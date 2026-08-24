package domain

import (
	"sort"
	"strings"
	"time"
)

// Milestone — этап плана работ.
type Milestone struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Due      time.Time `json:"due"`
	Progress float64   `json:"progress"`           // доля выполнения, 0..1
	Evidence []string  `json:"evidence,omitempty"` // чем подтверждается выполнение
}

// Done сообщает, закрыт ли этап полностью.
func (m Milestone) Done() bool { return m.Progress >= 1 }

// OverdueDays — число дней просрочки на дату now. Ноль означает, что срок ещё
// не наступил или этап закрыт. Частично выполненный этап с прошедшим сроком
// считается просроченным: «сделано наполовину» не отменяет пропущенной даты.
func (m Milestone) OverdueDays(now time.Time) int {
	if m.Done() || m.Due.IsZero() {
		return 0
	}
	if d := DaysBetween(m.Due, now); d > 0 {
		return d
	}
	return 0
}

// Readiness — готовность как доля закрытых этапов плана: сумма прогресса,
// делённая на число этапов. Возвращает долю от 0 до 1 и саму сумму.
//
// Считается кодом намеренно. Это первая цифра, которую PM назовёт заказчику,
// и она не должна зависеть от того, что модель напишет в свободной форме.
// Сумма возвращается отдельно, чтобы срез мог показать основание: не просто
// «27 %», а «2,4 из 9 этапов».
func Readiness(ms []Milestone) (share, done float64) {
	if len(ms) == 0 {
		return 0, 0
	}
	for _, m := range ms {
		done += m.Progress
	}
	return done / float64(len(ms)), done
}

// OverdueMilestones возвращает просроченные этапы, от самых старых к свежим.
func OverdueMilestones(ms []Milestone, now time.Time) []Milestone {
	var out []Milestone
	for _, m := range ms {
		if m.OverdueDays(now) > 0 {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Due.Before(out[j].Due) })
	return out
}

// DeadlineShift — зафиксированный перенос срока.
//
// Отдельная сущность, потому что в переписке нет строки «мы перенесли срок».
// Есть только служебные записи об изменении поля, и вывод «срок двигали
// четыре раза, ни один перенос не объяснён» появляется лишь при их
// сопоставлении. Ради таких выводов реестр и нужен.
type DeadlineShift struct {
	At      time.Time `json:"at"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Comment string    `json:"comment,omitempty"` // пусто — перенос не объяснили
}

// Explained сообщает, объяснили ли перенос.
func (s DeadlineShift) Explained() bool { return strings.TrimSpace(s.Comment) != "" }

// Unexplained возвращает число переносов без объяснения.
func Unexplained(shifts []DeadlineShift) int {
	n := 0
	for _, s := range shifts {
		if !s.Explained() {
			n++
		}
	}
	return n
}

// BlockerKind — тип блокера из шаблона среза. Тип важнее описания: он
// определяет, к кому идти, чтобы блокер снять.
type BlockerKind string

const (
	BlockerNoInfo     BlockerKind = "no_info"
	BlockerNoAccess   BlockerKind = "no_access"
	BlockerDependency BlockerKind = "dependency"
	BlockerTechnical  BlockerKind = "technical"
	BlockerNoApproach BlockerKind = "no_approach"
	BlockerNoTime     BlockerKind = "no_time"
)

// Label возвращает подпись типа блокера для интерфейса.
func (k BlockerKind) Label() string {
	switch k {
	case BlockerNoInfo:
		return "нет информации"
	case BlockerNoAccess:
		return "нет доступа"
	case BlockerDependency:
		return "зависит от другого человека или задачи"
	case BlockerTechnical:
		return "технический"
	case BlockerNoApproach:
		return "нет решения по подходу"
	case BlockerNoTime:
		return "нет ресурсов времени"
	}
	return string(k)
}

// Blocker — то, что тормозит задачу.
type Blocker struct {
	ID        string      `json:"id"`
	Summary   string      `json:"summary"`
	Kind      BlockerKind `json:"kind"`
	DependsOn string      `json:"dependsOn,omitempty"` // от кого зависит снятие
	Since     time.Time   `json:"since"`
	Evidence  Value       `json:"evidence"`
}

// AgeDays — сколько дней блокер висит на дату now.
func (b Blocker) AgeDays(now time.Time) int {
	if b.Since.IsZero() {
		return 0
	}
	if d := DaysBetween(b.Since, now); d > 0 {
		return d
	}
	return 0
}

// Risk — то, что может пойти не так.
type Risk struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	DaysImpact int    `json:"daysImpact"`       // влияние на срок, дней
	Spread     string `json:"spread,omitempty"` // на какие задачи и модули влияет
	Evidence   Value  `json:"evidence"`
}

// PMActionKind — что именно требуется от PM. Список закрытый: строка
// «разобраться с задачей» не действие, а её отсутствие.
type PMActionKind string

const (
	ActionApprove  PMActionKind = "approve"
	ActionAccess   PMActionKind = "access"
	ActionConnect  PMActionKind = "connect"
	ActionClarify  PMActionKind = "clarify"
	ActionEscalate PMActionKind = "escalate"
	ActionNone     PMActionKind = "none"
)

// Label возвращает подпись действия для интерфейса.
func (k PMActionKind) Label() string {
	switch k {
	case ActionApprove:
		return "согласовать"
	case ActionAccess:
		return "достать доступ"
	case ActionConnect:
		return "свести с другим специалистом"
	case ActionClarify:
		return "уточнить требования"
	case ActionEscalate:
		return "эскалировать"
	case ActionNone:
		return "ничего"
	}
	return string(k)
}

// PMAction — одно действие, которое должен сделать PM.
type PMAction struct {
	Kind PMActionKind `json:"kind"`
	Text string       `json:"text"`
	Why  string       `json:"why,omitempty"` // на что это разблокирует
}

// Question — вопрос специалисту из шаблона сбора.
type Question struct {
	N       int    `json:"n"` // номер в шаблоне, чтобы вопрос можно было найти
	Text    string `json:"text"`
	Unlocks string `json:"unlocks,omitempty"` // что даст ответ
	Answer  Value  `json:"answer"`
}

// Answered сообщает, закрыт ли вопрос источниками.
func (q Question) Answered() bool { return q.Answer.Known() }

// Artifact — файл, упомянутый в источниках.
//
// Present == false означает, что файл назван, но не приложен. Срез обязан
// говорить об этом прямо: иначе читатель решит, что материал учтён.
type Artifact struct {
	Name      string `json:"name"`
	Bytes     int    `json:"bytes,omitempty"`
	Present   bool   `json:"present"`
	WouldGive string `json:"wouldGive,omitempty"` // что дал бы этот файл
}

// Criterion — критерий приёмки: как заказчик поймёт, что работа сделана.
type Criterion struct {
	N    int    `json:"n"`
	Text string `json:"text"`
	Met  bool   `json:"met"`
	Note string `json:"note,omitempty"`
}

// MetCriteria возвращает число выполненных критериев.
func MetCriteria(cs []Criterion) int {
	n := 0
	for _, c := range cs {
		if c.Met {
			n++
		}
	}
	return n
}
