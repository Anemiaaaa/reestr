package domain

import (
	"math"
	"testing"
	"time"

	// Тесту нужна настоящая зона с переходом на летнее время. На Windows базы
	// зон в системе нет, и без этого импорта LoadLocation вернёт ошибку — то
	// есть проверка молча превратилась бы в пропуск.
	_ "time/tzdata"
)

// utc — короткая запись календарной даты для таблиц ниже.
func utc(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// TestDaysBetween проверяет то, ради чего функция вообще написана: расстояние
// между датами, а не между моментами. Весь срез состоит из таких расстояний —
// просрочка этапа, возраст блокера, остаток срока, — и ошибка на день здесь
// расходится по всему отчёту.
func TestDaysBetween(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("зона с переходом на летнее время не загрузилась: %v", err)
	}
	msk := time.FixedZone("MSK", 3*60*60)

	tests := []struct {
		name     string
		from, to time.Time
		want     int
	}{
		{
			name: "сутки",
			from: utc(2026, time.August, 20),
			to:   utc(2026, time.August, 21),
			want: 1,
		},
		{
			name: "тот же день, разное время",
			from: time.Date(2026, time.August, 20, 1, 0, 0, 0, time.UTC),
			to:   time.Date(2026, time.August, 20, 23, 59, 0, 0, time.UTC),
			want: 0,
		},
		{
			// Два часа, но через полночь: это уже другой день, и просрочка
			// должна вырасти на единицу, а не на 0,08.
			name: "два часа через полночь — целый день",
			from: time.Date(2026, time.August, 20, 23, 0, 0, 0, time.UTC),
			to:   time.Date(2026, time.August, 21, 1, 0, 0, 0, time.UTC),
			want: 1,
		},
		{
			name: "назад по календарю — отрицательное",
			from: utc(2026, time.August, 21),
			to:   utc(2026, time.August, 20),
			want: -1,
		},
		{
			name: "от 20 августа до срока АУРА",
			from: utc(2026, time.August, 20),
			to:   utc(2026, time.August, 31),
			want: 11,
		},
		{
			// В сутках перехода на летнее время 23 часа. Без округления 23/24
			// усеклось бы в ноль, и день просто исчез бы из просрочки.
			name: "сутки перехода на летнее время",
			from: time.Date(2026, time.March, 8, 0, 0, 0, 0, ny),
			to:   time.Date(2026, time.March, 9, 0, 0, 0, 0, ny),
			want: 1,
		},
		{
			name: "сутки возврата на зимнее время — 25 часов",
			from: time.Date(2026, time.November, 1, 0, 0, 0, 0, ny),
			to:   time.Date(2026, time.November, 2, 0, 0, 0, 0, ny),
			want: 1,
		},
		{
			// Месяц с переходом внутри: 743 часа вместо 744. Усечение дало бы
			// 30 дней вместо 31.
			name: "месяц через переход целиком",
			from: time.Date(2026, time.March, 1, 0, 0, 0, 0, ny),
			to:   time.Date(2026, time.April, 1, 0, 0, 0, 0, ny),
			want: 31,
		},
		{
			name: "полночь местная, а не по UTC",
			from: time.Date(2026, time.August, 20, 23, 30, 0, 0, msk),
			to:   time.Date(2026, time.August, 21, 0, 30, 0, 0, msk),
			want: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DaysBetween(tc.from, tc.to); got != tc.want {
				t.Errorf("DaysBetween(%s, %s) = %d, хотели %d",
					tc.from.Format(time.RFC3339), tc.to.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

func TestFormatDate(t *testing.T) {
	if got := FormatDate(time.Time{}); got != "" {
		t.Errorf("нулевая дата = %q, хотели пустую строку", got)
	}
	if got, want := FormatDate(utc(2026, time.August, 31)), "31.08.2026"; got != want {
		t.Errorf("FormatDate = %q, хотели %q", got, want)
	}
}

// TestValueKnown проверяет границу между «мы это знаем» и «здесь пробел».
// От неё зависит и счётчик пробелов в сводке, и то, перебьёт ли факт поле
// карточки.
func TestValueKnown(t *testing.T) {
	tests := []struct {
		name string
		v    Value
		want bool
	}{
		{"цитата", Quoted("31.08.2026", "s1", "срок — до 31 августа"), true},
		{"вывод", Derived("в работе", "s1", "по ленте задачи"), true},
		{"расчёт", Computed("27 %", "2,4 из 9 этапов закрыто"), true},
		{"пробел", Missing("в источниках не найдено"), false},
		{
			// Происхождение известно, а текста нет: это тоже пробел, иначе
			// пустая строка проехала бы в срез как ответ.
			name: "пустой текст при известном происхождении",
			v:    Value{Text: "   ", Origin: OriginQuoted, SourceID: "s1"},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.Known(); got != tc.want {
				t.Errorf("Known = %v, хотели %v", got, tc.want)
			}
		})
	}
}

// TestReadiness проверяет не только процент, но и его основание: срез
// показывает «2,4 из 9 этапов», и сумма возвращается отдельно именно для этого.
func TestReadiness(t *testing.T) {
	// План АУРА: два этапа закрыты, два начаты на 20 %, пять не начаты.
	plan := []Milestone{
		{Progress: 1}, {Progress: 1},
		{Progress: 0.2}, {Progress: 0.2},
		{Progress: 0}, {Progress: 0}, {Progress: 0}, {Progress: 0}, {Progress: 0},
	}

	share, done, total := Readiness(plan)
	if want := 2.4; math.Abs(done-want) > 1e-9 {
		t.Errorf("сумма прогресса = %v, хотели %v", done, want)
	}
	// Этапы без веса считаются как прежде: весь объём равен их числу.
	if want := 9.0; math.Abs(total-want) > 1e-9 {
		t.Errorf("весь объём = %v, хотели %v", total, want)
	}
	if want := 2.4 / 9; math.Abs(share-want) > 1e-9 {
		t.Errorf("доля = %v, хотели %v", share, want)
	}
	// Именно из этой доли получается подпись «27 %» в сводке.
	if got := int(math.Round(share * 100)); got != 27 {
		t.Errorf("округлённая доля = %d, хотели 27", got)
	}

	if share, done, total := Readiness(nil); share != 0 || done != 0 || total != 0 {
		t.Errorf("плана нет: (%v, %v, %v), хотели нули", share, done, total)
	}
}

// TestReadinessWeighted: этапы редко равны по объёму, и среднее по ним занижает
// готовность там, где объёмная часть уже закрыта. Ровно этот случай и был на
// живой задаче: настройка всех рабочих мест сделана, остался короткий урок, а
// среднее показывало половину.
func TestReadinessWeighted(t *testing.T) {
	plan := []Milestone{
		{Progress: 1, Weight: 4},   // большая работа, закрыта
		{Progress: 0, Weight: 0.5}, // мелочь, не начата
	}

	share, done, total := Readiness(plan)
	switch {
	case math.Abs(done-4) > 1e-9:
		t.Errorf("закрытый объём = %v, хотели 4", done)
	case math.Abs(total-4.5) > 1e-9:
		t.Errorf("весь объём = %v, хотели 4,5", total)
	case math.Abs(share-4.0/4.5) > 1e-9:
		t.Errorf("доля = %v, хотели %v", share, 4.0/4.5)
	}
	// 89 %, а не 50 %: разница между «почти сделано» и «сделано наполовину» —
	// это разный разговор с заказчиком.
	if got := int(math.Round(share * 100)); got != 89 {
		t.Errorf("округлённая доля = %d, хотели 89", got)
	}

	if !Weighted(plan) {
		t.Error("разные веса не опознаны")
	}
	// Ноль и единица — одно и то же: план, где объём не проставлен, взвешенным
	// не считается, и подпись под процентом остаётся прежней.
	if Weighted([]Milestone{{Progress: 1}, {Weight: 1}}) {
		t.Error("план без весов сочтён взвешенным")
	}
	if got := (Milestone{}).Load(); got != 1 {
		t.Errorf("вес этапа без веса = %v, хотели 1", got)
	}
	if got := (Milestone{Weight: -3}).Load(); got != 1 {
		t.Errorf("отрицательный вес = %v, хотели 1", got)
	}
}

func TestMilestoneDone(t *testing.T) {
	tests := []struct {
		progress float64
		want     bool
	}{{0, false}, {0.2, false}, {0.999, false}, {1, true}, {1.2, true}}

	for _, tc := range tests {
		if got := (Milestone{Progress: tc.progress}).Done(); got != tc.want {
			t.Errorf("прогресс %v: Done = %v, хотели %v", tc.progress, got, tc.want)
		}
	}
}

func TestMilestoneOverdueDays(t *testing.T) {
	now := utc(2026, time.August, 20)

	tests := []struct {
		name string
		m    Milestone
		want int
	}{
		{"срок в будущем", Milestone{Due: utc(2026, time.September, 1)}, 0},
		{"срок сегодня", Milestone{Due: now}, 0},
		{"срок прошёл", Milestone{Due: utc(2026, time.August, 10)}, 10},
		{"закрытый этап не просрочен", Milestone{Due: utc(2026, time.June, 1), Progress: 1}, 0},
		{"срока нет", Milestone{Progress: 0.5}, 0},
		{
			// «Сделано наполовину» не отменяет пропущенной даты: иначе
			// просрочка исчезала бы ровно там, где о ней важнее всего сказать.
			name: "начатый этап с прошедшим сроком всё равно просрочен",
			m:    Milestone{Due: utc(2026, time.July, 21), Progress: 0.5},
			want: 30,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.OverdueDays(now); got != tc.want {
				t.Errorf("OverdueDays = %d, хотели %d", got, tc.want)
			}
		})
	}
}

// TestOverdueMilestones проверяет и отбор, и порядок: сводка берёт нулевой
// элемент и подписывает «самый старый на N дней».
func TestOverdueMilestones(t *testing.T) {
	now := utc(2026, time.August, 20)
	plan := []Milestone{
		{ID: "m3", Due: utc(2026, time.August, 15)},
		{ID: "m1", Due: utc(2026, time.June, 30)},
		{ID: "closed", Due: utc(2026, time.May, 1), Progress: 1},
		{ID: "future", Due: utc(2026, time.September, 30)},
		{ID: "m2", Due: utc(2026, time.July, 31)},
		{ID: "nodate"},
	}

	got := OverdueMilestones(plan, now)
	want := []string{"m1", "m2", "m3"}
	if len(got) != len(want) {
		t.Fatalf("просрочено %d этапов, хотели %d: %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("этап %d = %q, хотели %q", i, got[i].ID, id)
		}
	}
	if n := got[0].OverdueDays(now); n != 51 {
		t.Errorf("самый старый просрочен на %d дней, хотели 51", n)
	}

	if got := OverdueMilestones(nil, now); len(got) != 0 {
		t.Errorf("плана нет, а просрочено %d этапов", len(got))
	}
}

func TestBlockerAgeDays(t *testing.T) {
	now := utc(2026, time.August, 20)

	tests := []struct {
		name string
		b    Blocker
		want int
	}{
		{"блокер АУРА с 8 июня", Blocker{Since: utc(2026, time.June, 8)}, 73},
		{"даты нет — возраста нет", Blocker{}, 0},
		{"появился сегодня", Blocker{Since: now}, 0},
		{"дата в будущем не даёт отрицательного возраста",
			Blocker{Since: utc(2026, time.September, 1)}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.AgeDays(now); got != tc.want {
				t.Errorf("AgeDays = %d, хотели %d", got, tc.want)
			}
		})
	}
}

func TestUnexplained(t *testing.T) {
	shifts := []DeadlineShift{
		{From: utc(2026, time.June, 30), To: utc(2026, time.July, 31)},
		{
			From:    utc(2026, time.July, 31),
			To:      utc(2026, time.August, 31),
			Comment: "срок продлён по просьбе заказчика",
		},
		// Пробелы вместо причины — это не объяснение.
		{From: utc(2026, time.August, 31), To: utc(2026, time.September, 30), Comment: "   "},
	}

	if got, want := Unexplained(shifts), 2; got != want {
		t.Errorf("переносов без объяснения = %d, хотели %d", got, want)
	}
	if got := Unexplained(nil); got != 0 {
		t.Errorf("переносов нет, а без объяснения %d", got)
	}
}

func TestMetCriteria(t *testing.T) {
	cs := []Criterion{
		{N: 1, Met: true}, {N: 2}, {N: 3, Met: true}, {N: 4}, {N: 5}, {N: 6},
	}
	if got, want := MetCriteria(cs), 2; got != want {
		t.Errorf("критериев выполнено %d, хотели %d", got, want)
	}
	if got := MetCriteria(nil); got != 0 {
		t.Errorf("критериев нет, а выполнено %d", got)
	}
}

// TestSliceOldestBlocker: блокер без даты не может быть самым старым — про него
// нечего сказать в подписи «висит N дней».
func TestSliceOldestBlocker(t *testing.T) {
	if _, ok := (Slice{}).OldestBlocker(); ok {
		t.Error("в срезе без блокеров нашёлся блокер")
	}

	only := Slice{Blockers: []Blocker{{ID: "b-nodate"}}}
	if b, ok := only.OldestBlocker(); ok {
		t.Errorf("блокер без даты выдан как самый старый: %q", b.ID)
	}

	sl := Slice{Blockers: []Blocker{
		{ID: "b-nodate"},
		{ID: "b-june", Since: utc(2026, time.June, 8)},
		{ID: "b-july", Since: utc(2026, time.July, 1)},
	}}
	b, ok := sl.OldestBlocker()
	if !ok {
		t.Fatal("блокер не найден")
	}
	if b.ID != "b-june" {
		t.Errorf("самый старый = %q, хотели b-june", b.ID)
	}
}

// TestSliceGaps проверяет счётчик, который стоит в сводке рядом с готовностью.
// «27 % готово» без числа пробелов читается как оценка задачи, хотя это оценка
// того, что мы про задачу знаем.
func TestSliceGaps(t *testing.T) {
	// Пустой срез — это десять неизвестных значений паспорта, цели и статуса.
	if got, want := (Slice{}).Gaps(), 10; got != want {
		t.Errorf("пустой срез: %d пробелов, хотели %d", got, want)
	}

	known := func(text string) Value {
		return Value{Text: text, Origin: OriginQuoted, SourceID: "s1"}
	}
	full := Slice{
		Passport: Passport{
			Title:    known("Внедрение автоматизации"),
			Author:   known("Ризван Мирзаев"),
			Assignee: known("Мухаммад Минатулаев"),
			OpenedAt: known("01.06.2026"),
			Deadline: known("31.08.2026"),
		},
		Goal: Goal{
			AsStated:  known("как поставлено"),
			Clarified: known("как понято"),
		},
		Status: Status{
			Stage:     known("в работе"),
			Readiness: known("27 %"),
		},
		PMActions: PMActions{NextCheck: known("25.08.2026")},
	}
	if got := full.Gaps(); got != 0 {
		t.Errorf("заполненный срез: %d пробелов, хотели 0", got)
	}

	// Открытые вопросы и неприложенные файлы — тоже пробелы: без них цифра
	// готовности выглядит достовернее, чем есть.
	full.Questions = []Question{
		{N: 1, Answer: known("ответ есть")},
		{N: 2},
		{N: 3, Answer: Missing("специалист не ответил")},
	}
	full.Artifacts = []Artifact{
		{Name: "смета.xlsx", Present: true},
		{Name: "схема AS IS", Present: false},
	}
	if got, want := full.Gaps(), 3; got != want {
		t.Errorf("два вопроса и один файл: %d пробелов, хотели %d", got, want)
	}

	if got, want := len(full.OpenQuestions()), 2; got != want {
		t.Errorf("открытых вопросов %d, хотели %d", got, want)
	}
	if got, want := len(full.MissingArtifacts()), 1; got != want {
		t.Errorf("неприложенных файлов %d, хотели %d", got, want)
	}
}
