package schema

import "sort"

// Схема ответа. Она описывает ровно то, что умеет проверить convert.go, и
// расходиться с ним не должна: схема просит, проверка решает, и лишнее поле в
// схеме означало бы обещание, которого разбор не выполнит.
//
// Схема собирается в Go, а не лежит строкой JSON, ради одной вещи: значение с
// происхождением встречается в ответе полтора десятка раз, и повторить его
// описание полтора десятка раз значило бы завести полтора десятка мест, где
// правило может разойтись.

// sortedKeys перечисляет поля схемы по алфавиту.
//
// Порядок устойчивый не ради красоты: схема входит в запрос, а запрос — в
// кэш промпта на стороне модели. Порядок обхода map в Go случаен от запуска к
// запуску, и без сортировки каждый запрос выглядел бы для кэша новым.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// valueSchema — значение с происхождением.
func valueSchema(what string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": what,
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "Значение словами. Пусто только при origin=missing.",
			},
			"origin": map[string]any{
				"type": "string",
				"enum": []string{"quoted", "derived", "missing"},
				"description": "quoted — сказано в источнике дословно; derived — следует из него; " +
					"missing — в источниках нет.",
			},
			"sourceId": map[string]any{
				"type":        "string",
				"description": "Номер источника из списка. Обязателен для quoted и derived.",
			},
			"quote": map[string]any{
				"type": "string",
				"description": "Дословный кусок источника, слово в слово. Обязателен для quoted; " +
					"сверяется программой.",
			},
			"note": map[string]any{
				"type": "string",
				"description": "Для derived — как получено значение. Для missing — что спросить, " +
					"чтобы узнать.",
			},
		},
		"required":             []string{"text", "origin"},
		"additionalProperties": false,
	}
}

func listOf(item map[string]any, what string) map[string]any {
	return map[string]any{"type": "array", "description": what, "items": item}
}

func dateSchema(what string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": what + " в виде ГГГГ-ММ-ДД. Пустая строка, если дата не названа.",
	}
}

