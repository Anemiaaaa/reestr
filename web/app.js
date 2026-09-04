// Интерфейс реестра.
//
// Здесь нет ни одного вычисления над данными задачи. Сервер присылает готовые
// строки — проценты, дни, склонения, — а этот файл раскладывает их по разметке.
// Единственные числа, которые он трогает, это доли для ширины полос и координаты
// в сетке схемы: ширину и положение словами не задать.
//
// Текст источников и цитаты подставляются только через textContent. Это чужой
// текст — переписка и аудит, — и разметкой он быть не должен.

"use strict";

// --- разметка ---

function $(sel) {
  return document.querySelector(sel);
}

// el собирает элемент. Свойства: class, text, style, hidden, on* — обработчик,
// остальное уходит в атрибуты. Значения null, undefined и false пропускаются,
// поэтому условные атрибуты пишутся выражением, а не ветвлением.
function el(tag, props, ...kids) {
  const node = document.createElement(tag);
  for (const [key, v] of Object.entries(props || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (key === "class") node.className = v;
    else if (key === "text") node.textContent = v;
    else if (key === "style") node.setAttribute("style", v);
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), v);
    else node.setAttribute(key, v === true ? "" : v);
  }
  put(node, kids);
  return node;
}

function put(node, kids) {
  for (const kid of kids.flat(Infinity)) {
    if (kid === null || kid === undefined || kid === false) continue;
    node.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
}

// --- сервер ---

async function api(path, init) {
  const res = await fetch(path, {
    headers: init && init.body ? { "Content-Type": "application/json" } : undefined,
    ...init,
  });
  const text = await res.text();

  let body = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      // Сервер ответил не JSON — покажем код состояния.
    }
  }
  if (!res.ok) {
    throw new Error((body && body.error) || res.status + " " + res.statusText);
  }
  return body;
}

const state = {
  tasks: [],
  current: "",
  board: null,
  tab: "slice",
};

// --- значение с происхождением ---

// val рисует значение и пометку рядом. По пометке раскрывается то, на чём
// значение держится: цитата, источник, пояснение. Пока не раскрыли — на экране
// только значение, и оно не тонет в служебных подписях.
function val(v) {
  const box = el("div", {});
  box.append(el("span", { class: v.known ? null : "miss", text: v.text }));

  const backing = v.quote || v.note || v.sourceTitle;
  if (!backing) {
    box.append(el("span", { class: "mark mark--" + v.origin, text: v.mark, title: v.originLabel }));
    return box;
  }

  const quote = el("div", { class: "quote", hidden: true });
  if (v.sourceTitle) {
    quote.append(el("span", { class: "quote__src", text: v.originLabel + " · " + v.sourceTitle }));
  }
  if (v.quote) {
    quote.append(document.createTextNode("«" + v.quote + "»"));
  }
  if (v.note) {
    quote.append(el("span", { class: "quote__note", text: v.note }));
  }

  const btn = el("button", {
    class: "mark mark--" + v.origin,
    type: "button",
    title: v.originLabel + (v.sourceTitle ? " · " + v.sourceTitle : ""),
    "aria-expanded": "false",
    text: v.mark,
    onclick: () => {
      quote.hidden = !quote.hidden;
      btn.setAttribute("aria-expanded", String(!quote.hidden));
    },
  });

  box.append(btn, quote);
  return box;
}

function field(label, v) {
  return el("div", { class: "row" },
    el("div", { class: "row__label", text: label }),
    el("div", { class: "row__value" }, val(v)));
}

function sec(n, title, note, ...kids) {
  const head = el("div", { class: "sec__head" });
  if (n) head.append(el("span", { class: "sec__n", text: n }));
  head.append(el("span", { class: "sec__title", text: title }));
  if (note) head.append(el("span", { class: "sec__note", text: note }));

  return el("section", { class: "sec" }, head, kids);
}

// bar рисует полосу по доле 0…1. Подпись к полосе всегда приходит отдельно и
// строкой: здесь только ширина.
function bar(share, slim) {
  return el("div", { class: slim ? "bar bar--slim" : "bar" },
    el("div", { class: "bar__fill", style: "width:" + share * 100 + "%" }));
}

// --- боковая колонка ---

function drawTasks() {
  const list = $("#tasks");
  list.replaceChildren();

  for (const t of state.tasks) {
    const meta = [t.project, t.deadlineNote].filter(Boolean).join(" · ");
    list.append(el("button", {
      class: "item",
      type: "button",
      "aria-current": String(t.id === state.current),
      onclick: () => open(t.id),
    },
      el("div", { class: "item__title", text: t.title }),
      el("div", { class: t.overdue ? "item__meta item__meta--warn" : "item__meta", text: meta })));
  }
}

// --- сводка ---

function cell(label, value, warn) {
  return el("div", { class: "head__cell" },
    el("div", { class: "head__label", text: label }),
    el("div", { class: warn ? "head__value head__value--warn" : "head__value" }, value));
}

function drawHead(h) {
  const box = el("div", { class: "head" });

  box.append(cell("Этап", val(h.stage)));

  const readiness = el("div", {}, val(h.readiness), bar(h.readinessShare));
  box.append(el("div", { class: "head__cell head__cell--wide" },
    el("div", { class: "head__label", text: "Готовность" }),
    el("div", { class: "head__value" }, readiness)));

  if (h.deadline) {
    box.append(cell("Срок",
      [h.deadline, h.deadlineNote ? el("div", { class: "crumb", text: h.deadlineNote }) : null],
      h.overdue));
  }
  if (h.budget) box.append(cell("Бюджет", h.budget));
  box.append(cell("Срез", ["v" + h.version, el("div", { class: "crumb", text: h.builtAt })]));
  return box;
}

