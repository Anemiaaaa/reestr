// Package auth — вход по логину и паролю.
//
// Пакет решает ровно две задачи: проверить пару «логин, пароль» и выдать
// пропуск, по которому браузер узнают в следующем запросе. Прав он не
// различает: у всех, кто вошёл, права одинаковые. Разделение прав появится,
// когда появится первый человек, которому нужно показать меньше остальных, —
// заводить роли раньше значит поддерживать пустую механику.
//
// Пропуск — подписанная кука, а не запись в базе. Сессий немного, живут они
// сутки, и хранилище под них добавило бы таблицу, миграцию и уборку
// просроченных записей ради того, что помещается в подпись.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// iterations — сколько раз прогоняется пароль.
	//
	// Смысл в том, чтобы проверка пароля была заметно дорогой: подбор идёт
	// перебором, и каждая лишняя миллисекунда умножается на число попыток. Сто
	// тысяч итераций — сотая доля секунды на вход и годы на перебор словаря.
	iterations = 100_000

	saltLen = 16
	keyLen  = 32

	// ttl — сколько живёт обычный пропуск. Сутки: рабочий день плюс запас,
	// чтобы вход не терялся на обеде.
	ttl = 24 * time.Hour

	// longTTL — пропуск на запомненном устройстве. Три месяца.
	//
	// Срок большой намеренно. «Запомнить» просят не ради удобства вообще, а
	// ради своего компьютера, за которым сидят каждый день; пропуск, который
	// приходится обновлять раз в неделю, эту просьбу не выполняет. Отозвать его
	// можно двумя способами: выйти на этом устройстве или сменить ключ подписи —
	// второе выбьет все устройства разом.
	longTTL = 90 * 24 * time.Hour
)

// Life — сколько живёт пропуск: обычный или на запомненном устройстве.
//
// Срок выдаёт auth, а не тот, кто ставит куку: их два, и разойдясь, они дали бы
// куку, живущую дольше подписи, — браузер считал бы вход целым, а сервер бы его
// не признавал. Человек видел бы «вошёл» и получал отказы.
func Life(remember bool) time.Duration {
	if remember {
		return longTTL
	}
	return ttl
}

// cred — соль и хеш пароля.
//
// Пароль не хранится: даже во внутреннем инструменте это лишний способ его
// потерять. Соль у каждого своя, поэтому одинаковые пароли дают разные хеши, и
// по хешу нельзя узнать, что двое выбрали одно и то же.
type cred struct {
	salt []byte
	hash []byte
}

// Auth — список входов и подпись пропусков.
type Auth struct {
	users  map[string]cred
	secret []byte
	now    func() time.Time

	// managers — кто видит журнал инцидентов. Пустая карта означает «все»:
	// опечатка в настройке не должна запирать человека снаружи его инструмента.
	managers map[string]bool
}

// New собирает вход из строки «логин:пароль,логин:пароль».
//
// Пароли приходят открытым текстом и тут же превращаются в хеши: строка живёт в
// настройках, а не в памяти процесса дольше запуска. Формат выбран ради того,
// чтобы смена пароля была правкой одной строки в .env, а не походом в базу.
//
// secret подписывает пропуска. Пустой означает «сгенерировать случайный»: тогда
// перезапуск сервера разлогинивает всех, и это честнее, чем подписывать
// предсказуемым ключом.
func New(users, secret string) (*Auth, error) {
	a := &Auth{
		users:  make(map[string]cred),
		secret: []byte(secret),
		now:    time.Now,
	}

	for _, pair := range strings.Split(users, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		login, password, ok := strings.Cut(pair, ":")
		login = strings.TrimSpace(login)
		switch {
		case !ok || login == "" || password == "":
			// Логин в текст ошибки попадает, пароль — нет: сообщение уедет в
			// лог, а лог читают не только свои.
			return nil, fmt.Errorf("вход %q: нужно «логин:пароль»", login)
		case a.users[login].hash != nil:
			return nil, fmt.Errorf("логин %q повторяется", login)
		}

		salt := make([]byte, saltLen)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("соль для %q: %w", login, err)
		}
		hash, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
		if err != nil {
			return nil, fmt.Errorf("хеш пароля для %q: %w", login, err)
		}
		a.users[login] = cred{salt: salt, hash: hash}
	}

	if len(a.users) == 0 {
		return nil, fmt.Errorf("не задано ни одного входа")
	}
	if len(a.secret) == 0 {
		a.secret = make([]byte, 32)
		if _, err := rand.Read(a.secret); err != nil {
			return nil, fmt.Errorf("ключ подписи: %w", err)
		}
	}
	return a, nil
}

