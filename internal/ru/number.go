package ru

import (
	"math"
	"strconv"
	"strings"
)

// Fixed печатает число с запятой вместо точки и без хвостовых нулей: 2,4 и 3,
// а не 2.4 и 3.0. Нужен там, где цифру читает человек, а не парсер.
func Fixed(v float64, digits int) string {
	s := strconv.FormatFloat(v, 'f', digits, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return strings.Replace(s, ".", ",", 1)
}

// Percent печатает долю 0..1 процентами: 0.2667 → «27 %». Пробел перед знаком
// неразрывный, чтобы процент не оторвался от числа при переносе строки.
func Percent(share float64) string {
	return strconv.Itoa(int(math.Round(share*100))) + " %"
}
