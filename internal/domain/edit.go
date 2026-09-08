package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Правка среза человеком.
//
// Тот, кто ведёт задачу, знает положение дел лучше переписки: переписка
// отстаёт, а модель читает только её. Но правка «поверх» была бы подлогом —
// исправленное значение выглядело бы взятым из источника, хотя в источниках его
// нет. Поэтому у правки своё происхождение, OriginStated, и оно старше любого
// разбора.
//
// Правка хранится не значением, а содержимым поля целиком. Причина в списках:
// адреса «третий пункт» не существует — после пересборки третьим станет другой
// пункт, и правка молча уехала бы на чужую строку. Список человек называет
// целиком, и целиком же он ложится поверх разбора.

// EditKind — из чего состоит поле: из одного значения или из списка, и если из
// списка, то какого. Интерфейс рисует по нему форму, сервер по нему разбирает
// присланное.
type EditKind string

const (
	EditValue      EditKind = "value"
	EditValueList  EditKind = "valueList"
	EditCriteria   EditKind = "criteria"
	EditMilestones EditKind = "milestones"
	EditBlockers   EditKind = "blockers"
	EditRisks      EditKind = "risks"
	EditQuestions  EditKind = "questions"
	EditActions    EditKind = "actions"
	EditArtifacts  EditKind = "artifacts"
	EditShifts     EditKind = "shifts"
)

// EditField — правимое поле среза.
type EditField struct {
	Field string   `json:"field"`
	Label string   `json:"label"`
	Kind  EditKind `json:"kind"`
}

// Editable перечисляет всё, что человек может назвать сам, в порядке разделов
// среза.
//
// Список закрытый, и это главное в нём. Открытый адрес поля означал бы, что в
// срез можно вписать что угодно куда угодно, и «поле» перестало бы значить
// «место, за которое кто-то отвечает».
//
// Готовности здесь нет и не будет. Она не приходит ниоткуда, а считается по
// этапам, и правка превратила бы первую цифру, которую называют заказчику, в
// число, которое ничем не объясняется. Если этапы говорят не то — правят этапы,
// и готовность пересчитается сама.
func Editable() []EditField {
	return []EditField{
		{"passport.title", "Название", EditValue},
		{"passport.author", "Автор постановки", EditValue},
		{"passport.assignee", "Исполнитель", EditValue},
		{"passport.openedAt", "Поставлена", EditValue},
		{"passport.deadline", "Срок", EditValue},
		{"passport.shifts", "Переносы срока", EditShifts},

		{"goal.asStated", "Цель: как поставлено", EditValue},
		{"goal.clarified", "Цель: что имелось в виду", EditValue},
		{"goal.criteria", "Критерии приёмки", EditCriteria},
		{"goal.outOfScope", "Вне задачи", EditValueList},

		{"status.stage", "Этап", EditValue},
		{"status.milestones", "Этапы плана", EditMilestones},
		{"status.done", "Сделано", EditValueList},
		{"status.left", "Осталось", EditValueList},

		{"blockers", "Блокеры", EditBlockers},
		{"risks", "Риски", EditRisks},

		{"pmActions.needed", "Что нужно от PM", EditActions},
		{"pmActions.nextCheck", "Следующая проверка", EditValue},

		{"questions", "Вопросы специалисту", EditQuestions},
		{"artifacts", "Файлы", EditArtifacts},
	}
}

// EditableField находит правимое поле по адресу.
func EditableField(field string) (EditField, bool) {
	for _, f := range Editable() {
		if f.Field == field {
			return f, true
		}
	}
	return EditField{}, false
}

// Correction — содержимое поля, названное человеком.
//
// Правки только добавляются, как и всё остальное в реестре: последняя по полю
// перекрывает предыдущие, а история остаётся. Doc хранит содержимое поля в его
// собственном виде — том же, в каком оно лежит в срезе.
type Correction struct {
	ID     string          `json:"id"`
	TaskID string          `json:"taskId"`
	Field  string          `json:"field"`
	Doc    json.RawMessage `json:"doc"`
	Author string          `json:"author,omitempty"`
	At     time.Time       `json:"at"`
}

// LatestCorrections оставляет по одной правке на поле — последнюю.
func LatestCorrections(list []Correction) map[string]Correction {
	out := make(map[string]Correction, len(list))
	for _, c := range list {
		// Список приходит от старых к свежим, поэтому поздняя просто
		// затирает раннюю.
		out[c.Field] = c
	}
	return out
}

