package domain

import "testing"

func slice(stage, deadline string, blockers []string, questions []Question) Slice {
	sl := Slice{}
	if stage != "" {
		sl.Status.Stage = Quoted(stage, "s-1", "цитата")
	}
	if deadline != "" {
		sl.Passport.Deadline = Derived(deadline, "s-1", "вывод")
	}
	for _, b := range blockers {
		sl.Blockers = append(sl.Blockers, Blocker{Summary: b, Kind: BlockerNoAccess})
	}
	sl.Questions = questions
	return sl
}

func find(t *testing.T, list []SliceChange, field string) SliceChange {
	t.Helper()

	for _, c := range list {
		if c.Field == field {
			return c
		}
	}
	t.Fatalf("изменения по полю %q нет: %+v", field, list)
	return SliceChange{}
}

func TestCompare(t *testing.T) {
	t.Parallel()

	before := slice("в работе", "31.08.2026", []string{"нет доступа"}, nil)
	after := slice("заблокировано", "", []string{"нет решения"}, nil)

	got := Compare(before, after)

	if c := find(t, got, "Этап"); c.Kind != ChangeEdited || c.Before != "в работе" || c.After != "заблокировано" {
		t.Errorf("этап: %+v", c)
	}
	// Пропавшее значение — тоже изменение, и притом важное: срок был назван, а
	// в новой версии его назвать нечем.
	if c := find(t, got, "Срок"); c.Kind != ChangeRemoved || c.Before != "31.08.2026" {
		t.Errorf("срок: %+v", c)
	}

	// Список сравнивается по содержанию: один блокер ушёл, другой появился.
	var removed, added int
	for _, c := range got {
		if c.Field != "Блокер" {
			continue
		}
		switch c.Kind {
		case ChangeRemoved:
			removed++
		case ChangeAdded:
			added++
		}
	}
	if removed != 1 || added != 1 {
		t.Errorf("блокеры: пропало %d, появилось %d — хотели по одному", removed, added)
	}
}

// TestCompareIgnoresGapWording: «нет данных, спросить у заказчика» и «нет
// данных, спросить у исполнителя» — один и тот же пробел, а не изменение среза.
// Иначе каждая пересборка показывала бы различия там, где ничего не менялось.
func TestCompareIgnoresGapWording(t *testing.T) {
	t.Parallel()

	before := Slice{Passport: Passport{Deadline: Missing("спросить у заказчика")}}
	after := Slice{Passport: Passport{Deadline: Missing("спросить у исполнителя")}}

	if got := Compare(before, after); len(got) != 0 {
		t.Errorf("разная формулировка пробела показана изменением: %+v", got)
	}
}

// TestCompareIgnoresOrder: списки собирает модель, и порядок в них не обещан.
// Сдвиг блокера на строку вверх не изменение, а перестановка выглядела бы как
// полная замена списка.
func TestCompareIgnoresOrder(t *testing.T) {
	t.Parallel()

	before := slice("", "", []string{"первый", "второй"}, nil)
	after := slice("", "", []string{"второй", "первый"}, nil)

	if got := Compare(before, after); len(got) != 0 {
		t.Errorf("перестановка показана изменением: %+v", got)
	}
}

// TestCompareSeesAnsweredQuestion: закрытие вопроса — главное, что человек ищет
// в сравнении версий.
func TestCompareSeesAnsweredQuestion(t *testing.T) {
	t.Parallel()

	q := Question{N: 1, Text: "Когда дадут доступ?"}
	before := slice("", "", nil, []Question{q})

	answered := q
	answered.Answer = Quoted("20 июня", "s-1", "20 июня")
	after := slice("", "", nil, []Question{answered})

	got := Compare(before, after)
	if len(got) != 2 {
		t.Fatalf("изменений %d, хотели 2 — вопрос без ответа пропал, отвеченный появился: %+v", len(got), got)
	}
}

// TestCompareSame: совпадающие версии дают пустой список. Это возможно и
// нормально: пересборка после нового материала могла ничего не поменять в
// выводах, и сказать об этом прямо честнее, чем показать пустой экран.
func TestCompareSame(t *testing.T) {
	t.Parallel()

	sl := slice("в работе", "31.08.2026", []string{"нет доступа"}, nil)
	if got := Compare(sl, sl); len(got) != 0 {
		t.Errorf("одинаковые версии дали различия: %+v", got)
	}
}