function drawChats(list) {
  if (!list || !list.length) return null;
  // Подпись собирается из готовых кусков сервера: и «задача Bitrix24 №4», и
  // «сообщения до №46» приходят строками, чтобы не форматировать их здесь
  // второй раз.
  const line = list.map(c => {
    const notes = [c.taskRef, c.syncText].filter(Boolean);
    return notes.length ? c.label + " (" + notes.join(", ") + ")" : c.label;
  }).join(" · ");
  return el("div", { class: "crumb crumb--chat" }, "чат: " + line);
}

// drawWarns рисует предупреждения сводки.
//
// Пять цветных полос подряд кричали одинаково громко, и от этого не значило
// ничего ни одно. Красное — то, из-за чего работа стоит, — остаётся на виду;
// остальное сворачивается в строку и раскрывается по нажатию. Это ответ на
// замечание заказчика о перегруженности, а не украшательство: убрать нельзя,
// потому что в свёрнутом виде пробелы и переносы всё равно посчитаны и названы.
function drawWarns(h) {
  const rows = [
    ["!", h.overdueText, "warn warn--red", true],
    ["!", h.blockerText, "warn warn--red", true],
    ["?", h.gapsText, "warn", false],
    ["↔", h.shiftsText, "warn", false],
    ["✓", h.criteriaText, "warn warn--green", false],
  ].filter(([, text]) => text);

  if (!rows.length) return null;

  const line = ([sign, text, cls]) =>
    el("div", { class: cls }, el("span", { class: "warn__mark", text: sign }), el("span", { text }));

  const loud = rows.filter(r => r[3]);
  const quiet = rows.filter(r => !r[3]);

  const box = el("div", { class: "warns" }, loud.map(line));
  if (!quiet.length) return box;

  const rest = el("div", { class: "warns", hidden: true }, quiet.map(line));
  const more = el("button", {
    class: "warns__more",
    type: "button",
    "aria-expanded": "false",
    // Подпись без числительного с существительным: падежи считает сервер, а
    // тут их согласовать нечем — «ещё 3 замечания» и «ещё 1 замечание»
    // потребовали бы правил склонения в браузере.
    text: "Показать остальные (" + quiet.length + ")",
    onclick: () => {
      rest.hidden = !rest.hidden;
      more.setAttribute("aria-expanded", String(!rest.hidden));
      more.hidden = !rest.hidden;
    },
  });

  box.append(more, rest);
  return box;
}

// --- срез ---

function ticks(items) {
  return el("ul", { class: "ticks" }, items);
}

function tick(box, met, body, note) {
  return el("li", { class: met ? "tick tick--met" : "tick" },
    el("span", { class: met ? "tick__box tick__box--met" : "tick__box", text: box }),
    el("div", {}, body, note ? el("div", { class: "tick__note", text: note }) : null));
}

// spec рисует справочную сетку «подпись — значение».
//
// Паспорт задачи это справка: имена, даты, номера. Строками во всю ширину она
// читалась как форма, которую заполнили и забыли, — глаз проскакивал раздел, не
// найдя в нём текста. Плотная сетка честно говорит «это справка» и умещается в
// один взгляд.
function spec(pairs) {
  return el("div", { class: "spec" }, pairs.map(([label, v]) =>
    el("div", {},
      el("div", { class: "spec__label", text: label }),
      el("div", { class: "spec__value" }, val(v)))));
}

// lead рисует то, что читают целиком: набор как у текста, а не как у поля.
function lead(label, v) {
  return el("div", { class: "lead" },
    el("div", { class: "lead__label", text: label }),
    el("div", { class: "lead__text" }, val(v)));
}

function sub(text) {
  return el("div", { class: "sub", text });
}

function drawPassport(p) {
  // Названия здесь нет намеренно: оно стоит заголовком страницы, и повторять
  // его строкой значит начинать раздел с того, что читатель только что прочёл.
  // Пометка происхождения названия при этом не теряется — она у заголовка.
  const rows = spec([
    ["Автор постановки", p.author],
    ["Исполнитель", p.assignee],
    ["Поставлена", p.openedAt],
    ["Срок", p.deadline],
  ]);

  if (!p.shifts.length) return sec("1", "Паспорт задачи", null, rows);

  const list = el("ul", { class: "list" }, p.shifts.map(s => {
    const moved = [s.from, s.to].filter(Boolean).join(" → ") || "перенос";
    return el("li", { class: s.explained ? "card" : "card card--warn" },
      el("div", { class: "card__top" },
        el("span", { class: "card__title", text: moved }),
        el("span", { class: s.explained ? "tag" : "tag tag--amber", text: s.explained ? "объяснён" : "без объяснения" }),
        s.at ? el("span", { class: "card__meta", text: s.at }) : null),
      s.movedText || s.comment
        ? el("div", { class: "card__body", text: [s.movedText, s.comment].filter(Boolean).join(" — ") })
        : null);
  }));

  return sec("1", "Паспорт задачи", null, rows, sub("Переносы срока"), list);
}

function drawGoal(g) {
  // Цель идёт прозой и первой: это единственное место среза, которое читают
  // целиком. В узкой колонке рядом с подписью она выглядела полем формы —
  // ровно тем, что глаз пропускает.
  //
  // «Как поставлено» и «Что имелось в виду» стоят подряд намеренно: расхождение
  // между ними и есть главный вывод раздела, и увидеть его можно, только когда
  // обе формулировки рядом.
  const kids = [lead("Как поставлено", g.asStated), lead("Что имелось в виду", g.clarified)];

  if (g.criteria.length) {
    kids.push(sub("Критерии приёмки"));
    kids.push(ticks(g.criteria.map(c =>
      tick(c.met ? "☑" : "☐", c.met,
        el("span", { class: "tick__text", text: c.n + ". " + c.text }), c.note))));
  }
  if (g.outOfScope.length) {
    kids.push(sub("Вне задачи"));
    kids.push(ticks(g.outOfScope.map(v => tick("—", false, val(v)))));
  }
  return sec("2", "Цель и границы", null, kids);
}