// Apply кладёт правку в срез.
//
// Разбор адреса написан переключателем, а не отражением по тегам JSON.
// Отражение приняло бы любой адрес, какой пришёл снаружи, и список правимых
// полей перестал бы быть списком: он совпал бы со структурой среза целиком.
func (s *Slice) Apply(c Correction) error {
	into := func(target any) error {
		if err := json.Unmarshal(c.Doc, target); err != nil {
			return fmt.Errorf("правка поля %s: %w", c.Field, err)
		}
		return nil
	}

	switch c.Field {
	case "passport.title":
		return into(&s.Passport.Title)
	case "passport.author":
		return into(&s.Passport.Author)
	case "passport.assignee":
		return into(&s.Passport.Assignee)
	case "passport.openedAt":
		return into(&s.Passport.OpenedAt)
	case "passport.deadline":
		return into(&s.Passport.Deadline)
	case "passport.shifts":
		return into(&s.Passport.Shifts)
	case "goal.asStated":
		return into(&s.Goal.AsStated)
	case "goal.clarified":
		return into(&s.Goal.Clarified)
	case "goal.criteria":
		return into(&s.Goal.Criteria)
	case "goal.outOfScope":
		return into(&s.Goal.OutOfScope)
	case "status.stage":
		return into(&s.Status.Stage)
	case "status.milestones":
		return into(&s.Status.Milestones)
	case "status.done":
		return into(&s.Status.Done)
	case "status.left":
		return into(&s.Status.Left)
	case "blockers":
		return into(&s.Blockers)
	case "risks":
		return into(&s.Risks)
	case "pmActions.needed":
		return into(&s.PMActions.Needed)
	case "pmActions.nextCheck":
		return into(&s.PMActions.NextCheck)
	case "questions":
		return into(&s.Questions)
	case "artifacts":
		return into(&s.Artifacts)
	}
	return fmt.Errorf("поле %q не правится", c.Field)
}

// --- то, что присылает форма ---

// Edit — правка в том виде, в каком её присылает интерфейс: плоские строки и
// числа, без происхождений и служебных полей.
//
// Отдельная форма нужна, чтобы браузер не подписывался за источник. Он
// присылает только то, что человек напечатал; происхождение, дату и подпись
// ставит сервер.
type Edit struct {
	Text  string     `json:"text,omitempty"`
	Items []EditItem `json:"items,omitempty"`
}

