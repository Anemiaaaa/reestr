// Package store описывает доступ к данным реестра.
//
// Интерфейс здесь, реализации — в подпакетах. Сервис работает только с
// интерфейсом, поэтому замена файла на SQL не заставит его переписывать: это
// как раз тот случай, когда абстракция оправдана — выбор хранилища ещё не
// сделан, а работать надо уже сегодня.
//
// Правило хранилища повторяет правило домена: источники и факты только
// добавляются. Методов Update и Delete нет намеренно, а не по недосмотру.
package store

import (
	"context"
	"errors"

	"github.com/Anemiaaaa/reestr/internal/domain"
)

// ErrNotFound возвращается, когда записи с таким идентификатором нет.
var ErrNotFound = errors.New("не найдено")

// ErrExists возвращается при попытке создать запись с занятым
// идентификатором.
var ErrExists = errors.New("уже существует")

// Store — хранилище реестра.
type Store interface {
	// Empty сообщает, что в реестре нет ни одной задачи. Нужен на старте: пустой
	// реестр можно наполнить примером, непустой трогать нельзя.
	Empty(ctx context.Context) (bool, error)

	// CreateTask сохраняет новую задачу. Если идентификатор занят, возвращает
	// ErrExists.
	CreateTask(ctx context.Context, t domain.Task) error

	// Task возвращает задачу по идентификатору.
	Task(ctx context.Context, id string) (domain.Task, error)

	// Tasks возвращает все задачи, свежие первыми.
	Tasks(ctx context.Context) ([]domain.Task, error)

	// AddSource добавляет источник. Источник без задачи допустим: сводка с
	// капитанского мостика говорит о нескольких задачах сразу, и фрагменты из
	// неё ссылаются на неё через ParentID.
	//
	// Если у источника заполнен внешний адрес и запись с таким адресом уже
	// есть, возвращает ErrExists: повторная подтяжка чата не должна заводить
	// второй экземпляр того же сообщения.
	AddSource(ctx context.Context, s domain.Source) error

	// Source возвращает источник по идентификатору.
	Source(ctx context.Context, id string) (domain.Source, error)

	// Sources возвращает источники задачи хроникой: по дате материала, а при
	// равных датах — в порядке поступления. Материал без даты идёт первым: его
	// дату ещё предстоит выяснить, и среди свежего он выглядел бы как самое
	// позднее событие.
	//
	// Общие источники, не привязанные к задаче, сюда не попадают: в задаче
	// участвует вырезанный из них фрагмент, а не сводка целиком.
	Sources(ctx context.Context, taskID string) ([]domain.Source, error)

	// SourceByExternal возвращает источник по адресу оригинала во внешней
	// системе — ключу domain.ExternalRef.Key. Пустой ключ всегда даёт
	// ErrNotFound: искать дубль не по чему.
	SourceByExternal(ctx context.Context, key string) (domain.Source, error)

	// SlicesUsing возвращает версии срезов, которые ссылаются на источник, от
	// старых к свежим. Отвечает на вопрос «на что этот материал повлиял»: без
	// него загруженный кусок переписки — запись в журнале, о судьбе которой
	// ничего не известно.
	SlicesUsing(ctx context.Context, sourceID string) ([]domain.SliceRef, error)

	// AddFacts добавляет извлечённые факты. Существующие факты не заменяются:
	// новая версия значения просто ложится сверху.
	//
	// Все задачи, упомянутые в наборе, должны существовать, иначе ErrNotFound и
	// не записано ничего: набор фактов — результат одного разбора, и половина
	// разбора в хранилище хуже, чем ни одного.
	AddFacts(ctx context.Context, facts []domain.Fact) error

	// Facts возвращает факты задачи в порядке добавления.
	Facts(ctx context.Context, taskID string) ([]domain.Fact, error)

	// PutProcess сохраняет схему процесса, заменяя прежнюю схему того же вида.
	// Схема — не факт, а его отображение, и пересобирается целиком. Задача
	// должна существовать, иначе ErrNotFound.
	PutProcess(ctx context.Context, p domain.Process) error

	// Processes возвращает схемы задачи: «как есть» и «как будет».
	Processes(ctx context.Context, taskID string) ([]domain.Process, error)

	// SaveSlice сохраняет собранный срез как очередную версию. Если версия с
	// таким номером уже есть, возвращает ErrExists: собранная версия не
	// переписывается, иначе сравнение «было → стало» потеряло бы смысл. Задача
	// должна существовать, иначе ErrNotFound.
	SaveSlice(ctx context.Context, s domain.Slice) error

	// LatestSlice возвращает последнюю собранную версию среза.
	LatestSlice(ctx context.Context, taskID string) (domain.Slice, error)

	// NextSliceVersion возвращает номер, который получит следующая сборка.
	NextSliceVersion(ctx context.Context, taskID string) (int, error)
}
