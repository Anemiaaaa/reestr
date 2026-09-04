package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBuildCorrectionStamps: сервер сам ставит происхождение на всё, что
// прислала форма. Ради этого форма и присылает голые строки: подписаться
// цитатой из источника браузер не должен уметь в принципе.
func TestBuildCorrectionStamps(t *testing.T) {
	t.Parallel()

	doc, err := BuildCorrection("status.stage", Edit{Text: "  Сдаём заказчику  "}, "правку внёс kurban")
	if err != nil {
		t.Fatalf("BuildCorrection: %v", err)
	}

	var v Value
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	switch {
	case v.Text != "Сдаём заказчику":
		t.Errorf("текст = %q — пробелы по краям не срезаны", v.Text)
	case v.Origin != OriginStated:
		t.Errorf("происхождение = %q, хотели stated", v.Origin)
	case v.Note != "правку внёс kurban":
		t.Errorf("подпись = %q", v.Note)
	case v.SourceID != "":
		t.Errorf("у сказанного человеком появился источник: %q", v.SourceID)
	}
}

// TestBuildCorrectionRejects: правка не всесильна. Пустое одиночное значение
// стёрло бы ответ, которого у человека как раз нет причины не иметь, а
// незнакомый вид блокера сделал бы бессмысленной подпись «к кому идти».
func TestBuildCorrectionRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		field string
		in    Edit
		want  string
	}{
		{"пустое значение", "status.stage", Edit{Text: "   "}, "не может быть пустым"},
		{"поля нет в списке", "status.readiness", Edit{Text: "80 %"}, "не правится"},
		{
			"незнакомый вид блокера", "blockers",
			Edit{Items: []EditItem{{Summary: "стоим", Kind: "потому что"}}},
			"неизвестный вид блокера",
		},
		{
			"незнакомый вид действия", "pmActions.needed",
			Edit{Items: []EditItem{{Text: "сделать", Kind: "как-нибудь"}}},
			"неизвестный вид действия",
		},
		{
			"неразобранная дата", "status.milestones",
			Edit{Items: []EditItem{{Text: "Этап", Due: "как-нибудь в мае"}}},
			"не разобрана",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildCorrection(tt.field, tt.in, "правка")
			if err == nil {
				t.Fatalf("правка принята, хотя не должна была")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ошибка %q, ожидали упоминание %q", err, tt.want)
			}
		})
	}
}

// TestBuildCorrectionLists: списки человек называет целиком. Пустые строки
// выпадают, номера проставляются заново — номер это адрес в разговоре
// («третий критерий не закрыт»), и дырка в нумерации сделала бы адрес враньём.
func TestBuildCorrectionLists(t *testing.T) {
	t.Parallel()

	doc, err := BuildCorrection("goal.criteria", Edit{Items: []EditItem{
		{Text: "Аренда подключена", Met: true},
		{Text: "   "},
		{Text: "Обучение завершено", Note: "менеджеры частично"},
	}}, "правка")
	if err != nil {
		t.Fatalf("BuildCorrection: %v", err)
	}

	var cs []Criterion
	if err := json.Unmarshal(doc, &cs); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if len(cs) != 2 {
		t.Fatalf("критериев %d, хотели 2: %+v", len(cs), cs)
	}
	if cs[0].N != 1 || cs[1].N != 2 {
		t.Errorf("номера после выпавшей строки: %d, %d", cs[0].N, cs[1].N)
	}
	if !cs[0].Met || cs[1].Met {
		t.Errorf("отметки выполнения: %v, %v", cs[0].Met, cs[1].Met)
	}
	if cs[1].Note != "менеджеры частично" {
		t.Errorf("пояснение = %q", cs[1].Note)
	}
}

// TestBuildCorrectionEmptyList: пустой список — законная правка. «Блокеров
// больше нет» это утверждение, а не отсутствие ответа, и запретить его значило
// бы оставить в срезе снятый блокер навсегда.
func TestBuildCorrectionEmptyList(t *testing.T) {
	t.Parallel()

	doc, err := BuildCorrection("blockers", Edit{}, "правка")
	if err != nil {
		t.Fatalf("BuildCorrection: %v", err)
	}
	var bs []Blocker
	if err := json.Unmarshal(doc, &bs); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if len(bs) != 0 {
		t.Errorf("блокеров %d, хотели 0", len(bs))
	}
}

// TestBuildCorrectionQuestionWithoutAnswer: вопрос без ответа остаётся
// открытым. Пустая строка со словами человека закрыла бы его молча, и срез
// перестал бы считать пробелы.
func TestBuildCorrectionQuestionWithoutAnswer(t *testing.T) {
	t.Parallel()

	doc, err := BuildCorrection("questions", Edit{Items: []EditItem{
		{Text: "Кто подписывает акт?"},
		{Text: "Сколько лицензий?", Answer: "Двенадцать"},
	}}, "правка")
	if err != nil {
		t.Fatalf("BuildCorrection: %v", err)
	}

	var qs []Question
	if err := json.Unmarshal(doc, &qs); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("вопросов %d, хотели 2", len(qs))
	}
	if qs[0].Answered() {
		t.Error("вопрос без ответа считается закрытым")
	}
	if !qs[1].Answered() || qs[1].Answer.Origin != OriginStated {
		t.Errorf("ответ человека: %+v", qs[1].Answer)
	}
}