// EditItem — одна строка правимого списка. Полей больше, чем нужно любому
// одному списку: заполняются те, что у этого списка есть.
type EditItem struct {
	Text    string `json:"text,omitempty"`
	Summary string `json:"summary,omitempty"`
	Note    string `json:"note,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Met     bool   `json:"met,omitempty"`

	Progress  float64 `json:"progress,omitempty"`
	Weight    float64 `json:"weight,omitempty"`
	Due       string  `json:"due,omitempty"`
	DependsOn string  `json:"dependsOn,omitempty"`
	Since     string  `json:"since,omitempty"`
	Days      int     `json:"days,omitempty"`
	Spread    string  `json:"spread,omitempty"`
	Unlocks   string  `json:"unlocks,omitempty"`
	Why       string  `json:"why,omitempty"`
	Answer    string  `json:"answer,omitempty"`
	Present   bool    `json:"present,omitempty"`

	At   string `json:"at,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// BuildCorrection превращает присланное формой в содержимое поля.
//
// Здесь же на каждое значение ставится происхождение: не «из источника», а
// «сказал руководитель», с подписью и датой. Ставит его сервер и только он —
// иначе браузер мог бы подписать выдумку цитатой.
func BuildCorrection(field string, in Edit, note string) (json.RawMessage, error) {
	f, ok := EditableField(field)
	if !ok {
		return nil, fmt.Errorf("поле %q не правится", field)
	}

	said := func(text string) Value { return Stated(strings.TrimSpace(text), note) }

	// Пустой список — законная правка: «блокеров больше нет» это утверждение, а
	// не отсутствие ответа. Пустое одиночное значение — нет: стереть значение
	// правкой нельзя, пробел в срезе означает «в источниках ответа нет», а
	// здесь ответ как раз есть.
	var out any
	switch f.Kind {
	case EditValue:
		if strings.TrimSpace(in.Text) == "" {
			return nil, fmt.Errorf("%s: значение не может быть пустым", f.Label)
		}
		out = said(in.Text)

	case EditValueList:
		list := make([]Value, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Text) == "" {
				continue
			}
			list = append(list, said(it.Text))
		}
		out = list

	case EditCriteria:
		list := make([]Criterion, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Text) == "" {
				continue
			}
			// Номер проставляется заново по порядку: он адрес в разговоре
			// («третий критерий не закрыт»), и дырки в нумерации после удаления
			// строки сделали бы этот адрес враньём.
			list = append(list, Criterion{
				N: len(list) + 1, Text: strings.TrimSpace(it.Text),
				Met: it.Met, Note: strings.TrimSpace(it.Note),
			})
		}
		out = list

	case EditMilestones:
		list := make([]Milestone, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Text) == "" {
				continue
			}
			m := Milestone{
				ID:       fmt.Sprintf("m%d", len(list)+1),
				Title:    strings.TrimSpace(it.Text),
				Progress: clampShare(it.Progress),
			}
			// Вес не обрезается сверху: «эта настройка в десять раз объёмнее
			// звонка» — законное соотношение. Отрицательный значит «обычный»,
			// как и ноль.
			if it.Weight > 0 {
				m.Weight = it.Weight
			}
			due, err := parseEditDate(it.Due, f.Label)
			if err != nil {
				return nil, err
			}
			m.Due = due
			list = append(list, m)
		}
		out = list

	case EditBlockers:
		list := make([]Blocker, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Summary) == "" {
				continue
			}
			kind := BlockerKind(strings.TrimSpace(it.Kind))
			if kind.Label() == string(kind) {
				// Вид блокера важнее описания: он определяет, к кому идти,
				// чтобы блокер снять. Незнакомый вид принимать нельзя.
				return nil, fmt.Errorf("%s: неизвестный вид блокера %q", f.Label, it.Kind)
			}
			since, err := parseEditDate(it.Since, f.Label)
			if err != nil {
				return nil, err
			}
			list = append(list, Blocker{
				ID:      fmt.Sprintf("b%d", len(list)+1),
				Summary: strings.TrimSpace(it.Summary),
				Kind:    kind, DependsOn: strings.TrimSpace(it.DependsOn),
				Since: since, Evidence: said(it.Note),
			})
		}
		out = list

	case EditRisks:
		list := make([]Risk, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Summary) == "" {
				continue
			}
			list = append(list, Risk{
				ID:      fmt.Sprintf("r%d", len(list)+1),
				Summary: strings.TrimSpace(it.Summary),
				// Влияние на срок не может быть отрицательным: риск сдвигает
				// срок вперёд или не сдвигает вовсе.
				DaysImpact: max(it.Days, 0),
				Spread:     strings.TrimSpace(it.Spread),
				Evidence:   said(it.Note),
			})
		}
		out = list

	case EditQuestions:
		list := make([]Question, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Text) == "" {
				continue
			}
			q := Question{
				N: len(list) + 1, Text: strings.TrimSpace(it.Text),
				Unlocks: strings.TrimSpace(it.Unlocks),
			}
			// Незаполненный ответ остаётся пробелом, а не пустой строкой со
			// словами человека: вопрос без ответа — это открытый вопрос, и срез
			// обязан считать его открытым.
			if strings.TrimSpace(it.Answer) == "" {
				q.Answer = Missing("ответа пока нет")
			} else {
				q.Answer = said(it.Answer)
			}
			list = append(list, q)
		}
		out = list

	case EditActions:
		list := make([]PMAction, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Text) == "" {
				continue
			}
			kind := PMActionKind(strings.TrimSpace(it.Kind))
			if kind.Label() == string(kind) {
				return nil, fmt.Errorf("%s: неизвестный вид действия %q", f.Label, it.Kind)
			}
			list = append(list, PMAction{
				Kind: kind, Text: strings.TrimSpace(it.Text), Why: strings.TrimSpace(it.Why),
			})
		}
		out = list

	case EditArtifacts:
		list := make([]Artifact, 0, len(in.Items))
		for _, it := range in.Items {
			if strings.TrimSpace(it.Text) == "" {
				continue
			}
			list = append(list, Artifact{
				Name:    strings.TrimSpace(it.Text),
				Present: it.Present,
				// Размер человек не называет: он берётся у приложенного файла, а
				// названный на словах ничего не значит.
				WouldGive: strings.TrimSpace(it.Why),
			})
		}
		out = list

	case EditShifts:
		list := make([]DeadlineShift, 0, len(in.Items))
		for _, it := range in.Items {
			at, err := parseEditDate(it.At, f.Label)
			if err != nil {
				return nil, err
			}
			from, err := parseEditDate(it.From, f.Label)
			if err != nil {
				return nil, err
			}
			to, err := parseEditDate(it.To, f.Label)
			if err != nil {
				return nil, err
			}
			if at.IsZero() && from.IsZero() && to.IsZero() {
				continue
			}
			list = append(list, DeadlineShift{
				At: at, From: from, To: to, Comment: strings.TrimSpace(it.Note),
			})
		}
		out = list

	default:
		return nil, fmt.Errorf("поле %q не правится", field)
	}

	doc, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("правка поля %s: %w", f.Label, err)
	}
	return doc, nil
}

// clampShare держит долю в границах 0…1: доля больше единицы означала бы этап,
// выполненный на сто двадцать процентов, а из таких долей считается готовность.
func clampShare(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// parseEditDate читает дату формы. Пустая строка — нулевая дата: «срок не
// назван» и «первое января первого года» разные утверждения.
func parseEditDate(s, label string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%s: дата %q не разобрана", label, s)
}