function drawStatus(st, share) {
  // Этапа и готовности здесь нет намеренно: обе строки слово в слово стоят в
  // сводке наверху, вместе со своими пометками происхождения. Повтор через
  // экран прокрутки ничего не добавлял, но занимал первый экран раздела — тот,
  // с которого начинают читать, — справкой вместо плана работ.
  const kids = [];

  if (st.milestones.length) {
    kids.push(el("div", { class: "steps" }, st.milestones.map(m =>
      el("div", { class: "step" },
        el("div", {},
          el("div", { class: "step__title", text: m.title }),
          bar(m.share, true)),
        el("div", { class: m.overdueText ? "step__due step__due--late" : "step__due", text: m.overdueText || m.due || "" }),
        el("div", { class: m.done ? "step__pct step__pct--done" : "step__pct", text: m.progress })))));
  }
  if (st.done.length) {
    kids.push(sub("Сделано"));
    kids.push(ticks(st.done.map(v => tick("✓", true, val(v)))));
  }
  if (st.left.length) {
    kids.push(sub("Осталось"));
    kids.push(ticks(st.left.map(v => tick("·", false, val(v)))));
  }
  return sec("3", "Статус и план", null, kids);
}

function drawTrouble(sl) {
  const kids = [];

  if (sl.blockers.length) {
    kids.push(el("ul", { class: "list" }, sl.blockers.map(b =>
      el("li", { class: "card card--warn" },
        el("div", { class: "card__top" },
          el("span", { class: "card__title", text: b.summary }),
          el("span", { class: "tag tag--red", text: b.kindLabel }),
          b.ageText ? el("span", { class: "card__meta", text: b.ageText }) : null),
        b.dependsOn ? el("div", { class: "card__body", text: "ждёт: " + b.dependsOn }) : null,
        el("div", { class: "card__body" }, val(b.evidence))))));
  } else {
    kids.push(el("div", { class: "row__label", text: "Блокеров не зафиксировано." }));
  }

  if (sl.risks.length) {
    kids.push(sub("Риски"));
    kids.push(el("ul", { class: "list" }, sl.risks.map(r =>
      el("li", { class: "card" },
        el("div", { class: "card__top" },
          el("span", { class: "card__title", text: r.summary }),
          r.impactText ? el("span", { class: "tag tag--amber", text: r.impactText }) : null,
          r.spread ? el("span", { class: "card__meta", text: r.spread }) : null),
        el("div", { class: "card__body" }, val(r.evidence))))));
  }
  return sec("4", "Блокеры и риски", null, kids);
}

function drawActions(pm) {
  const kids = [];

  if (pm.needed.length) {
    kids.push(el("ul", { class: "list" }, pm.needed.map(a =>
      el("li", { class: "card" },
        el("div", { class: "card__top" },
          el("span", { class: "tag tag--blue", text: a.kindLabel }),
          el("span", { class: "card__title", text: a.text })),
        a.why ? el("div", { class: "card__body", text: a.why }) : null))));
  }
  kids.push(el("div", { class: "rows", style: "margin-top:12px" },
    field("Следующая проверка", pm.nextCheck)));
  if (pm.comment) {
    kids.push(el("div", { class: "quote", style: "margin-top:10px" }, el("span", { text: pm.comment })));
  }
  return sec("5", "Что делать PM", null, kids);
}

function drawQuestions(list) {
  const open = list.filter(q => !q.answered).length;
  return sec("6", "Вопросы специалисту", open ? "без ответа: " + open : "все закрыты",
    ticks(list.map(q => {
      const body = el("div", {}, el("span", { text: q.n + ". " + q.text }));
      if (q.answered) body.append(val(q.answer));
      return tick(q.answered ? "☑" : "☐", q.answered, body, q.unlocks ? "разблокирует: " + q.unlocks : null);
    })));
}

function drawArtifacts(list) {
  if (!list.length) return null;
  const have = list.filter(a => a.present).length;
  return sec(null, "Артефакты", have + " из " + list.length + " на руках",
    ticks(list.map(a => tick(a.present ? "☑" : "☐", a.present,
      el("span", { text: a.name + (a.sizeText ? " · " + a.sizeText : "") }),
      a.present ? null : a.wouldGive))));
}

function drawSlice(sl) {
  return el("div", {},
    drawPassport(sl.passport),
    drawGoal(sl.goal),
    drawStatus(sl.status, sl.head.readinessShare),
    drawTrouble(sl),
    drawActions(sl.pmActions),
    drawQuestions(sl.questions),
    drawArtifacts(sl.artifacts));
}

// --- схемы процессов ---

// drawDiagram раскладывает шаги по сетке. Строка — дорожка, столбец — позиция в
// потоке; и то и другое посчитал сервер, здесь только сдвиг на единицу, потому
// что первый столбец занят подписью дорожки, а CSS считает от единицы.
function drawDiagram(d) {
  const grid = el("div", { class: "grid" });
  grid.style.setProperty("--cols", d.cols);

  d.lanes.forEach((name, row) => {
    const lane = el("div", { class: "lane", text: name });
    lane.style.gridRow = String(row + 1);
    grid.append(lane);
  });

  for (const s of d.steps) {
    const node = el("div", { class: s.problem ? "node node--problem" : "node" },
      el("div", { class: "node__n", text: s.id }),
      el("div", { class: "node__title", text: s.title }),
      s.note ? el("div", { class: "node__note", text: s.note }) : null);
    node.style.gridRow = String(s.row + 1);
    node.style.gridColumn = String(s.col + 2);
    grid.append(node);
  }

  const meta = [d.stepsText, d.lanesText, d.problemsText, d.loopsText].filter(Boolean).join(" · ");
  const box = el("div", { class: "diagram" },
    el("div", { class: "diagram__head" },
      el("span", { class: "diagram__title", text: d.title }),
      el("span", { class: "tag", text: d.kindLabel }),
      el("span", { class: "diagram__meta", text: meta })),
    el("div", { class: "canvas" }, grid));

  if (d.links.length) {
    box.append(el("div", { class: "flow" }, d.links.map(l =>
      el("div", { class: l.back ? "flow__row flow__row--back" : "flow__row" },
        el("span", { class: "flow__code", text: l.from + (l.back ? " ⟲ " : " → ") + l.to }),
        l.label ? " " + l.label : null))));
  }
  if (d.changes.length) {
    box.append(el("ul", { class: "changes" }, d.changes.map(c =>
      el("li", { class: "change" },
        el("span", { class: "tag tag--green", text: c.opLabel }),
        el("span", { text: c.text + (c.nodeId ? " (" + c.nodeId + ")" : "") })))));
  }
  box.append(el("div", { class: "rows", style: "margin-top:10px" }, field("Основание", d.evidence)));
  return box;
}

