// Package domain описывает предметную область реестра: задачу, загруженные по
// ней источники, извлечённые из них факты и срез — сводку по задаче.
//
// Пакет не зависит ни от чего, кроме стандартной библиотеки. Направление
// зависимостей в проекте — только внутрь, к домену: хранилище, аналитик и
// транспорт знают о домене, домен о них не знает.
//
// Здесь закреплено главное решение архитектуры: срез — не документ, а
// проекция. Источники и факты только добавляются, срез каждый раз собирается
// из них заново. Поэтому у любого значения в срезе есть происхождение: без
// него PM не сможет показать заказчику цифру, которую нечем подтвердить.
package domain

import (
	"math"
	"strings"
	"time"
)

// Origin — способ получения значения, отвечающий на вопрос «почему в это можно
// поверить». Интерфейс рисует по нему чип рядом с полем.
type Origin string

const (
	// OriginQuoted — значение есть в источнике дословно.
	OriginQuoted Origin = "quoted"

	// OriginDerived — значение выведено из источника, но прямо в нём не
	// написано. Так собирается, например, история переносов срока: каждый
	// перенос виден лишь в служебной строке, а вывод «срок двигали четыре
	// раза, ни один перенос не объяснён» не написан нигде.
	OriginDerived Origin = "derived"

	// OriginComputed — значение посчитано из других значений. Считает код, а не
	// модель: арифметика обязана быть воспроизводимой.
	OriginComputed Origin = "computed"

	// OriginMissing — в источниках ответа нет, нужно спрашивать людей. Такое
	// значение попадает в срез наравне с остальными: умолчать о пробеле хуже,
	// чем сам пробел.
	OriginMissing Origin = "missing"

	// OriginStated — значение назвал человек, который ведёт задачу.
	//
	// Пятое происхождение появилось не для удобства правки, а потому что без
	// него правка была бы подлогом: исправленное значение выглядело бы взятым из
	// источника, хотя в источниках его нет. Руководитель знает положение дел
	// лучше переписки — переписка отстаёт, — но знание это другого рода, и
	// назвать его надо своим именем.
	//
	// Такое значение старше любого разбора: модель его не перебивает. Иначе
	// первая же пересборка стирала бы правку и человек правил бы одно и то же по
	// кругу.
	OriginStated Origin = "stated"
)

// Label возвращает подпись происхождения для интерфейса.
func (o Origin) Label() string {
	switch o {
	case OriginQuoted:
		return "из источника"
	case OriginDerived:
		return "выведено"
	case OriginComputed:
		return "посчитано"
	case OriginMissing:
		return "нужно спросить"
	case OriginStated:
		return "сказал руководитель"
	}
	return string(o)
}

// Value — значение поля среза вместе с его происхождением.
type Value struct {
	Text     string `json:"text"`
	Origin   Origin `json:"origin"`
	SourceID string `json:"sourceId,omitempty"`
	Quote    string `json:"quote,omitempty"` // дословная выдержка из источника
	Note     string `json:"note,omitempty"`  // как получено или что спросить
}

// Quoted — значение, взятое из источника дословно.
func Quoted(text, sourceID, quote string) Value {
	return Value{Text: text, Origin: OriginQuoted, SourceID: sourceID, Quote: quote}
}

// Derived — значение, выведенное из источника; note объясняет, как именно.
func Derived(text, sourceID, note string) Value {
	return Value{Text: text, Origin: OriginDerived, SourceID: sourceID, Note: note}
}

// Computed — значение, посчитанное из других; note показывает основание счёта.
func Computed(text, note string) Value {
	return Value{Text: text, Origin: OriginComputed, Note: note}
}

// Missing — значение, которого в источниках нет; note говорит, что спросить.
func Missing(note string) Value {
	return Value{Origin: OriginMissing, Note: note}
}

// Stated — значение, названное человеком; note говорит, кто и когда.
func Stated(text, note string) Value {
	return Value{Text: text, Origin: OriginStated, Note: note}
}

// Known сообщает, удалось ли заполнить значение.
func (v Value) Known() bool {
	return v.Origin != OriginMissing && strings.TrimSpace(v.Text) != ""
}

// DaysBetween — календарная разница в днях между from и to. Считается по датам,
// а не по суткам, чтобы переход через полночь давал целый день, а переход на
// летнее время не давал дробного.
func DaysBetween(from, to time.Time) int {
	day := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	}
	return int(math.Round(day(to).Sub(day(from)).Hours() / 24))
}

// FormatDate печатает дату так, как её ждут в русском документе: 19.08.2026.
func FormatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("02.01.2006")
}