func Properties() map[string]any {
	return map[string]any{
		"stage":        valueSchema("Этап задачи: не начато, в работе, на проверке, заблокировано, готово."),
		"goalAsStated": valueSchema("Цель словами постановщика, как она сформулирована в задаче."),
		"goalClarified": valueSchema("Цель после уточнений в переписке. " +
			"Расхождение с первой формулировкой — сам по себе важный вывод."),
		"outOfScope": listOf(valueSchema("Что в объём работ не входит."), "Что в объём работ не входит."),
		"done": listOf(
			valueSchema("Одна сделанная работа, короткой фразой с глаголом."),
			"Что уже сделано. Один пункт — одно дело: перечисление через запятую разбей на пункты."),
		"left": listOf(
			valueSchema("Одна оставшаяся работа, короткой фразой с глаголом."),
			"Что осталось сделать. Один пункт — одно дело. Не пиши сюда то, что дальше по "+
				"переписке уже сделано или о чём уже договорились: этому место в done."),

		"criteria": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"n":    map[string]any{"type": "integer", "description": "Номер по порядку."},
				"text": map[string]any{"type": "string", "description": "Как заказчик поймёт, что работа сделана."},
				"met":  map[string]any{"type": "boolean", "description": "Выполнен ли критерий."},
				"note": map[string]any{"type": "string", "description": "Пояснение, если нужно."},
			},
			"required":             []string{"n", "text", "met"},
			"additionalProperties": false,
		}, "Критерии приёмки."),

		"milestones": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string", "description": "Название этапа."},
				"due":   dateSchema("Срок этапа"),
				"progress": map[string]any{
					"type": "number", "minimum": 0, "maximum": 1,
					"description": "Доля закрытых работ этапа, от 0 до 1: раздели этап на шаги и " +
						"посчитай, три из четырёх — 0.75. Не занижай на всякий случай — из этих " +
						"долей программа считает готовность задачи, которую назовут заказчику.",
				},
				"weight": map[string]any{
					"type": "number", "minimum": 0,
					"description": "Объём этапа относительно других: 1 — обычный, 3 — втрое " +
						"больше работы, 0.5 — вдвое меньше. Этапы почти никогда не равны, и " +
						"готовность считается с этим весом: короткий урок не должен весить " +
						"столько же, сколько настройка всех рабочих мест.",
				},
				"evidence": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Чем подтверждается выполнение.",
				},
			},
			"required":             []string{"title", "progress", "weight"},
			"additionalProperties": false,
		}, "Этапы плана с их выполнением."),

		"shifts": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"at":      dateSchema("Когда о переносе договорились"),
				"from":    dateSchema("Прежний срок"),
				"to":      dateSchema("Новый срок"),
				"comment": map[string]any{"type": "string", "description": "Чем объяснили перенос. Пусто — не объяснили."},
			},
			"required":             []string{"from", "to"},
			"additionalProperties": false,
		}, "Переносы срока, найденные в источниках."),

		"blockers": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "description": "Что мешает, одной фразой."},
				"kind": map[string]any{
					"type": "string",
					"enum": []string{"no_info", "no_access", "dependency", "technical", "no_approach", "no_time"},
					"description": "Вид блокера. Он важнее описания: от него зависит, к кому идти, " +
						"чтобы блокер снять.",
				},
				"dependsOn": map[string]any{"type": "string", "description": "От кого или чего зависит снятие."},
				"since":     dateSchema("С какого дня висит"),
				"evidence":  valueSchema("Чем подтверждается блокер."),
			},
			"required":             []string{"summary", "kind", "evidence"},
			"additionalProperties": false,
		}, "Блокеры: то, из-за чего работа стоит."),

		"risks": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary":    map[string]any{"type": "string", "description": "Риск одной фразой."},
				"daysImpact": map[string]any{"type": "integer", "minimum": 0, "description": "Во сколько дней срока обойдётся, если сбудется."},
				"spread":     map[string]any{"type": "string", "description": "На что ещё влияет."},
				"evidence":   valueSchema("Чем подтверждается риск."),
			},
			"required":             []string{"summary", "evidence"},
			"additionalProperties": false,
		}, "Риски: то, что может помешать."),

		"questions": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"n":       map[string]any{"type": "integer", "description": "Номер по порядку."},
				"text":    map[string]any{"type": "string", "description": "Вопрос специалисту."},
				"unlocks": map[string]any{"type": "string", "description": "Что даст ответ."},
				"Answer":  valueSchema("Ответ, если он есть в источниках. Иначе origin=missing."),
			},
			"required":             []string{"n", "text", "Answer"},
			"additionalProperties": false,
		}, "Вопросы, ответы на которые нужны для полноты среза."),

		"artifacts": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":    map[string]any{"type": "string", "description": "Имя файла или документа."},
				"present": map[string]any{"type": "boolean", "description": "Приложен ли он на самом деле."},
				"bytes":   map[string]any{"type": "integer", "minimum": 0, "description": "Размер, если известен."},
				"wouldGive": map[string]any{
					"type":        "string",
					"description": "Что дал бы этот файл, если его получить.",
				},
			},
			"required":             []string{"name", "present"},
			"additionalProperties": false,
		}, "Файлы, упомянутые в источниках. Названный, но не приложенный файл — тоже запись."),

		"pmActions": listOf(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"approve", "access", "connect", "clarify", "escalate", "none"},
					"description": "Что именно требуется от руководителя проекта.",
				},
				"text": map[string]any{
					"type": "string",
					"description": "Одно действие короткой фразой с глаголом. Не проси того, о чём " +
						"дальше по переписке уже договорились.",
				},
				"why": map[string]any{"type": "string", "description": "Что это разблокирует."},
			},
			"required":             []string{"kind", "text"},
			"additionalProperties": false,
		}, "Что нужно от руководителя проекта, чтобы работа пошла дальше. Одно действие — один пункт."),
	}
}

// ToolName — имя инструмента, которым модель обязана ответить.
//
// Ответ идёт инструментом, а не текстом. Текстовый ответ пришлось бы вырезать
// из markdown-обёртки и разбирать на удачу, а разбор на удачу ошибается ровно
// тогда, когда модель написала что-то непривычное, — то есть в самом
// интересном случае.
const ToolName = "srez"

// ToolDescription — что делает инструмент, словами для модели.
const ToolDescription = "Вернуть разбор источников задачи."

// Required перечисляет обязательные поля ответа.
//
// Обязательны все разделы, включая списки. Пустой список — это утверждение
// «ничего не нашлось», а пропущенный раздел — молчание, и различать их важнее,
// чем экономить на длине ответа.
func Required() []string { return sortedKeys(Properties()) }

// JSONSchema — схема ответа целиком, как её ждут и Anthropic, и
// OpenAI-совместимые шлюзы: у обоих это обычный JSON Schema объекта.
func JSONSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           Properties(),
		"required":             Required(),
		"additionalProperties": false,
	}
}