function drawDiagrams(list) {
  if (!list.length) {
    return el("div", { class: "empty", text: "Схемы не собраны: аналитик не нашёл в источниках описания процесса." });
  }
  return el("div", {}, list.map(d => drawDiagram(d)));
}

// --- источники ---

function drawSources(list) {
  if (!list.length) return el("div", { class: "empty", text: "Источников нет." });

  const box = el("div", {});
  for (const s of list) {
    const body = el("pre", { class: "body", hidden: true });
    let loaded = false;

    const toggle = el("button", {
      class: "btn",
      type: "button",
      text: "показать",
      onclick: async () => {
        if (!loaded) {
          toggle.disabled = true;
          try {
            const full = await api("/api/sources/" + encodeURIComponent(s.id));
            body.textContent = full.body || "(пусто)";
            loaded = true;
          } catch (e) {
            body.textContent = "не удалось загрузить: " + e.message;
          } finally {
            toggle.disabled = false;
          }
        }
        body.hidden = !body.hidden;
        toggle.textContent = body.hidden ? "показать" : "скрыть";
      },
    });

    box.append(
      el("div", { class: "src" },
        el("div", {},
          el("span", { class: "src__title", text: s.title }),
          el("span", { class: "tag", style: "margin-left:8px", text: s.kindLabel }),
          el("div", { class: "src__meta", text: s.uploadedAt + " · " + s.sizeText })),
        toggle),
      body);
  }
  return box;
}

// --- журнал фактов ---

async function drawFacts(id) {
  const list = await api("/api/tasks/" + encodeURIComponent(id) + "/facts");
  if (!list.length) return el("div", { class: "empty", text: "Журнал пуст." });

  const head = el("tr", {}, ["Поле", "Значение", "Уверенность", "Наблюдено", "Записано"].map(t =>
    el("th", { text: t })));

  const rows = list.map(f => el("tr", {},
    el("td", { class: "facts__field", text: f.field }),
    el("td", {}, val(f.value)),
    el("td", { class: "facts__num", text: f.confidenceText }),
    el("td", { class: "facts__num", text: f.observedAt || "—" }),
    el("td", { class: "facts__num", text: f.createdAt })));

  return el("table", { class: "facts" }, el("thead", {}, head), el("tbody", {}, rows));
}

// --- страница ---

function render() {
  const b = state.board;
  const page = el("div", { class: "page" },
    el("div", { class: "crumb", text: b.task.project }),
    el("h1", { text: b.slice.head.title }),
    drawHead(b.slice.head),
    drawChats(b.chats),
    drawWarns(b.slice.head));

  page.append(el("div", { class: "actions" },
    el("button", {
      class: "btn btn--primary",
      type: "button",
      text: "Пересобрать срез",
      onclick: ev => rebuild(ev.currentTarget),
    }),
    el("button", { class: "btn", type: "button", text: "Добавить источник", onclick: addSource }),
    // Кнопка есть только у задачи с закреплённым чатом: подтягивать неоткуда,
    // а кнопка, которая всегда отвечает «чата нет», — обещание, которого
    // интерфейс не сдержит.
    b.chats.length
      ? el("button", {
        class: "btn",
        type: "button",
        text: "Подтянуть переписку",
        onclick: ev => pull(ev.currentTarget),
      })
      : null,
  ));

  // Сведения о сборке — не действие, и в ряду кнопок читались как подпись к
  // ним. Место им под заголовком, рядом с остальным, что описывает срез.
  page.append(el("div", { class: "byline" },
    "собрал: " + b.slice.head.analyst + " · фактов в журнале: " + b.facts));

  const panels = [
    ["slice", "Срез", null, () => drawSlice(b.slice)],
    ["flows", "Схемы", b.diagrams.length, () => drawDiagrams(b.diagrams)],
    ["sources", "Источники", b.sources.length, () => drawSources(b.sources)],
    ["versions", "Версии", b.versions.length, () => drawVersions(b.versions)],
    ["facts", "Факты", b.facts, () => drawFacts(b.task.id)],
  ];

  const tabs = el("div", { class: "tabs", role: "tablist" });
  const body = el("div", {});
  page.append(tabs, body);

  const show = key => {
    state.tab = key;
    for (const btn of tabs.children) {
      btn.setAttribute("aria-selected", String(btn.dataset.tab === key));
    }
    const build = panels.find(p => p[0] === key)[3];
    const out = build();
    // Журнал фактов запрашивается отдельно: он нужен редко и незачем тащить его
    // вместе с каждым открытием задачи.
    if (out instanceof Promise) {
      body.replaceChildren(el("div", { class: "empty", text: "Загрузка…" }));
      out.then(node => {
        if (state.tab === key) body.replaceChildren(node);
      }).catch(e => body.replaceChildren(el("div", { class: "err", text: e.message })));
      return;
    }
    body.replaceChildren(out);
  };

  for (const [key, label, count] of panels) {
    tabs.append(el("button", {
      class: "tab",
      type: "button",
      role: "tab",
      "data-tab": key,
      "aria-selected": String(key === state.tab),
      onclick: () => show(key),
    }, label, count ? el("span", { class: "tab__n", text: count }) : null));
  }

  $("#main").replaceChildren(page);
  show(panels.some(p => p[0] === state.tab) ? state.tab : "slice");
}

