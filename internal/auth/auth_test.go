package auth

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// pair — заведомо годная пара входов для проверок.
const pair = "kurban:kurban,amirullah:amirullah"

func newAuth(t *testing.T, users string) *Auth {
	t.Helper()

	a, err := New(users, "ключ подписи")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestCheck(t *testing.T) {
	t.Parallel()

	a := newAuth(t, pair)

	if !a.Check("kurban", "kurban") || !a.Check("amirullah", "amirullah") {
		t.Error("верный пароль не принят")
	}
	// Пароли не перепутаны между входами: одинаковая у них только форма.
	if a.Check("kurban", "amirullah") {
		t.Error("принят пароль другого пользователя")
	}
	if a.Check("kurban", "Kurban") {
		t.Error("пароль принят без учёта регистра")
	}
	if a.Check("нет такого", "kurban") {
		t.Error("принят неизвестный логин")
	}
	if a.Check("kurban", "") {
		t.Error("принят пустой пароль")
	}
}

// TestSameSaltNever: соль у каждого своя, поэтому по хешу нельзя узнать, что
// двое выбрали одинаковый пароль.
func TestSameSaltNever(t *testing.T) {
	t.Parallel()

	a := newAuth(t, "первый:одинаковый,второй:одинаковый")
	if string(a.users["первый"].hash) == string(a.users["второй"].hash) {
		t.Error("одинаковые пароли дали одинаковые хеши: соль не работает")
	}
}

// TestPasswordNotStored: пароль не хранится даже во внутреннем инструменте —
// это лишний способ его потерять.
func TestPasswordNotStored(t *testing.T) {
	t.Parallel()

	a := newAuth(t, pair)
	for login, c := range a.users {
		if strings.Contains(string(c.hash), login) {
			t.Errorf("пароль %q виден в хеше", login)
		}
	}
}

func TestIssueAndVerify(t *testing.T) {
	t.Parallel()

	a := newAuth(t, pair)
	token := a.Issue("kurban", false)

	login, ok := a.Verify(token)
	if !ok || login != "kurban" {
		t.Fatalf("пропуск не принят: %q, %v", login, ok)
	}

	// Подделанная подпись не проходит: иначе пропуск можно было бы выписать
	// себе самому, зная только формат.
	broken := token[:len(token)-3] + "AAA"
	if _, ok := a.Verify(broken); ok {
		t.Error("принят пропуск с подделанной подписью")
	}
	// Подменённый логин ломает подпись — она считается по логину вместе со
	// сроком.
	parts := strings.Split(token, ".")
	forged := enc("amirullah") + "." + parts[1] + "." + parts[2]
	if _, ok := a.Verify(forged); ok {
		t.Error("принят пропуск с подменённым логином")
	}

	for _, bad := range []string{"", "мусор", "a.b", "a.b.c.d"} {
		if _, ok := a.Verify(bad); ok {
			t.Errorf("принят пропуск %q", bad)
		}
	}
}

// TestExpired: пропуск живёт сутки, а не вечно.
func TestExpired(t *testing.T) {
	t.Parallel()

	a := newAuth(t, pair)
	token := a.Issue("kurban", false)

	a.Clock(func() time.Time { return time.Now().Add(ttl + time.Minute) })
	if _, ok := a.Verify(token); ok {
		t.Error("просроченный пропуск принят")
	}
}

// TestRemovedUser: удалённый из настроек человек не должен ходить по реестру до
// конца суток на выданном вчера пропуске.
func TestRemovedUser(t *testing.T) {
	t.Parallel()

	a := newAuth(t, pair)
	token := a.Issue("kurban", false)

	delete(a.users, "kurban")
	if _, ok := a.Verify(token); ok {
		t.Error("принят пропуск удалённого пользователя")
	}
}

// TestOtherSecret: чужой ключ подписи не подходит. Иначе пропуск, выписанный на
// одном сервере, работал бы на другом.
func TestOtherSecret(t *testing.T) {
	t.Parallel()

	token := newAuth(t, pair).Issue("kurban", false)

	other, err := New(pair, "другой ключ")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := other.Verify(token); ok {
		t.Error("принят пропуск, подписанный другим ключом")
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, users string }{
		{"пусто", ""},
		{"без пароля", "kurban"},
		{"пустой пароль", "kurban:"},
		{"пустой логин", ":kurban"},
		{"повторяющийся логин", "kurban:one,kurban:two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := New(tc.users, "ключ"); err == nil {
				t.Errorf("приняты входы %q", tc.users)
			}
		})
	}
}