// Clock подменяет часы. Нужен проверкам: срок жизни пропуска иначе пришлось бы
// отсиживать по-настоящему.
func (a *Auth) Clock(f func() time.Time) { a.now = f }

// Managers объявляет, кто из заведённых входов — руководитель.
//
// Роль ровно одна, и она не про иерархию, а про журнал инцидентов: в нём лежат
// записи о работе конкретных людей, и читать их должен тот, кто оценивает, а не
// тот, кого оценивают. Всё остальное в реестре — задачи, срезы, источники —
// общее: реестр затевали как общую картину, и делить её незачем.
//
// Пустой список означает «все руководители». Умолчание выбрано так намеренно:
// опечатка в настройке не должна запирать человека снаружи его же инструмента.
// О том, что роли не разделены, сервер говорит при запуске.
func (a *Auth) Managers(list string) error {
	a.managers = make(map[string]bool)
	for _, login := range strings.Split(list, ",") {
		login = strings.TrimSpace(login)
		if login == "" {
			continue
		}
		if _, ok := a.users[login]; !ok {
			return fmt.Errorf("руководитель %q: такого входа нет", login)
		}
		a.managers[login] = true
	}
	return nil
}

// Manager отвечает, руководитель ли это. Пустой логин — нет: без пропуска роли
// не бывает.
func (a *Auth) Manager(login string) bool {
	if login == "" {
		return false
	}
	if len(a.managers) == 0 {
		return true
	}
	return a.managers[login]
}

// ManagersSet сообщает, разделены ли роли. Нужен для лога при запуске: «все
// видят журнал» человек должен узнать от сервера, а не из настроек.
func (a *Auth) ManagersSet() bool { return len(a.managers) > 0 }

// Logins перечисляет заведённые входы. Нужен для лога при запуске: человек
// должен видеть, кого сервер пустит, не заглядывая в настройки.
func (a *Auth) Logins() []string {
	out := make([]string, 0, len(a.users))
	for login := range a.users {
		out = append(out, login)
	}
	return out
}

// Weak перечисляет входы, у которых пароль совпадает с логином.
//
// Отдельный метод, потому что об этом надо сказать вслух при каждом запуске.
// Такой пароль подбирается первой же попыткой, и на публичном адресе это
// означает открытый доступ ко всему реестру.
func (a *Auth) Weak() []string {
	var out []string
	for login := range a.users {
		if a.Check(login, login) {
			out = append(out, login)
		}
	}
	return out
}

// Check проверяет пару «логин, пароль».
//
// Неизвестный логин обрабатывается так же долго, как известный: иначе по
// времени ответа можно перебрать список входов, не зная ни одного пароля.
func (a *Auth) Check(login, password string) bool {
	c, ok := a.users[strings.TrimSpace(login)]
	if !ok {
		// Считаем хеш от случайной соли и выбрасываем: работа та же, ответ
		// всегда отрицательный.
		c = cred{salt: make([]byte, saltLen), hash: make([]byte, keyLen)}
	}

	hash, err := pbkdf2.Key(sha256.New, password, c.salt, iterations, keyLen)
	if err != nil {
		return false
	}
	// Сравнение постоянного времени: обычное прекращается на первом
	// несовпавшем байте, и по времени ответа хеш подбирается побайтно.
	return ok && subtle.ConstantTimeCompare(hash, c.hash) == 1
}

// Issue выдаёт пропуск: «логин.срок.подпись».
//
// remember означает «это моё устройство»: пропуск живёт три месяца вместо
// суток. Срок зашит в самом пропуске и подписан вместе с логином — подделать
// его, продлив себе доступ, нельзя.
func (a *Auth) Issue(login string, remember bool) string {
	until := a.now().Add(Life(remember)).Unix()
	body := enc(login) + "." + strconv.FormatInt(until, 10)
	return body + "." + a.sign(body)
}

// Verify проверяет пропуск и возвращает логин.
//
// Проверяется и подпись, и срок, и существование входа. Последнее важно
// отдельно: удалённый из настроек человек не должен ходить по реестру до конца
// суток на выданном вчера пропуске.
func (a *Auth) Verify(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	body := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(a.sign(body)), []byte(parts[2])) {
		return "", false
	}

	until, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || a.now().Unix() >= until {
		return "", false
	}
	login, err := dec(parts[0])
	if err != nil {
		return "", false
	}
	if _, ok := a.users[login]; !ok {
		return "", false
	}
	return login, true
}

func (a *Auth) sign(body string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(body))
	return enc(string(mac.Sum(nil)))
}

func enc(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func dec(s string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return string(b), err
}
