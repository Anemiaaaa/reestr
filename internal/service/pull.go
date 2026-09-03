package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
)

// pullPageLimit — сколько сообщений просить у портала за один вызов. Больше
// сотни портал всё равно не отдаёт.
const pullPageLimit = 100

// pullMaxPages — предел страниц за одну подтяжку.
//
// Ограничение нужно не ради скорости, а чтобы цикл был конечным при любом
// поведении портала. Если курсор по какой-то причине перестанет двигаться —
// портал сменил семантику FIRST_ID, вернул то же самое, отдал сообщение с
// меньшим номером, — то без предела мы получили бы вечный цикл походов в чужой
// сервис. Сто страниц по сотне сообщений это десять тысяч за раз: больше, чем
// бывает в чате задачи, и достаточно, чтобы первая подтяжка забрала историю
// целиком.
const pullMaxPages = 100

// PullResult — итог подтяжки одного чата.
//
// Отдельно Fetched и Added, потому что это разные ответы на разные вопросы.
// Fetched говорит, сколько портал отдал; Added — сколько из них реестр видит
// впервые. Расхождение между ними нормально: портал вправе вернуть
// перекрывающийся кусок. А вот Fetched без Added раз за разом означает, что
// курсор стоит на месте, и это уже видно по числам.
type PullResult struct {
	DialogID string
	Title    string

	Fetched int
	Added   int

	// LastMessageID — курсор после подтяжки. Совпадает с прежним, если нового
	// ничего не пришло.
	LastMessageID int
}

// PullChats переносит в реестр новые сообщения всех чатов, закреплённых за
// задачей.
//
// Порядок действий здесь важнее самих действий: сначала прочитать от курсора,
// потом записать сообщения, и только потом сдвинуть курсор. Курсор, уехавший
// вперёд раньше записи, — это переписка, потерянная молча: следующая подтяжка
// начнёт с номера, до которого ничего не сохранено, и узнать об этом будет
// неоткуда.
//
// Без настроенного портала — ErrInvalid, а не пустой список: подтяжку запускает
// человек нажатием, и молчаливое «ничего не произошло» он прочитает как «новых
// сообщений нет».
func (s *Service) PullChats(ctx context.Context, taskID string) ([]PullResult, error) {
	if !s.PortalConfigured() {
		return nil, fmt.Errorf("Bitrix24 не настроен: %w", ErrInvalid)
	}

	links, err := s.TaskChats(ctx, taskID)
	if err != nil {
		return nil, err
	}

	out := make([]PullResult, 0, len(links))
	for _, link := range links {
		// Чужие системы пропускаем молча: связь с телеграмом читать нечем, но и
		// ошибкой это не является — придёт свой клиент, придёт своя подтяжка.
		if link.System != domain.SystemBitrix {
			continue
		}
		res, err := s.pullChat(ctx, link)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// pullChat переносит новые сообщения одного закреплённого чата.
func (s *Service) pullChat(ctx context.Context, link domain.ChatLink) (PullResult, error) {
	res := PullResult{
		DialogID:      link.DialogID,
		Title:         link.Title,
		LastMessageID: link.LastMessageID,
	}

	for page := 0; page < pullMaxPages; page++ {
		msgs, err := s.portal.Messages(ctx, link.DialogID, res.LastMessageID, pullPageLimit)
		if err != nil {
			return PullResult{}, err
		}
		if len(msgs) == 0 {
			break
		}
		res.Fetched += len(msgs)

		raw := make([]domain.RawMessage, 0, len(msgs))
		highest := res.LastMessageID
		for _, m := range msgs {
			raw = append(raw, s.rawMessage(link.DialogID, m))
			if m.ID > highest {
				highest = m.ID
			}
		}

		added, err := s.store.AddRawMessages(ctx, raw)
		if err != nil {
			return PullResult{}, err
		}
		res.Added += added

		// Курсор двигается только теперь, когда сообщения уже в хранилище, и
		// только вперёд. Портал, отдавший сообщение с меньшим номером, курсор не
		// откатывает: откат привёл бы всю переписку вторым экземпляром.
		if highest <= res.LastMessageID {
			break
		}
		res.LastMessageID = highest

		// Неполная страница — конец чата. Просить следующую незачем: она придёт
		// пустой, но поход в портал уже состоится.
		if len(msgs) < pullPageLimit {
			break
		}
	}

	// Курсор сдвигается даже когда нового не пришло: время последнего похода —
	// не то же самое, что «не ходили вовсе», и AdvanceChatCursor пишет обе
	// величины разом.
	err := s.store.AdvanceChatCursor(ctx, link.TaskID, link.System, link.DialogID,
		res.LastMessageID, s.now())
	if err != nil {
		return PullResult{}, err
	}

	s.log.Info("сообщения перенесены",
		"задача", link.TaskID, "чат", link.DialogID,
		"получено", res.Fetched, "новых", res.Added, "курсор", res.LastMessageID)
	return res, nil
}

// rawMessage переводит сообщение портала в запись реестра.
//
// Замазка здесь не повторяется: её уже сделал клиент, и Redacted говорит, что
// она сработала. Повторный проход по тексту ничего бы не нашёл, а вот признак
// «текст не дословный» обязан доехать до хранилища — срез может сослаться на
// этот текст, и читатель вправе знать.
func (s *Service) rawMessage(dialogID string, m bitrix.Message) domain.RawMessage {
	id := strconv.Itoa(m.ID)
	return domain.RawMessage{
		External: domain.ExternalRef{
			System:    domain.SystemBitrix,
			ChatID:    dialogID,
			MessageID: id,
			URL:       s.portal.MessageURL(dialogID, id),
		},
		Author:     m.Author,
		AuthorID:   m.AuthorID,
		Body:       m.Text,
		OccurredAt: m.Date,
		FetchedAt:  s.now(),
		Service:    m.Service,
		Redacted:   m.Redacted,
	}
}

// ChatMessages возвращает перенесённые сообщения всех чатов задачи, свежие
// последними.
//
// Спрашивается по задаче, хотя хранятся сообщения по чату: наружу удобнее
// задача, а собрать по её чатам — работа сервиса, а не вызывающего.
func (s *Service) ChatMessages(ctx context.Context, taskID string) ([]domain.RawMessage, error) {
	links, err := s.TaskChats(ctx, taskID)
	if err != nil {
		return nil, err
	}

	var out []domain.RawMessage
	for _, link := range links {
		msgs, err := s.store.RawMessages(ctx, link.System, link.DialogID)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
	}
	return out, nil
}