// TestWeak: о пароле, совпадающем с логином, надо говорить вслух при каждом
// запуске. Он подбирается первой же попыткой, и на публичном адресе это
// означает открытый доступ ко всему реестру.
func TestWeak(t *testing.T) {
	t.Parallel()

	weak := newAuth(t, "kurban:kurban,amirullah:другой").Weak()
	if len(weak) != 1 || weak[0] != "kurban" {
		t.Errorf("слабые пароли: %v, хотели [kurban]", weak)
	}
}

// TestEmptySecretStillWorks: пустой ключ означает «сгенерировать случайный».
// Перезапуск при этом разлогинивает всех, и это честнее, чем подписывать
// предсказуемым ключом.
func TestEmptySecretStillWorks(t *testing.T) {
	t.Parallel()

	a, err := New(pair, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := a.Verify(a.Issue("kurban", false)); !ok {
		t.Error("со случайным ключом пропуск не работает")
	}
}

// TestManagers: журнал инцидентов — не для всех. В нём записи о работе
// конкретных людей, и читать их должен тот, кто оценивает, а не тот, кого
// оценивают.
func TestManagers(t *testing.T) {
	t.Parallel()

	a, err := New("kurban:k1,amirullah:a1", "секрет")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Пока роли не разделены, руководители все. Умолчание выбрано так
	// намеренно: опечатка в настройке не должна запирать человека снаружи его
	// же инструмента.
	if a.ManagersSet() {
		t.Error("роли разделены до настройки")
	}
	if !a.Manager("kurban") || !a.Manager("amirullah") {
		t.Error("без настройки кто-то не считается руководителем")
	}

	if err := a.Managers("kurban"); err != nil {
		t.Fatalf("Managers: %v", err)
	}
	if !a.ManagersSet() {
		t.Error("роли не разделились")
	}
	switch {
	case !a.Manager("kurban"):
		t.Error("названный руководитель им не считается")
	case a.Manager("amirullah"):
		t.Error("не названный вход считается руководителем")
	}

	// Без пропуска роли не бывает: пустой логин — не человек, а отсутствие
	// входа.
	if a.Manager("") {
		t.Error("пустой логин считается руководителем")
	}

	// Опечатка в логине — ошибка запуска, а не тихое «руководителей нет».
	// Иначе журнал молча закрылся бы от всех, включая того, для кого он.
	if err := a.Managers("курбан"); err == nil {
		t.Error("руководитель без входа принят")
	}
}

// TestRememberedPass: «запомнить это устройство» — пропуск на три месяца вместо
// суток. Без него вход приходилось набирать заново каждый день, а после
// перезапуска сервера — и вовсе каждый раз.
func TestRememberedPass(t *testing.T) {
	t.Parallel()

	a := newAuth(t, pair)
	now := time.Date(2026, time.September, 7, 10, 0, 0, 0, time.UTC)
	a.Clock(func() time.Time { return now })

	short := a.Issue("kurban", false)
	long := a.Issue("kurban", true)

	// Через двое суток обычный пропуск уже недействителен, а запомненный ещё
	// работает: в этом вся его суть.
	a.Clock(func() time.Time { return now.Add(48 * time.Hour) })
	if _, ok := a.Verify(short); ok {
		t.Error("обычный пропуск пережил свои сутки")
	}
	if _, ok := a.Verify(long); !ok {
		t.Error("запомненный пропуск не дожил до второго дня")
	}

	// Но и он не вечен: через полгода вход нужно подтвердить заново.
	a.Clock(func() time.Time { return now.Add(180 * 24 * time.Hour) })
	if _, ok := a.Verify(long); ok {
		t.Error("запомненный пропуск оказался бессрочным")
	}

	// Срок куки берётся из того же места, что и срок подписи: разойдясь, они
	// дали бы куку, живущую дольше пропуска, и человек видел бы «вошёл»,
	// получая отказы.
	if Life(false) >= Life(true) {
		t.Errorf("сроки перепутаны: обычный %v, запомненный %v", Life(false), Life(true))
	}
}

// TestRememberedPassCannotBeForged: срок лежит в самом пропуске, но подписан
// вместе с логином. Продлить себе доступ, поправив число, нельзя.
func TestRememberedPassCannotBeForged(t *testing.T) {
	t.Parallel()

	a := newAuth(t, pair)
	now := time.Date(2026, time.September, 7, 10, 0, 0, 0, time.UTC)
	a.Clock(func() time.Time { return now })

	parts := strings.Split(a.Issue("kurban", false), ".")
	if len(parts) != 3 {
		t.Fatalf("пропуск из %d частей", len(parts))
	}
	// Подставляем срок на год вперёд, подпись оставляем прежнюю.
	forged := parts[0] + "." +
		strconv.FormatInt(now.Add(365*24*time.Hour).Unix(), 10) + "." + parts[2]

	a.Clock(func() time.Time { return now.Add(48 * time.Hour) })
	if _, ok := a.Verify(forged); ok {
		t.Error("подделанный срок принят")
	}
}