function flash(text, cls = "err") {
  const page = $("#main .page");
  if (!page) return;
  const box = el("div", { class: cls, text });
  page.insertBefore(box, page.children[2] || null);
}

async function open(id) {
  state.current = id;
  state.board = null;
  drawTasks();
  if (location.hash.slice(1) !== id) location.hash = id;
  $("#main").replaceChildren(el("div", { class: "empty", text: "Загрузка…" }));

  try {
    state.board = await api("/api/tasks/" + encodeURIComponent(id) + "/board");
    render();
  } catch (e) {
    $("#main").replaceChildren(el("div", { class: "empty" }, el("div", { class: "err", text: e.message })));
  }
}

// --- диалог ---

// ask показывает форму и возвращает значения полей либо null, если отменили.
function ask(title, fields) {
  const dlg = $("#modal");
  const body = $("#modal-body");
  $("#modal-title").textContent = title;
  body.replaceChildren();

  const inputs = {};
  for (const f of fields) {
    const id = "field-" + f.name;
    let input;
    if (f.kind === "text") {
      input = el("textarea", { id, required: f.required, placeholder: f.hint });
    } else if (f.kind === "search") {
      // Поиск идёт на портале, а не по загруженному списку: задач там почти
      // десять тысяч, и нужная почти никогда не из последней сотни. Выгружать
      // их все ради подстроки — мегабайты по сети на каждое открытие формы.
      input = el("select", { id, size: 8, class: "field__list" },
        f.options.map(o => el("option", { value: o[0], text: o[1] })));

      const hint = el("div", { class: "field__hint", text: f.note || "" });

      // Пауза перед запросом: иначе на каждую букву идёт поход в чужой сервис.
      let timer = null;
      const search = el("input", {
        id: id + "-q",
        type: "search",
        class: "field__search",
        placeholder: "часть названия задачи в Bitrix24",
        // Enter здесь означает «нашёл», а не «сохранить»: отправка формы по
        // нему потеряла бы остальные поля.
        onkeydown: ev => { if (ev.key === "Enter") ev.preventDefault(); },
        oninput: () => {
          clearTimeout(timer);
          const q = search.value.trim();
          hint.textContent = q ? "ищу…" : (f.note || "");
          timer = setTimeout(async () => {
            try {
              const found = await f.search(q);
              input.replaceChildren(...found.map(o => el("option", { value: o[0], text: o[1] })));
              // Выбор сбрасывается вместе со списком: иначе форма отправила бы
              // задачу, которой человек уже не видит.
              if (f.onChange) f.onChange(input.value, inputs);
              hint.textContent = q
                ? (found.length ? "нашлось: " + found.length : "ничего не найдено")
                : (f.note || "");
            } catch (e) {
              hint.textContent = "поиск не удался: " + e.message;
            }
          }, 350);
        },
      });

      inputs[f.name] = input;
      if (f.onChange) input.addEventListener("change", () => f.onChange(input.value, inputs));
      body.append(el("div", { class: "field" },
        el("label", { class: "field__label", for: id + "-q", text: f.label }),
        search, input, hint));
      continue;
    } else if (f.kind === "select") {
      input = el("select", { id }, f.options.map(o => el("option", { value: o[0], text: o[1] })));
    } else {
      input = el("input", { id, type: f.kind || "text", required: f.required, placeholder: f.hint, value: f.value });
    }
    inputs[f.name] = input;
    // Шов для полей, которые заполняют другие поля: выбор задачи портала
    // подставляет название, людей и даты. Обработчик получает все поля формы, а
    // не отдельный список: перечислять их значило бы завести второе описание
    // формы рядом с этим.
    if (f.onChange) input.addEventListener("change", () => f.onChange(input.value, inputs));
    const row = [el("label", { class: "field__label", for: id, text: f.label }), input];
    if (f.note) row.push(el("div", { class: "field__hint", text: f.note }));
    body.append(el("div", { class: "field" }, row));
  }

  dlg.returnValue = "";
  dlg.showModal();
  const first = Object.values(inputs)[0];
  if (first) first.focus();

  return new Promise(resolve => {
    dlg.addEventListener("close", function done() {
      dlg.removeEventListener("close", done);
      if (dlg.returnValue !== "ok") {
        resolve(null);
        return;
      }
      const out = {};
      for (const [name, input] of Object.entries(inputs)) out[name] = input.value.trim();
      resolve(out);
    });
  });
}

// --- действия ---

// rebuild пересобирает срез.
//
// Итог показывается всегда, даже когда версия не появилась: «материал не
// менялся» — это ответ, а молчание после нажатия человек читает как поломку.
async function rebuild(btn) {
  const done = busy(btn, "Собираю срез…");
  try {
    const res = await api("/api/tasks/" + encodeURIComponent(state.current) + "/slice/rebuild", { method: "POST" });
    done();
    await open(state.current);
    flash(res.text, res.built ? "done" : "warn");
  } catch (e) {
    done();
    flash(e.message);
  }
}

