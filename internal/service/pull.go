package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Anemiaaaa/reestr/internal/bitrix"
	"github.com/Anemiaaaa/reestr/internal/domain"
	"github.com/Anemiaaaa/reestr/internal/store"
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

	// SourceID — источник, заведённый из перенесённой переписки. Пусто, если
	// заводить было не из чего.
	SourceID string
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

	// Перенесённое за эту подтяжку копится целиком: источник заводится один на
	// подтяжку, а не один на страницу. Страница — деталь разговора с порталом, и
	// делить по ней материал значило бы показывать человеку границы, которых в
	// переписке нет.
	var moved []domain.RawMessage

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
		moved = append(moved, raw...)

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

	// Источник заводится после курсора, а не до: пока курсор не сдвинут,
	// подтяжка не закончена, и материал, выставленный разбору раньше времени,
	// пришлось бы отзывать — а источники не отзываются.
	if res.SourceID, err = s.sourceFrom(ctx, link, moved); err != nil {
		return PullResult{}, err
	}

	s.log.Info("сообщения перенесены",
		"задача", link.TaskID, "чат", link.DialogID,
		"получено", res.Fetched, "новых", res.Added, "курсор", res.LastMessageID,
		"источник", res.SourceID)
	return res, nil
}

// sourceFrom заводит из перенесённой переписки один источник.
//
// Один на подтяжку, а не один на сообщение. Сообщений в чате сотни, и каждое
// отдельным источником превратило бы список источников задачи в ленту чата —
// ровно ту перегруженность, на которую жаловался заказчик. Разбору же удобнее
// читать переписку подряд: реплика в отрыве от соседних чаще всего непонятна.
//
// Системные сообщения портала в источник не попадают. «Задача завершена» и
// «изменён исполнитель» цитировать не в чем: в срезе от них нет ни факта, ни
// цитаты, а разбор они заваливают шумом. В raw_messages они при этом остаются —
// журнал переписки обязан быть полным.
func (s *Service) sourceFrom(ctx context.Context, link domain.ChatLink, msgs []domain.RawMessage) (string, error) {
	usable := make([]domain.RawMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Usable() {
			usable = append(usable, m)
		}
	}
	if len(usable) == 0 {
		return "", nil
	}

	last := usable[len(usable)-1]
	src := domain.Source{
		ID:     newID("s"),
		TaskID: link.TaskID,
		Kind:   domain.KindCorrespondence,
		Title:  chatTitle(link) + ", сообщения " + msgRange(usable),
		Body:   messagesText(usable),
		// Дата события — дата последнего сообщения: в хронологии эта порция
		// переписки стоит там, где закончилась, а не там, где началась.
		OccurredAt: last.OccurredAt,
		UploadedAt: s.now(),
		// Адрес оригинала — последнее сообщение порции. Он же ключ, по которому
		// повторная подтяжка не заведёт второй такой же источник: курсор не
		// откатывается, значит и ключ не повторится.
		External: last.External,
	}

	if err := s.store.AddSource(ctx, src); err != nil {
		// Источник с таким адресом уже есть — значит эту порцию уже переносили.
		// Это не сбой: подтяжку могли запустить дважды подряд.
		if errors.Is(err, store.ErrExists) {
			s.log.Info("переписка уже была заведена источником",
				"задача", link.TaskID, "чат", link.DialogID)
			return "", nil
		}
		return "", err
	}
	return src.ID, nil
}

// chatTitle — подпись чата для названия источника.
func chatTitle(link domain.ChatLink) string {
	if title := strings.TrimSpace(link.Title); title != "" {
		return "Переписка: " + title
	}
	return "Переписка в чате " + link.DialogID
}

// msgRange — диапазон номеров сообщений, чтобы по названию источника было видно,
// какая именно часть чата в нём лежит.
func msgRange(msgs []domain.RawMessage) string {
	first, last := msgs[0].External.MessageID, msgs[len(msgs)-1].External.MessageID
	if first == last {
		return "№" + first
	}
	return "№" + first + "–№" + last
}

// messagesText складывает переписку в текст, пригодный и для разбора, и для
// чтения человеком.
//
// Формат намеренно простой: дата, автор, текст. Разбор будет цитировать отсюда
// дословно, и всякое украшательство — рамки, отступы, разметка — попадёт в
// цитату и не сойдётся с источником при проверке.
func messagesText(msgs []domain.RawMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if !m.OccurredAt.IsZero() {
			b.WriteString(domain.FormatDate(m.OccurredAt))
			b.WriteString(", ")
		}
		if m.Author != "" {
			b.WriteString(m.Author)
			b.WriteString(": ")
		}
		b.WriteString(m.Body)
	}
	return b.String()
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

// SourceFromPortalTask заводит источник из описания задачи портала.
//
// Описание — это постановка задачи словами автора: состав работ, суммы,
// договорённости. Материал не хуже переписки, а прав на него нужно меньше:
// хватает того же права на задачи, тогда как чат требует отдельного права на
// сообщения. На портале, где чаты закрыты, это единственный автоматический
// материал, который вообще можно получить.
//
// Пустое описание источника не даёт: пустой источник в списке — обещание
// материала, которого нет.
func (s *Service) SourceFromPortalTask(ctx context.Context, taskID string, portal bitrix.Task) (string, error) {
	body := strings.TrimSpace(portal.Description)
	if body == "" {
		return "", nil
	}

	src := domain.Source{
		ID:     newID("s"),
		TaskID: taskID,
		// Вид «ТЗ», а не «переписка»: это то, что заказчику пообещали, а не то,
		// что обсуждали по дороге.
		Kind:       domain.KindSpec,
		Title:      "Постановка задачи Bitrix24 №" + portal.ID,
		Body:       body,
		Author:     portal.Author,
		OccurredAt: portal.CreatedAt,
		UploadedAt: s.now(),
		// Адрес оригинала — сама задача портала. Он же ключ: повторное
		// закрепление той же задачи второго такого источника не заведёт.
		External: domain.ExternalRef{
			System:    domain.SystemBitrix,
			ChatID:    "task",
			MessageID: portal.ID,
		},
	}

	if err := s.store.AddSource(ctx, src); err != nil {
		if errors.Is(err, store.ErrExists) {
			return "", nil
		}
		return "", err
	}
	s.log.Info("постановка задачи портала заведена источником",
		"задача", taskID, "задача портала", portal.ID, "знаков", len(body))
	return src.ID, nil
}
