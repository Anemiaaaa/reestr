package pgstore

import (
	"context"
	"os"
	"testing"

	"github.com/Anemiaaaa/reestr/internal/store"
	"github.com/Anemiaaaa/reestr/internal/store/storetest"
)

// dsnEnv — отдельная переменная окружения, намеренно не DATABASE_URL.
//
// Разница здесь не в удобстве. Проверка начинает каждую подпроверку с очистки
// всех таблиц, и если бы она подхватывала ту же переменную, с которой запускают
// сервис, обычный `go test ./...` на рабочей машине вытер бы рабочий реестр.
// Чтобы это случилось, переменную нужно назвать отдельно и осознанно.
const dsnEnv = "REESTR_TEST_DSN"

// TestConformance прогоняет по базе тот же контракт, что и по файлу. Смысл
// именно в общем наборе: пока у каждого хранилища был свой, расхождения между
// ними никак себя не проявляли — ни одно из них не было неправым в своих
// собственных проверках.
//
// Без базы проверка пропускается, а не падает: сборка не должна требовать
// поднятого PostgreSQL, иначе она станет невыполнимой на чужой машине.
func TestConformance(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("переменная %s не задана — хранилище в базе не проверяется", dsnEnv)
	}

	st, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(st.Close)

	// Схема применяется один раз на весь прогон, а чистое состояние даёт
	// очистка: пересоздавать таблицы перед каждой подпроверкой значило бы
	// проверять DDL, а не поведение хранилища.
	storetest.Run(t, func(t *testing.T) store.Store {
		t.Helper()

		truncate(t, st)
		return st
	})
}

// truncate возвращает базу в состояние сразу после создания схемы.
//
// RESTART IDENTITY не косметика: seq — второй ключ сортировки задач и
// источников, и без сброса счётчиков порядок в подпроверке зависел бы от того,
// сколько записей прошло до неё.
func truncate(t *testing.T, s *Store) {
	t.Helper()

	// Одним выражением, потому что таблицы связаны внешними ключами: PostgreSQL
	// требует перечислить все зависимые таблицы в том же TRUNCATE. Список растёт
	// вместе со схемой: новая таблица со ссылкой на tasks, забытая здесь, валит
	// весь прогон на очистке, а не на проверяемом поведении.
	const q = `TRUNCATE slice_sources, slices, processes, facts, sources, raw_messages, chat_links, incidents, tasks RESTART IDENTITY`

	if _, err := s.pool.Exec(context.Background(), q); err != nil {
		t.Fatalf("очистка базы: %v", err)
	}
}