// pull переносит новые сообщения закреплённых чатов.
//
// Срез после этого не пересобирается, в отличие от загрузки источника. Разница
// не в удобстве: перенесённое сообщение станет источником, только если на него
// сослался разбор, и пересборка сразу после подтяжки чаще всего дала бы ту же
// самую версию — то есть лишнюю запись в истории.
async function pull(btn) {
  const done = busy(btn, "Читаю чат…");
  try {
    const res = await api("/api/tasks/" + encodeURIComponent(state.current) + "/pull", { method: "POST" });
    // Перерисовываем в любом случае: даже без новых сообщений изменилось время
    // последнего похода, и человек должен видеть, что кнопка сработала.
    // Сначала страница, потом сообщение: open заменяет содержимое целиком и
    // стёр бы сообщение, вставленное до него.
    done();
    await open(state.current);
    flash(res.text, res.added ? "done" : "warn");
  } catch (e) {
    done();
    flash(e.message);
  }
}

// addSource грузит материал и сразу пересобирает срез: смысл загрузки в том,
// чтобы срез изменился, а не в том, чтобы файл лёг в список.
async function addSource() {
  const got = await ask("Новый источник", [
    {
      name: "kind", label: "Что это", kind: "select", options: [
        ["correspondence", "Переписка"],
        ["spec", "ТЗ"],
        ["audit", "Аудит"],
        ["bridge", "Сводка с Капитанского мостика"],
        ["doc", "Документ"],
        ["note", "Заметка"],
      ],
    },
    { name: "title", label: "Название", required: true, hint: "Переписка в Битрикс24, июнь" },
    // Автор и дата события — для материала, который сам про себя не
    // рассказывает. У сводки с мостика дата планёрки известна человеку, а из
    // текста её не вычитать; пустой она и останется, а не подменится днём
    // загрузки.
    { name: "author", label: "Автор", hint: "кто это сказал или составил" },
    { name: "occurredAt", label: "Когда это было", kind: "date" },
    { name: "body", label: "Текст", kind: "text", required: true, hint: "Вставьте выгрузку целиком" },
  ]);
  if (!got) return;

  // Кнопки здесь нет — форма уже закрыта, — но полоса вверху нужна: следом
  // идёт пересборка, а она занимает около минуты.
  const done = busy(null, "");
  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/sources", {
      method: "POST",
      body: JSON.stringify(got),
    });
    // Новый материал — повод пересобрать: смысл загрузки в том, чтобы срез
    // изменился, а не в том, чтобы файл лёг в список.
    const res = await api("/api/tasks/" + encodeURIComponent(state.current) + "/slice/rebuild",
      { method: "POST" });
    done();
    await open(state.current);
    flash(res.text, res.built ? "done" : "warn");
  } catch (e) {
    done();
    flash(e.message);
  }
}

// FROM_PORTAL — поля, которые заполняются из карточки задачи Bitrix24. Список
// один и тот же и для подстановки, и для ответа сервера: имена полей формы там
// и там совпадают намеренно, поэтому таблицы соответствий здесь нет.
const FROM_PORTAL = ["title", "author", "assignee", "openedAt", "deadline"];

async function addTask() {
  let portal = [];
  // Чат больше не выбирают: у задачи Bitrix24 он ровно один, и спрашивать про
  // то, у чего нет выбора, незачем. Ручка чатов на сервере осталась — она нужна
  // для обсуждения в отдельном групповом чате.
  const taskField = {
    name: "bitrixTaskId",
    label: "Задача Bitrix24",
    kind: "search",
    options: [["", "без задачи портала"]],
    note: "Bitrix24 не настроен: задача создастся без чата",
    // search спрашивает портал заново. Пустой запрос — свежие задачи: форма
    // должна открываться сразу и с чем-то в списке.
    async search(q) {
      const opts = await api("/api/bitrix/tasks" + (q ? "?q=" + encodeURIComponent(q) : ""));
      portal = opts.tasks || [];
      return [["", "без задачи портала"]].concat(portal.map(t => [t.id, t.label]));
    },
    onChange(id, inputs) {
      const chosen = portal.find(t => t.id === id);
      if (!chosen) return;
      // Заполняются только пустые поля: человек мог начать печатать до того, как
      // выбрал задачу, и затирать набранное им нельзя.
      for (const name of FROM_PORTAL) {
        const input = inputs[name];
        if (input && !input.value && chosen[name]) input.value = chosen[name];
      }
    },
  };
  try {
    const opts = await api("/api/bitrix/tasks");
    portal = opts.tasks || [];
    taskField.note = opts.note;
    taskField.options = [["", "без задачи портала"]].concat(portal.map(t => [t.id, t.label]));
  } catch (e) {
    // Отказ портала не мешает создать задачу: реестр не должен переставать
    // работать из-за недоступного Bitrix.
    taskField.note = "список задач не загрузился: " + e.message + ". Задачу можно создать без чата.";
  }

  const got = await ask("Новая задача", [
    taskField,
    { name: "project", label: "Проект", required: true, hint: "АУРА — дистрибуция бытовой химии" },
    { name: "title", label: "Задача", required: true, hint: "Внедрение автоматизации" },
    { name: "author", label: "Автор постановки", hint: "кто поставил" },
    { name: "assignee", label: "Исполнитель", hint: "кто делает" },
    { name: "openedAt", label: "Поставлена", kind: "date" },
    { name: "deadline", label: "Срок", kind: "date" },
    { name: "budget", label: "Бюджет, ₽", kind: "number" },
  ]);
  if (!got) return;

  try {
    const body = { ...got, budget: Number(got.budget) || 0 };
    if (!body.bitrixTaskId) delete body.bitrixTaskId;
    const created = await api("/api/tasks", {
      method: "POST",
      body: JSON.stringify(body),
    });
    state.tasks = await api("/api/tasks");
    await open(created.id);
  } catch (e) {
    flash(e.message);
  }
}

// --- запуск ---

