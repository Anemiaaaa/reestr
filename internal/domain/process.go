package domain

// ProcessKind — какой процесс описан: текущий или целевой.
type ProcessKind string

const (
	ProcessAsIs ProcessKind = "as_is"
	ProcessToBe ProcessKind = "to_be"
)

// Label возвращает подпись процесса для интерфейса.
func (k ProcessKind) Label() string {
	switch k {
	case ProcessAsIs:
		return "как есть"
	case ProcessToBe:
		return "как будет"
	}
	return string(k)
}

// Valid сообщает, известен ли такой вид процесса.
func (k ProcessKind) Valid() bool {
	return k == ProcessAsIs || k == ProcessToBe
}

// Node — шаг процесса.
//
// Row задаёт дорожку: кто выполняет шаг. Раскладка по дорожкам — часть смысла
// схемы, а не оформление: она показывает, сколько раз работа переходит из рук
// в руки, и именно эти переходы обычно и есть проблема.
type Node struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Note    string `json:"note,omitempty"`
	Row     string `json:"row,omitempty"`
	Problem bool   `json:"problem,omitempty"` // шаг, который и надо убрать
}

// Edge — переход между шагами.
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
	Back  bool   `json:"back,omitempty"` // возврат назад: доработка, отказ, круг
}

// Process — схема бизнес-процесса, «как есть» или «как будет».
type Process struct {
	TaskID string      `json:"taskId"`
	Kind   ProcessKind `json:"kind"`
	Title  string      `json:"title"`
	Nodes  []Node      `json:"nodes,omitempty"`
	Edges  []Edge      `json:"edges,omitempty"`

	// Changes — чем целевой процесс отличается от текущего. Заполняется только
	// у to_be: схема без списка отличий не показывает, за что заплатили.
	Changes []Change `json:"changes,omitempty"`

	// Evidence — на чём построена схема. Схема без ссылки на источник — рисунок,
	// а не вывод.
	Evidence Value `json:"evidence"`
}

// Rows возвращает дорожки в порядке первого появления шагов.
func (p Process) Rows() []string {
	var rows []string
	seen := map[string]bool{}
	for _, n := range p.Nodes {
		if n.Row == "" || seen[n.Row] {
			continue
		}
		seen[n.Row] = true
		rows = append(rows, n.Row)
	}
	return rows
}

// Loops возвращает переходы-возвраты: круги доработок в процессе.
func (p Process) Loops() []Edge {
	var out []Edge
	for _, e := range p.Edges {
		if e.Back {
			out = append(out, e)
		}
	}
	return out
}

// Problems возвращает шаги, помеченные как проблемные.
func (p Process) Problems() []Node {
	var out []Node
	for _, n := range p.Nodes {
		if n.Problem {
			out = append(out, n)
		}
	}
	return out
}

// ChangeOp — характер изменения при переходе к целевому процессу.
type ChangeOp string

const (
	ChangeRemove ChangeOp = "remove" // шаг уходит
	ChangeAdd    ChangeOp = "add"    // шаг появляется
	ChangeAlter  ChangeOp = "change" // шаг остаётся, но делается иначе
)

// Label возвращает подпись изменения для интерфейса.
func (o ChangeOp) Label() string {
	switch o {
	case ChangeRemove:
		return "убираем"
	case ChangeAdd:
		return "добавляем"
	case ChangeAlter:
		return "меняем"
	}
	return string(o)
}

// Change — одно отличие целевого процесса от текущего.
type Change struct {
	Op     ChangeOp `json:"op"`
	NodeID string   `json:"nodeId,omitempty"`
	Text   string   `json:"text"`
}
