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
  box.append(el("div", { class: "head__cell", style: "min-width:200px;flex:1" },
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
  return el("div", { class: "crumb", style: "margin-top:8px" }, "чат: " + line);
}

function drawWarns(h) {
  const rows = [
    ["!", h.overdueText, "warn warn--red"],
    ["!", h.blockerText, "warn warn--red"],
    ["?", h.gapsText, "warn"],
    ["↔", h.shiftsText, "warn"],
    ["✓", h.criteriaText, "warn warn--green"],
  ].filter(([, text]) => text);

  if (!rows.length) return null;

  return el("div", { class: "warns" }, rows.map(([sign, text, cls]) =>
    el("div", { class: cls }, el("span", { class: "warn__mark", text: sign }), el("span", { text }))));
}

// --- срез ---

function ticks(items) {
  return el("ul", { class: "ticks" }, items);
}

function tick(box, met, body, note) {
  return el("li", { class: "tick" },
    el("span", { class: met ? "tick__box tick__box--met" : "tick__box", text: box }),
    el("div", {}, body, note ? el("div", { class: "tick__note", text: note }) : null));
}

function drawPassport(p) {
  const rows = el("div", { class: "rows" },
    field("Название", p.title),
    field("Автор постановки", p.author),
    field("Исполнитель", p.assignee),
    field("Поставлена", p.openedAt),
    field("Срок", p.deadline));

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

  return sec("1", "Паспорт задачи", null, rows,
    el("div", { class: "sec__head", style: "margin-top:18px" },
      el("span", { class: "sec__title", style: "font-size:14px", text: "Переносы срока" })),
    list);
}

function drawGoal(g) {
  const kids = [el("div", { class: "rows" },
    field("Как поставлено", g.asStated),
    field("Что имелось в виду", g.clarified))];

  if (g.criteria.length) {
    kids.push(el("div", { class: "sec__head", style: "margin-top:18px" },
      el("span", { class: "sec__title", style: "font-size:14px", text: "Критерии приёмки" })));
    kids.push(ticks(g.criteria.map(c =>
      tick(c.met ? "☑" : "☐", c.met, el("span", { text: c.n + ". " + c.text }), c.note))));
  }
  if (g.outOfScope.length) {
    kids.push(el("div", { class: "sec__head", style: "margin-top:18px" },
      el("span", { class: "sec__title", style: "font-size:14px", text: "Вне задачи" })));
    kids.push(ticks(g.outOfScope.map(v => tick("—", false, val(v)))));
  }
  return sec("2", "Цель и границы", null, kids);
}

function drawStatus(st, share) {
  const kids = [el("div", { class: "rows" },
    field("Этап", st.stage),
    el("div", { class: "row" },
      el("div", { class: "row__label", text: "Готовность" }),
      el("div", { class: "row__value" }, val(st.readiness), bar(share))))];

  if (st.milestones.length) {
    kids.push(el("div", { class: "steps", style: "margin-top:14px" }, st.milestones.map(m =>
      el("div", { class: "step" },
        el("div", {},
          el("div", { class: "step__title", text: m.title }),
          bar(m.share, true)),
        el("div", { class: m.overdueText ? "step__due step__due--late" : "step__due", text: m.overdueText || m.due || "" }),
        el("div", { class: m.done ? "step__pct step__pct--done" : "step__pct", text: m.progress })))));
  }
  if (st.done.length) {
    kids.push(el("div", { class: "sec__head", style: "margin-top:18px" },
      el("span", { class: "sec__title", style: "font-size:14px", text: "Сделано" })));
    kids.push(ticks(st.done.map(v => tick("✓", true, val(v)))));
  }
  if (st.left.length) {
    kids.push(el("div", { class: "sec__head", style: "margin-top:18px" },
      el("span", { class: "sec__title", style: "font-size:14px", text: "Осталось" })));
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
    kids.push(el("div", { class: "sec__head", style: "margin-top:20px" },
      el("span", { class: "sec__title", style: "font-size:14px", text: "Риски" }),
      el("span", { class: "sec__note", text: sl.risks.length + " шт." })));
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
    el("span", { class: "actions__note", text: "аналитик: " + b.slice.head.analyst + " · фактов: " + b.facts })));

  const panels = [
    ["slice", "Срез", null, () => drawSlice(b.slice)],
    ["flows", "Схемы", b.diagrams.length, () => drawDiagrams(b.diagrams)],
    ["sources", "Источники", b.sources.length, () => drawSources(b.sources)],
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

function flash(text) {
  const page = $("#main .page");
  if (!page) return;
  const box = el("div", { class: "err", text });
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

async function rebuild(btn) {
  btn.disabled = true;
  btn.textContent = "Собираю…";
  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/slice/rebuild", { method: "POST" });
    await open(state.current);
  } catch (e) {
    btn.disabled = false;
    btn.textContent = "Пересобрать срез";
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
        ["note", "Заметка"],
      ],
    },
    { name: "title", label: "Название", required: true, hint: "Переписка в Битрикс24, июнь" },
    { name: "body", label: "Текст", kind: "text", required: true, hint: "Вставьте выгрузку целиком" },
  ]);
  if (!got) return;

  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/sources", {
      method: "POST",
      body: JSON.stringify(got),
    });
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/slice/rebuild", { method: "POST" });
    await open(state.current);
  } catch (e) {
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
    kind: "select",
    options: [["", "без задачи портала"]],
    note: "Bitrix24 не настроен: задача создастся без чата",
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