async function boot() {
  $("#add-task").addEventListener("click", addTask);
  $("#open-journal").addEventListener("click", openJournal);
  drawUser();
  window.addEventListener("hashchange", () => {
    const id = location.hash.slice(1);
    if (id && id !== state.current) open(id);
  });

  try {
    const [health, tasks] = await Promise.all([api("/api/health"), api("/api/tasks")]);
    $("#analyst").textContent = "аналитик: " + health.analyst;
    state.tasks = tasks;
    drawTasks();

    const id = location.hash.slice(1) || (tasks[0] && tasks[0].id);
    if (id) {
      await open(id);
    } else {
      $("#main").replaceChildren(el("div", { class: "empty", text: "Задач нет. Создайте первую слева." }));
    }
  } catch (e) {
    $("#main").replaceChildren(el("div", { class: "empty" },
      el("div", { class: "err", text: "Сервер не ответил: " + e.message })));
  }
}

boot();

// --- версии ---

// drawVersions рисует историю сборок.
//
// У каждой версии, кроме самой старой, есть кнопка сравнения с предыдущей.
// Выбор двух версий из двух списков был бы гибче, но вопрос, который задают
// почти всегда, один: «что изменилось с прошлого раза».
function drawVersions(list) {
  if (!list || !list.length) {
    return el("div", { class: "empty", text: "Срез ещё не собирался." });
  }

  const box = el("div", { class: "versions" });
  const out = el("div", { class: "diff" });

  list.forEach((v, i) => {
    const prev = list[i + 1];
    const row = el("div", { class: "versions__row" },
      el("span", { class: "versions__label", text: v.label }),
      i === 0 ? el("span", { class: "crumb", text: "текущая" }) : null,
      prev
        ? el("button", {
          class: "btn btn--small",
          type: "button",
          text: "сравнить с v" + prev.version,
          onclick: ev => compare(ev.currentTarget, prev.version, v.version, out),
        })
        : null);
    box.append(row);
  });

  return el("div", {}, box, out);
}

// compare показывает различия между двумя версиями.
async function compare(btn, a, b, out) {
  btn.disabled = true;
  try {
    const url = "/api/tasks/" + encodeURIComponent(state.current) +
      "/slices/compare?a=" + a + "&b=" + b;
    const d = await api(url);
    out.replaceChildren(drawDiff(d));
  } catch (e) {
    out.replaceChildren(el("div", { class: "err", text: e.message }));
  } finally {
    btn.disabled = false;
  }
}

function drawDiff(d) {
  const box = el("div", {},
    el("h3", { text: "Было v" + d.before.version + " → стало v" + d.after.version }),
    el("div", { class: "crumb", text: d.text }));

  if (!d.changes.length) return box;

  // Различия сгруппированы по разделам среза: сравнение читают сверху вниз,
  // как и сам срез.
  const sections = new Map();
  for (const c of d.changes) {
    if (!sections.has(c.section)) sections.set(c.section, []);
    sections.get(c.section).push(c);
  }

  for (const [section, changes] of sections) {
    box.append(el("h4", { class: "diff__section", text: section }));
    for (const c of changes) {
      const row = el("div", { class: "diff__row diff__row--" + c.kind },
        el("div", { class: "diff__field" }, c.field + " · " + c.label));
      // Тексты идут через textContent: это чужой текст из переписки, и
      // вставлять его разметкой нельзя ни при каких обстоятельствах.
      if (c.before) row.append(el("div", { class: "diff__before", text: "было: " + c.before }));
      if (c.after) row.append(el("div", { class: "diff__after", text: "стало: " + c.after }));
      box.append(row);
    }
  }
  return box;
}

// --- журнал инцидентов ---

// openJournal показывает журнал случаев.
//
// Отдельный экран, а не вкладка задачи: журнал ведут по человеку и месяцу, а не
// по задаче. Задача в записи есть, но она место, где случай произошёл, а не то,
// вокруг чего журнал устроен.
async function openJournal() {
  state.current = "";
  drawTasks();
  location.hash = "";
  $("#main").replaceChildren(el("div", { class: "empty", text: "Загрузка…" }));

  try {
    state.journal = await api("/api/incidents");
    renderJournal();
  } catch (e) {
    $("#main").replaceChildren(el("div", { class: "empty" }, el("div", { class: "err", text: e.message })));
  }
}

function renderJournal() {
  const j = state.journal;
  // Журнал шире среза: у среза ширина ограничена ради чтения прозы, а здесь
  // девять столбцов, и та же колонка загнала бы половину из них под прокрутку.
  const page = el("div", { class: "page page--wide" },
    el("div", { class: "crumb", text: "Оценка KPI" }),
    el("h1", { text: "Журнал инцидентов" }),
    el("div", { class: "crumb" },
      "Основание для оценки: без зафиксированного случая KPI не снижается. " +
      "Оценку в процентах ставит руководитель — реестр хранит случаи."),
    el("div", { class: "actions" },
      el("button", {
        class: "btn btn--primary",
        type: "button",
        text: "Записать случай",
        onclick: addIncident,
      }),
      el("span", { class: "actions__note", text: j.text })));

  if (!j.incidents.length) {
    page.append(el("div", { class: "empty", text: "Пока ни одного случая." }));
    $("#main").replaceChildren(page);
    return;
  }

  page.append(journalTable(j.incidents));
  $("#main").replaceChildren(page);
}

