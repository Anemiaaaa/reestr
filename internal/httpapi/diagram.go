package httpapi

import (
	"fmt"

	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/ru"
)

// step — шаг процесса на своей дорожке.
//
// Row и Col — клетка сетки, а не пиксели. Кто делает шаг и каким он идёт по
// порядку — это смысл схемы, и он считается здесь. Размер клетки в пикселях —
// дело вёрстки, и остаётся во фронтенде.
type step struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Note    string `json:"note,omitempty"`
	Lane    string `json:"lane"`
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	Problem bool   `json:"problem"`
}

// link — переход между шагами.
type link struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
	Back  bool   `json:"back"`
}

// change — что меняется между «как есть» и «как будет».
type change struct {
	Op      string `json:"op"`
	OpLabel string `json:"opLabel"`
	NodeID  string `json:"nodeId,omitempty"`
	Text    string `json:"text"`
}

// diagram — схема процесса, готовая к выводу.
type diagram struct {
	Kind      string   `json:"kind"`
	KindLabel string   `json:"kindLabel"`
	Title     string   `json:"title"`
	Lanes     []string `json:"lanes"`
	Cols      int      `json:"cols"`
	Steps     []step   `json:"steps"`
	Links     []link   `json:"links"`
	Changes   []change `json:"changes"`
	Evidence  value    `json:"evidence"`

	// Итоги схемы словами: их читают вместо пересчёта прямоугольников на экране.
	StepsText    string `json:"stepsText"`
	LanesText    string `json:"lanesText"`
	ProblemsText string `json:"problemsText,omitempty"`
	LoopsText    string `json:"loopsText,omitempty"`
}

// newDiagram переводит схему процесса в представление.
//
// Раскладка — дорожки по вертикали, порядок шагов по горизонтали. Порядок берётся
// из последовательности узлов, как их перечислил аналитик: это и есть порядок
// рассказа о процессе. Получается лестница, на которой каждая передача из рук в
// руки видна как ступенька — именно то, что в схеме и надо разглядеть.
func newDiagram(p domain.Process, srcs sources) diagram {
	lanes := p.Rows()
	row := make(map[string]int, len(lanes))
	for i, name := range lanes {
		row[name] = i
	}

	d := diagram{
		Kind:      string(p.Kind),
		KindLabel: p.Kind.Label(),
		Title:     p.Title,
		Lanes:     lanes,
		Cols:      len(p.Nodes),
		Steps:     make([]step, 0, len(p.Nodes)),
		Links:     make([]link, 0, len(p.Edges)),
		Changes:   make([]change, 0, len(p.Changes)),
		Evidence:  newValue(p.Evidence, srcs),
	}

	for i, n := range p.Nodes {
		d.Steps = append(d.Steps, step{
			ID:      n.ID,
			Title:   n.Title,
			Note:    n.Note,
			Lane:    n.Row,
			Row:     row[n.Row],
			Col:     i,
			Problem: n.Problem,
		})
	}
	for _, e := range p.Edges {
		d.Links = append(d.Links, link{From: e.From, To: e.To, Label: e.Label, Back: e.Back})
	}
	for _, c := range p.Changes {
		d.Changes = append(d.Changes, change{
			Op:      string(c.Op),
			OpLabel: c.Op.Label(),
			NodeID:  c.NodeID,
			Text:    c.Text,
		})
	}

	d.StepsText = ru.Count(len(p.Nodes), "шаг", "шага", "шагов")
	d.LanesText = ru.Count(len(lanes), "участник", "участника", "участников")
	if n := len(p.Problems()); n > 0 {
		d.ProblemsText = fmt.Sprintf("%s с проблемой",
			ru.Count(n, "шаг", "шага", "шагов"))
	}
	if n := len(p.Loops()); n > 0 {
		d.LoopsText = ru.Count(n, "возврат назад", "возврата назад", "возвратов назад")
	}
	return d
}

func newDiagrams(list []domain.Process, srcs sources) []diagram {
	out := make([]diagram, 0, len(list))
	for _, p := range list {
		out = append(out, newDiagram(p, srcs))
	}
	return out
}