// TestBuildCorrectionClampsProgress: доля выполнения этапа держится в границах
// нуля и единицы. Из этих долей считается готовность — первая цифра, которую
// называют заказчику, — и этап «на сто двадцать процентов» испортил бы её.
func TestBuildCorrectionClampsProgress(t *testing.T) {
	t.Parallel()

	doc, err := BuildCorrection("status.milestones", Edit{Items: []EditItem{
		{Text: "Перебор", Progress: 1.4},
		{Text: "Недобор", Progress: -2},
		{Text: "В срок", Progress: 0.5, Due: "2026-09-04"},
	}}, "правка")
	if err != nil {
		t.Fatalf("BuildCorrection: %v", err)
	}

	var ms []Milestone
	if err := json.Unmarshal(doc, &ms); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if len(ms) != 3 {
		t.Fatalf("этапов %d, хотели 3", len(ms))
	}
	if ms[0].Progress != 1 || ms[1].Progress != 0 {
		t.Errorf("доли вне границ: %v, %v", ms[0].Progress, ms[1].Progress)
	}
	if want := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.UTC); !ms[2].Due.Equal(want) {
		t.Errorf("срок этапа = %v, хотели %v", ms[2].Due, want)
	}
	// Незаполненный срок остаётся нулевым: «срок не назван» и «первое января
	// первого года» — разные утверждения.
	if !ms[0].Due.IsZero() {
		t.Errorf("пустой срок заполнился: %v", ms[0].Due)
	}
}

// TestApply: правка ложится в своё поле и только в него.
func TestApply(t *testing.T) {
	t.Parallel()

	sl := Slice{
		TaskID:   "aura",
		Passport: Passport{Title: Quoted("Старое название", "s1", "цитата")},
		Status:   Status{Stage: Quoted("Не начато", "s1", "цитата")},
	}

	doc, err := BuildCorrection("status.stage", Edit{Text: "Идёт приёмка"}, "правка")
	if err != nil {
		t.Fatalf("BuildCorrection: %v", err)
	}
	if err := sl.Apply(Correction{Field: "status.stage", Doc: doc}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if sl.Status.Stage.Text != "Идёт приёмка" || sl.Status.Stage.Origin != OriginStated {
		t.Errorf("этап не поправлен: %+v", sl.Status.Stage)
	}
	if sl.Passport.Title.Text != "Старое название" {
		t.Errorf("правка этапа задела название: %q", sl.Passport.Title.Text)
	}

	// Неизвестный адрес — отказ, а не молчание: иначе правка пропадала бы
	// бесследно, и человек считал бы её сделанной.
	if err := sl.Apply(Correction{Field: "выдумка", Doc: doc}); err == nil {
		t.Error("правка неизвестного поля принята")
	}
}

// TestLatestCorrections: по полю остаётся последняя правка. На этом держится
// правка списков — список называют целиком, и предыдущий список должен уступить
// новому целиком же, а не смешаться с ним.
func TestLatestCorrections(t *testing.T) {
	t.Parallel()

	got := LatestCorrections([]Correction{
		{ID: "c1", Field: "status.stage", Doc: []byte(`{"text":"первая"}`)},
		{ID: "c2", Field: "goal.criteria", Doc: []byte(`[]`)},
		{ID: "c3", Field: "status.stage", Doc: []byte(`{"text":"вторая"}`)},
	})

	if len(got) != 2 {
		t.Fatalf("полей %d, хотели 2", len(got))
	}
	if got["status.stage"].ID != "c3" {
		t.Errorf("по этапу осталась %s, хотели c3", got["status.stage"].ID)
	}
}

// TestEditableCovers: список правимых полей и разбор адресов в Apply обязаны
// совпадать. Разойдясь, они дали бы поле, которое интерфейс предлагает
// поправить, а сервер отказывается принять.
func TestEditableCovers(t *testing.T) {
	t.Parallel()

	for _, f := range Editable() {
		var sl Slice
		doc, err := BuildCorrection(f.Field, sampleEdit(f.Kind), "правка")
		if err != nil {
			t.Errorf("%s: BuildCorrection: %v", f.Field, err)
			continue
		}
		if err := sl.Apply(Correction{Field: f.Field, Doc: doc}); err != nil {
			t.Errorf("%s: Apply: %v", f.Field, err)
		}
	}

	// Готовности в списке нет и быть не должно: она считается по этапам, и
	// правка превратила бы её в число, которое ничем не объясняется.
	if _, ok := EditableField("status.readiness"); ok {
		t.Error("готовность попала в правимые поля")
	}
}

// sampleEdit возвращает заполненную правку для вида поля.
func sampleEdit(kind EditKind) Edit {
	switch kind {
	case EditValue:
		return Edit{Text: "значение"}
	case EditBlockers:
		return Edit{Items: []EditItem{{Summary: "стоим", Kind: string(BlockerNoInfo)}}}
	case EditRisks:
		return Edit{Items: []EditItem{{Summary: "может съехать", Days: 3}}}
	case EditActions:
		return Edit{Items: []EditItem{{Text: "созвониться", Kind: string(ActionConnect)}}}
	case EditShifts:
		return Edit{Items: []EditItem{{At: "2026-09-01", From: "2026-09-01", To: "2026-09-08"}}}
	}
	return Edit{Items: []EditItem{{Text: "строка"}}}
}