// journalTable рисует журнал таблицей — той же, что руководитель вёл в
// электронной таблице.
//
// Карточки читались хуже: журнал просматривают по столбцу («у кого за месяц
// накопилось», «где эскалации не было»), а карточка заставляет читать каждую
// запись целиком, чтобы найти в ней одно поле. Столбцы стоят в том же порядке,
// что и в исходной таблице, — переносить взгляд с бумаги на экран не приходится.
function journalTable(incidents) {
  const head = el("tr", {}, ["Дата", "Специалист", "Проект", "Блок KPI", "Что произошло",
    "В зоне контроля", "Эскалация", "Комментарий руководителя", "Кто зафиксировал"]
    .map(t => el("th", { text: t })));

  const rows = incidents.map(in_ => {
    // Проект — свободная строка руководителя; связанная задача реестра, если
    // она есть, идёт под ним отдельной строкой: это ссылка на срез, а не
    // второе название проекта.
    const project = el("td", {}, el("div", { text: in_.project }),
      in_.taskId
        ? el("button", {
            class: "linkish", type: "button", text: in_.taskTitle || in_.taskId,
            onclick: () => open(in_.taskId),
          })
        : null);

    // Текст случая — через textContent: это чужие слова о человеке, и разметкой
    // они быть не должны ни при каких обстоятельствах. Переносы строк в нём
    // значимы — руководитель делит запись на факты и последствия, — поэтому
    // ячейка сохраняет их через CSS, а не через <br>.
    return el("tr", { class: in_.external ? "jr jr--external" : "jr" },
      el("td", { class: "jr__date", text: in_.at }),
      el("td", { text: in_.employee }),
      project,
      el("td", {}, el("span", { class: "tag", text: in_.blockLabel })),
      el("td", { class: "jr__text", text: in_.text }),
      el("td", { class: "jr__yn" },
        el("span", { text: in_.controlText }),
        in_.note ? el("div", { class: "crumb", text: in_.note }) : null),
      el("td", { class: "jr__yn", text: in_.escalatedText }),
      el("td", { class: in_.managerNote ? null : "miss", text: in_.managerNote || "не разобрано" }),
      el("td", { class: "jr__who", text: in_.recordedBy }));
  });

  return el("div", { class: "sheet" },
    el("table", { class: "journal" },
      el("thead", {}, head),
      el("tbody", {}, rows)));
}

async function addIncident() {
  const j = state.journal;
  // Задача реестра необязательна: журнал ведут по всем работам сразу, а в
  // реестр заведены единицы. Пустой первый пункт — это «случай не в задаче
  // реестра», а не пропущенное поле.
  const tasks = [["", "— не в задаче реестра —"], ...state.tasks.map(t => [t.id, t.title])];

  const got = await ask("Случай", [
    { name: "at", label: "Дата", kind: "date", required: true, hint: "когда случилось" },
    { name: "employee", label: "Специалист", required: true, hint: "кого касается" },
    { name: "project", label: "Проект", required: true, hint: "работа, в которой это случилось" },
    { name: "taskId", label: "Задача реестра", kind: "select", options: tasks },
    // Ровно один блок: один случай не должен съедать несколько блоков сразу.
    { name: "block", label: "Блок KPI", kind: "select", options: j.blocks.map(b => [b.value, b.label]) },
    { name: "text", label: "Что произошло", kind: "text", required: true, hint: "факты и последствия, а не оценка" },
    {
      name: "control", label: "В зоне контроля специалиста", kind: "select",
      options: [["yes", "да"], ["no", "нет — помешал клиент или внешний фактор"]],
      note: "«Нет» оставит случай в журнале как объяснение, но в оценку он не пойдёт.",
    },
    {
      name: "escalated", label: "Была эскалация", kind: "select",
      options: [["", "нет"], ["yes", "да — сообщили наверх"]],
      note: "Эскалация меняет знак случая: штрафуется не проблема, а молчание о ней.",
    },
    { name: "escalatedAt", label: "Когда эскалировано", kind: "date" },
    { name: "managerNote", label: "Комментарий руководителя", kind: "text" },
  ]);
  if (!got) return;

  try {
    // Подпись «кто зафиксировал» не отправляем: её ставит сервер из пропуска.
    await api("/api/incidents", {
      method: "POST",
      body: JSON.stringify({
        ...got,
        external: got.control === "no",
        escalated: got.escalated === "yes",
      }),
    });
    await openJournal();
  } catch (e) {
    flash(e.message);
  }
}

// --- кто вошёл ---

// drawUser показывает имя вошедшего и кнопку выхода.
//
// Без входа блок скрыт целиком: на локальном запуске без REESTR_USERS показывать
// «выйти» некуда и незачем.
async function drawUser() {
  let login = "";
  try {
    login = (await api("/api/me")).login || "";
  } catch {
    // Не ответили — значит и показывать нечего. Ронять из-за этого страницу
    // незачем: реестр читается и без подписи в углу.
  }
  if (!login) return;

  $("#user-login").textContent = login;
  $("#user").hidden = false;
  $("#logout").addEventListener("click", async () => {
    try {
      await api("/api/logout", { method: "POST" });
    } finally {
      location.href = "/login";
    }
  });
}

// --- занятость ---

// busy отмечает долгую работу: полоса вверху и счётчик секунд на кнопке.
//
// Сборка среза моделью занимает около минуты. Замершая кнопка на минуту
// выглядит как зависший интерфейс, и человек жмёт её второй раз — а второй раз
// это второй оплаченный вызов модели. Поэтому видно и что работа идёт, и
// сколько она уже длится.
//
// Возвращает функцию, которая всё возвращает на место. Вызывать её обязательно,
// в том числе на ошибке: полоса, оставшаяся ползти навсегда, хуже её отсутствия.
function busy(btn, label) {
  const bar = $("#progress");
  const started = Date.now();
  const was = btn ? btn.textContent : "";

  if (bar) bar.hidden = false;
  if (btn) btn.disabled = true;

  const tick = () => {
    if (!btn) return;
    const sec = Math.round((Date.now() - started) / 1000);
    // Первые секунды без счётчика: на быстрой операции мелькающие цифры только
    // дёргают глаз.
    btn.textContent = sec < 2 ? label : label + " " + sec + " с";
  };
  tick();
  const timer = setInterval(tick, 1000);

  return () => {
    clearInterval(timer);
    if (bar) bar.hidden = true;
    if (btn) {
      btn.disabled = false;
      btn.textContent = was;
    }
  };
}
