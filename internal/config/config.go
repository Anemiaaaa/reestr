// Пакет config читает локальные настройки из файла .env.
//
// Своя реализация вместо библиотеки: нужно ровно одно — строки вида «КЛЮЧ=
// значение». Зависимость ради тридцати строк добавила бы в проект чужой код,
// который придётся обновлять, и ничего не упростила бы.
//
// Порядок важности: переменная окружения сильнее файла. Файл — это удобство
// для локальной работы, а окружение — то, чем настраивают запуск на сервере, и
// перебивать его файлом из рабочего каталога нельзя.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Load читает .env и переносит из него в окружение только те ключи, которых
// там ещё нет. Отсутствие файла ошибкой не считается: настройки могут прийти
// и целиком из окружения.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("чтение настроек %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("%s, строка %d: нет знака равенства", path, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("%s, строка %d: пустое имя настройки", path, line)
		}

		// Кавычки снимаются, если обрамляют значение целиком: пароль может
		// содержать пробел, и тогда кавычки — единственный способ его записать.
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' ||
			value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}

		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s, строка %d: %w", path, line, err)
		}
	}
	return sc.Err()
}

// Env возвращает значение настройки или подставляет запасное.
func Env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
