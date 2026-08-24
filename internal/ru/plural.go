// Package ru согласует русский текст с числами.
//
// Отдельный пакет, потому что согласование нужно и транспорту, и аналитику, а
// домену оно не нужно: «72 дня» вместо «72 дней» — вопрос языка, а не
// предметной области.
package ru

import "strconv"

// Plural выбирает форму слова по числу: Plural(2, "день", "дня", "дней") даст
// «дня». Формы идут в порядке «1», «2», «5».
func Plural(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return many
	case n%10 == 1:
		return one
	case n%10 >= 2 && n%10 <= 4:
		return few
	}
	return many
}

// Count печатает число вместе с согласованным словом: «4 переноса».
func Count(n int, one, few, many string) string {
	return strconv.Itoa(n) + " " + Plural(n, one, few, many)
}

// CountOf печатает число со словом в родительном падеже — для оборотов вида
// «2,4 из 9 этапов». Формы идут в порядке «из 1», «из 5».
//
// Отдельно от Count, потому что падеж другой: Count даёт именительный
// («9 этапов», но «1 этап», «2 этапа»), а после «из» нужен родительный —
// «из 1 этапа», «из 2 этапов». На девяти этапах формы совпадают, на одном и на
// двух — нет.
func CountOf(n int, one, many string) string {
	if n < 0 {
		n = -n
	}
	if n%10 == 1 && n%100 != 11 {
		return strconv.Itoa(n) + " " + one
	}
	return strconv.Itoa(n) + " " + many
}

// Days печатает число дней: «1 день», «4 дня», «72 дня», «11 дней».
func Days(n int) string { return Count(n, "день", "дня", "дней") }

// Money печатает сумму в рублях с разделением разрядов: «117 000 ₽».
func Money(v int) string {
	sign := ""
	if v < 0 {
		sign, v = "−", -v
	}
	digits := strconv.Itoa(v)
	var out []byte
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, c)
	}
	return sign + string(out) + " ₽"
}
