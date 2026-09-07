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
  journalTab: "rows",

  // Выбранный отрезок журнала. Пустой означает «весь журнал» — законный вид, а
  // не забытый фильтр.
  period: {},
};

// --- значение с происхождением ---

// val рисует значение и пометку рядом. По пометке раскрывается то, на чём
// значение держится: цитата, источник, пояснение. Пока не раскрыли — на экране
// только значение, и оно не тонет в служебных подписях.
function val(v) {
  const box = el("div", { class: v.field ? "val val--fixable" : "val" });
  box.append(el("span", { class: v.known ? null : "miss", text: v.text }));

  // Через put, а не через append напрямую: append превращает null в строку
  // «null» и печатает её на экране, а карандаша у неправимого поля нет.
  const backing = v.quote || v.note || v.sourceTitle;
  if (!backing) {
    put(box, [el("span", { class: "mark mark--" + v.origin, text: v.mark, title: v.originLabel }), fix(v)]);
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

  put(box, [btn, fix(v), quote]);
  return box;
}

// fix — карандаш правки у значения, которое человек может назвать сам.
//
// Появляется только у полей с адресом: список правимых полей живёт на сервере,
// в домене, и браузер про него ничего не решает. Виден по наведению — правка
// нужна изредка, а девять карандашей на экране кричали бы, что срез недоделан.
function fix(v) {
  if (!v.field) return null;
  return el("button", {
    class: "fix",
    type: "button",
    title: "Поправить: " + v.fieldLabel,
    text: "✎",
    onclick: () => correct(v),
  });
}

// correct спрашивает верное значение и записывает его.
//
// Правка не переписывает версию, а заводит следующую: срез — основание для
// разговора с заказчиком, и «в прошлый раз тут стояло другое» должно оставаться
// проверяемым.
async function correct(v) {
  const edited = v.origin === "stated";
  const got = await ask("Поправить: " + v.fieldLabel, [
    {
      name: "text", label: v.fieldLabel, kind: "text", required: true,
      value: v.known ? v.text : "",
      note: "Значение встанет в срез как сказанное вами и пересборкой не затрётся. " +
        "Появится новая версия — прежняя останется в истории.",
    },
  ], edited ? { value: "revert", text: "Вернуть как было" } : null);
  if (!got) return;

  if (got.action === "revert") {
    await dropCorrections(v.field);
    return;
  }
  await sendCorrection(v.field, { text: got.text });
}

// sendCorrection отправляет правку и перечитывает задачу.
async function sendCorrection(field, edit) {
  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/slice/correct", {
      method: "POST",
      body: JSON.stringify({ field, edit }),
    });
    await open(state.current);
  } catch (e) {
    flash(e.message);
  }
}

// dropCorrections снимает правки поля — или все правки задачи, если поле пустое.
//
// Это возврат к тому, что сказал разбор. Модель при этом не зовётся: её выводы
// уже лежат в журнале фактов, и платить за них второй раз незачем.
async function dropCorrections(field) {
  const what = field
    ? "Вернуть поле к тому, что сказал разбор?"
    : "Снять все правки этой задачи?";
  if (!window.confirm(what + "\n\nПоявится новая версия среза; прежняя останется в истории.")) return;

  const q = field ? "?field=" + encodeURIComponent(field) : "";
  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/slice/corrections" + q, {
      method: "DELETE",
    });
    await open(state.current);
  } catch (e) {
    flash(e.message);
  }
}

// --- правка списков ---

// Из чего состоит строка каждого правимого списка.
//
// Таблица, а не форма на каждый вид: списков десять, и десять почти одинаковых
// форм разошлись бы между собой в первый же месяц. Поля названы так же, как их
// ждёт сервер, — переименование по дороге было бы лишним местом для ошибки.
const EDIT_ROWS = {
  valueList: [{ name: "text", label: "Пункт", kind: "text" }],
  criteria: [
    { name: "met", label: "Выполнен", kind: "check" },
    { name: "text", label: "Критерий", kind: "text" },
    { name: "note", label: "Пояснение", kind: "line" },
  ],
  milestones: [
    { name: "text", label: "Этап", kind: "line" },
    { name: "progress", label: "Готовность, %", kind: "percent" },
    { name: "due", label: "Срок", kind: "date" },
  ],
  blockers: [
    { name: "summary", label: "Что стоит", kind: "line" },
    { name: "kind", label: "Вид", kind: "select", from: "blockers" },
    { name: "dependsOn", label: "Ждём кого", kind: "line" },
    { name: "since", label: "Стоит с", kind: "date" },
    { name: "note", label: "Основание", kind: "text" },
  ],
  risks: [
    { name: "summary", label: "Риск", kind: "line" },
    { name: "days", label: "Срок +дней", kind: "number" },
    { name: "spread", label: "На что влияет", kind: "line" },
    { name: "note", label: "Основание", kind: "text" },
  ],
  questions: [
    { name: "text", label: "Вопрос", kind: "text" },
    { name: "unlocks", label: "Что даст ответ", kind: "line" },
    { name: "answer", label: "Ответ", kind: "line" },
  ],
  actions: [
    { name: "kind", label: "Что сделать", kind: "select", from: "actions" },
    { name: "text", label: "Действие", kind: "line" },
    { name: "why", label: "Что разблокирует", kind: "line" },
  ],
  artifacts: [
    { name: "text", label: "Файл", kind: "line" },
    { name: "present", label: "Приложен", kind: "check" },
    { name: "why", label: "Что дал бы", kind: "line" },
  ],
  shifts: [
    { name: "at", label: "Когда двинули", kind: "date" },
    { name: "from", label: "С даты", kind: "date" },
    { name: "to", label: "На дату", kind: "date" },
    { name: "note", label: "Объяснение", kind: "line" },
  ],
};

// rowsOf вынимает текущее содержимое списка в том виде, в каком его правят.
//
// Берётся из уже нарисованного среза, а не из отдельного запроса: на экране
// лежит ровно то, что человек правит, и второй источник значил бы, что форма
// однажды откроется не с тем, что видно.
function rowsOf(field, sl) {
  switch (field) {
    case "goal.outOfScope": return sl.goal.outOfScope.map(v => ({ text: v.text }));
    case "status.done": return sl.status.done.map(v => ({ text: v.text }));
    case "status.left": return sl.status.left.map(v => ({ text: v.text }));
    case "goal.criteria":
      return sl.goal.criteria.map(c => ({ text: c.text, met: c.met, note: c.note || "" }));
    case "status.milestones":
      return sl.status.milestones.map(m => ({
        text: m.title, progress: Math.round(m.share * 100), due: m.due || "",
      }));
    case "blockers":
      return sl.blockers.map(b => ({
        summary: b.summary, kind: b.kind, dependsOn: b.dependsOn || "",
        since: b.since || "", note: b.evidence.known ? b.evidence.text : "",
      }));
    case "risks":
      return sl.risks.map(r => ({
        summary: r.summary, days: r.days || 0, spread: r.spread || "",
        note: r.evidence.known ? r.evidence.text : "",
      }));
    case "questions":
      return sl.questions.map(q => ({
        text: q.text, unlocks: q.unlocks || "", answer: q.answered ? q.answer.text : "",
      }));
    case "pmActions.needed":
      return sl.pmActions.needed.map(a => ({ kind: a.kind, text: a.text, why: a.why || "" }));
    case "artifacts":
      return sl.artifacts.map(a => ({ text: a.name, present: a.present, why: a.wouldGive || "" }));
    case "passport.shifts":
      return sl.passport.shifts.map(s => ({
        at: s.at || "", from: s.from || "", to: s.to || "", note: s.comment || "",
      }));
  }
  return [];
}

// editList правит список целиком.
//
// Целиком, а не по пунктам, потому что адреса «третий пункт» не существует:
// после пересборки третьим станет другой пункт, и правка молча уехала бы на
// чужую строку. Человек называет список — реестр кладёт его поверх разбора.
async function editList(field) {
  const sl = state.board.slice;
  const f = sl.edit.find(e => e.field === field);
  if (!f) return;

  const spec = EDIT_ROWS[f.kind];
  const edited = sl.editedFields.includes(field);
  const got = await askRows(f.label, spec, rowsOf(field, sl), sl.kinds,
    edited ? { value: "revert", text: "Вернуть как было" } : null);
  if (!got) return;

  if (got.action === "revert") {
    await dropCorrections(field);
    return;
  }
  await sendCorrection(field, { items: got.items });
}

// askRows показывает редактор списка: строки, у каждой свои поля, плюс
// добавление и удаление.
//
// Пустой список — законный ответ. «Блокеров больше нет» это утверждение, а не
// пропуск, и запретить его значило бы оставить снятый блокер в срезе навсегда.
function askRows(title, spec, rows, kinds, extra) {
  const dlg = $("#modal");
  const body = $("#modal-body");
  $("#modal-title").textContent = "Поправить: " + title;
  body.replaceChildren();

  const list = el("div", { class: "rowset" });
  const values = [];

  const addRow = data => {
    const inputs = {};
    const box = el("div", { class: "rowset__row" });

    for (const f of spec) {
      let input;
      if (f.kind === "check") {
        input = el("input", { type: "checkbox" });
        input.checked = !!data[f.name];
      } else if (f.kind === "select") {
        input = el("select", {}, kinds[f.from].map(o =>
          el("option", { value: o.value, text: o.label })));
        input.value = data[f.name] || kinds[f.from][0].value;
      } else if (f.kind === "text") {
        input = el("textarea", { rows: 2 });
        input.value = data[f.name] || "";
      } else {
        const type = f.kind === "date" ? "text" : (f.kind === "number" || f.kind === "percent" ? "number" : "text");
        input = el("input", {
          type,
          // Дата вводится как в срезе — 04.09.2026. Родное поле даты браузера
          // показывает её в другом порядке, и человек, сверяясь с экраном,
          // печатал бы одно, а видел другое.
          placeholder: f.kind === "date" ? "дд.мм.гггг" : "",
        });
        input.value = data[f.name] === undefined ? "" : String(data[f.name]);
      }

      inputs[f.name] = input;
      box.append(el("label", { class: "rowset__field rowset__field--" + f.kind },
        el("span", { class: "rowset__label", text: f.label }), input));
    }

    const entry = { inputs, box };
    values.push(entry);
    box.append(el("button", {
      class: "rowset__drop", type: "button", title: "Убрать строку", text: "✕",
      onclick: () => {
        entry.dropped = true;
        box.remove();
      },
    }));
    list.append(box);
  };

  for (const r of rows) addRow(r);

  body.append(list, el("button", {
    class: "btn", type: "button", text: "+ добавить",
    onclick: () => addRow({}),
  }));

  return openModal(dlg, extra, () => ({
    items: values.filter(v => !v.dropped).map(v => {
      const out = {};
      for (const f of spec) {
        const input = v.inputs[f.name];
        if (f.kind === "check") out[f.name] = input.checked;
        else if (f.kind === "percent") out[f.name] = (Number(input.value) || 0) / 100;
        else if (f.kind === "number") out[f.name] = Number(input.value) || 0;
        else out[f.name] = input.value.trim();
      }
      return out;
    }),
  }));
}

// openModal показывает диалог и разрешается собранными значениями.
//
// Общая часть двух форм — одиночной и списочной. Третья кнопка появляется
// только там, где ей есть что делать: «вернуть как было» у неправленого поля
// предлагало бы отменить то, чего не было.
function openModal(dlg, extra, collect) {
  const foot = dlg.querySelector(".modal__foot");
  const old = foot.querySelector(".modal__extra");
  if (old) old.remove();
  if (extra) {
    foot.prepend(el("button", {
      class: "btn btn--danger modal__extra", value: extra.value, text: extra.text,
      formnovalidate: true,
    }));
  }

  dlg.returnValue = "";
  dlg.showModal();

  return new Promise(resolve => {
    dlg.addEventListener("close", function done() {
      dlg.removeEventListener("close", done);
      const action = dlg.returnValue;
      if (action !== "ok" && (!extra || action !== extra.value)) {
        resolve(null);
        return;
      }
      resolve({ ...collect(), action });
    });
  });
}

// listFix — кнопка правки рядом с подзаголовком списка.
function listFix(sl, field) {
  if (!sl.edit.some(e => e.field === field)) return null;
  return el("button", {
    class: "fix fix--list", type: "button", title: "Поправить список",
    text: sl.editedFields.includes(field) ? "✎ поправлено" : "✎",
    onclick: () => editList(field),
  });
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

// drawChats рисует закреплённые чаты задачи.
//
// Чатов бывает несколько: обсуждение задачи ведут внутри, а с клиентом говорят
// в контакт-центре, и обе переписки относятся к одной работе. Поэтому каждый
// чат отдельной строкой, а не все через точку в один абзац: строку с
// перепиской, которую пора открепить, надо уметь найти глазами.
function drawChats(list) {
  const box = el("div", { class: "chats" });

  for (const c of list || []) {
    // Подписи собраны сервером: и «задача Bitrix24 №4», и «сообщения до №46»
    // приходят строками, чтобы не форматировать их здесь второй раз.
    const notes = [c.taskRef, c.syncText].filter(Boolean).join(", ");
    box.append(el("div", { class: c.client ? "chats__row chats__row--client" : "chats__row" },
      el("span", { class: "chats__kind", text: c.kindLabel || "чат" }),
      el("span", { class: "chats__title", text: c.title }),
      notes ? el("span", { class: "chats__note", text: notes }) : null,
      el("button", {
        class: "linkish linkish--drop",
        type: "button",
        text: "открепить",
        onclick: () => unpinChat(c),
      })));
  }

  box.append(el("button", {
    class: "linkish chats__add",
    type: "button",
    text: "+ прикрепить чат",
    onclick: pinChat,
  }));
  return box;
}

// pinChat прикрепляет к задаче ещё один чат портала.
//
// Обычно это переписка с клиентом из контакт-центра. Список чатов портала
// показывается для выбора, но полагаться только на него нельзя: im.recent.list
// отдаёт недавние чаты владельца вебхука, а переписку контакт-центра ведут
// операторы, и владельца вебхука в ней может не быть ни одного сообщения.
// Поэтому рядом со списком — поле для номера: его видно в адресной строке
// портала, и это единственный надёжный путь.
async function pinChat() {
  let options = [["", "— выбрать из списка портала —"]];
  let note = "";
  try {
    const res = await api("/api/bitrix/chats");
    note = res.note || "";
    options = options.concat(res.chats.map(c => [c.dialogId, c.label]));
  } catch (e) {
    note = "список чатов не загрузился: " + e.message;
  }

  const got = await ask("Прикрепить чат", [
    { name: "pick", label: "Чат портала", kind: "select", options, note },
    {
      name: "dialogId", label: "Или номер чата",
      hint: "например 28 или chat28",
      note: "Номер виден в адресной строке портала. Пригодится, когда переписки " +
        "контакт-центра нет в списке: в неё владелец вебхука мог ни разу не писать.",
    },
  ]);
  if (!got) return;

  // Набранный номер главнее выбранного из списка: если человек его напечатал,
  // он и есть ответ, а список мог остаться на прежнем пункте.
  const dialogId = got.dialogId || got.pick;
  if (!dialogId) {
    flash("Не выбран чат: выберите из списка или наберите номер.");
    return;
  }

  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/chats", {
      method: "POST",
      body: JSON.stringify({ dialogId }),
    });
    await open(state.current);
  } catch (e) {
    flash(e.message);
  }
}

// unpinChat снимает чат с задачи.
//
// Перенесённые сообщения и заведённые из них источники остаются: открепление
// означает «больше отсюда не читаем», а не «этого не было». Срез, собранный на
// этой переписке, обязан продолжать ею объясняться.
async function unpinChat(c) {
  const what = "Открепить «" + c.title + "»?";
  const why = "Уже перенесённые сообщения и заведённые из них источники останутся: " +
    "срез, собранный на них, должен продолжать объясняться. Новые сообщения " +
    "из этого чата подтягиваться перестанут.";
  if (!window.confirm(what + "\n\n" + why)) return;

  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) +
      "/chats?dialog=" + encodeURIComponent(c.dialogId), { method: "DELETE" });
    await open(state.current);
  } catch (e) {
    flash(e.message);
  }
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

// tick — строка списка с пометкой слева. Номер, если он есть, идёт отдельной
// колонкой, а не приклеивается к тексту: приклеенный, он ломал выключку —
// вторая строка длинного критерия начиналась под цифрой, а не под словом.
function tick(box, met, body, note, n) {
  return el("li", { class: met ? "tick tick--met" : "tick" },
    el("span", { class: met ? "tick__box tick__box--met" : "tick__box", text: box }),
    n ? el("span", { class: "tick__n", text: n }) : null,
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

// sub — подзаголовок части раздела. Счёт стоит рядом с ним, а не под списком:
// десять одинаковых строк не говорят, сколько их и сколько закрыто, пока не
// пересчитаешь глазами.
function sub(text, note, fix) {
  return el("div", { class: "sub" },
    el("span", { text }),
    note ? el("span", { class: "sub__note", text: note }) : null,
    fix);
}
// --- разделы среза ---

// block рисует часть раздела: подзаголовок со счётом и кнопкой правки, а под
// ним сам список.
//
// Пустой список тоже показывается, если поле правимое. Иначе в него нечем
// добавить первую строку: раздела без содержимого на экране просто нет, и
// нажать в нём не на что.
function block(sl, field, title, note, draw) {
  const rows = rowsOf(field, sl);
  const editable = sl.edit.some(e => e.field === field);
  if (!rows.length && !editable) return [];

  const out = [sub(title, rows.length ? note : "пусто", listFix(sl, field))];
  if (rows.length) out.push(draw());
  return out;
}

function drawPassport(sl) {
  const p = sl.passport;
  // Названия здесь нет намеренно: оно стоит заголовком страницы, и повторять
  // его строкой значит начинать раздел с того, что читатель только что прочёл.
  // Пометка происхождения названия при этом не теряется — она у заголовка.
  const rows = spec([
    ["Автор постановки", p.author],
    ["Исполнитель", p.assignee],
    ["Поставлена", p.openedAt],
    ["Срок", p.deadline],
  ]);

  const shifts = block(sl, "passport.shifts", "Переносы срока", p.shifts.length, () =>
    el("ul", { class: "list" }, p.shifts.map(s => {
      const moved = [s.from, s.to].filter(Boolean).join(" → ") || "перенос";
      return el("li", { class: s.explained ? "card" : "card card--warn" },
        el("div", { class: "card__top" },
          el("span", { class: "card__title", text: moved }),
          el("span", { class: s.explained ? "tag" : "tag tag--amber", text: s.explained ? "объяснён" : "без объяснения" }),
          s.at ? el("span", { class: "card__meta", text: s.at }) : null),
        s.movedText || s.comment
          ? el("div", { class: "card__body", text: [s.movedText, s.comment].filter(Boolean).join(" — ") })
          : null);
    })));

  return sec("1", "Паспорт задачи", null, rows, shifts);
}

function drawGoal(sl) {
  const g = sl.goal;
  // Цель идёт прозой и первой: это единственное место среза, которое читают
  // целиком. В узкой колонке рядом с подписью она выглядела полем формы —
  // ровно тем, что глаз пропускает.
  //
  // «Как поставлено» и «Что имелось в виду» стоят подряд намеренно: расхождение
  // между ними и есть главный вывод раздела, и увидеть его можно, только когда
  // обе формулировки рядом.
  const kids = [lead("Как поставлено", g.asStated), lead("Что имелось в виду", g.clarified)];

  // Счёт стоит у заголовка списка, а не под ним: семь одинаковых квадратиков
  // подряд не говорят, сколько из них закрыто, пока их не пересчитаешь глазами.
  kids.push(block(sl, "goal.criteria", "Критерии приёмки", g.criteriaText, () =>
    ticks(g.criteria.map(c =>
      tick(c.met ? "☑" : "☐", c.met,
        el("span", { class: "tick__text", text: c.text }), c.note, c.n)))));

  kids.push(block(sl, "goal.outOfScope", "Вне задачи", g.outOfScope.length, () =>
    ticks(g.outOfScope.map(v => tick("—", false, val(v))))));

  return sec("2", "Цель и границы", null, kids);
}

function drawStatus(sl) {
  const st = sl.status;
  // Этапа и готовности здесь нет намеренно: обе строки стоят в правой колонке,
  // на виду всё время чтения. Повтор занимал первый экран раздела — тот, с
  // которого начинают читать, — справкой вместо плана работ.
  const kids = [];

  kids.push(block(sl, "status.milestones", "Этапы плана", st.milestones.length, () =>
    el("div", { class: "steps" }, st.milestones.map(m =>
      el("div", { class: "step" },
        el("div", {},
          el("div", { class: "step__title", text: m.title }),
          bar(m.share, true)),
        el("div", { class: m.overdueText ? "step__due step__due--late" : "step__due", text: m.overdueText || m.due || "" }),
        el("div", { class: m.done ? "step__pct step__pct--done" : "step__pct", text: m.progress }))))));

  kids.push(block(sl, "status.done", "Сделано", st.done.length, () =>
    ticks(st.done.map(v => tick("✓", true, val(v))))));

  kids.push(block(sl, "status.left", "Осталось", st.left.length, () =>
    ticks(st.left.map(v => tick("·", false, val(v))))));

  return sec("3", "Статус и план", null, kids);
}

function drawTrouble(sl) {
  const kids = [];

  kids.push(block(sl, "blockers", "Блокеры", sl.blockers.length, () =>
    el("ul", { class: "list" }, sl.blockers.map(b =>
      el("li", { class: "card card--warn" },
        el("div", { class: "card__top" },
          el("span", { class: "card__title", text: b.summary }),
          el("span", { class: "tag tag--red", text: b.kindLabel }),
          b.ageText ? el("span", { class: "card__meta", text: b.ageText }) : null),
        b.dependsOn ? el("div", { class: "card__body", text: "ждёт: " + b.dependsOn }) : null,
        el("div", { class: "card__body" }, val(b.evidence)))))));

  kids.push(block(sl, "risks", "Риски", sl.risks.length, () =>
    el("ul", { class: "list" }, sl.risks.map(r =>
      el("li", { class: "card" },
        el("div", { class: "card__top" },
          el("span", { class: "card__title", text: r.summary }),
          r.impactText ? el("span", { class: "tag tag--amber", text: r.impactText }) : null,
          r.spread ? el("span", { class: "card__meta", text: r.spread }) : null),
        el("div", { class: "card__body" }, val(r.evidence)))))));

  return sec("4", "Блокеры и риски", null, kids);
}

function drawActions(sl) {
  const pm = sl.pmActions;
  const kids = [];

  kids.push(block(sl, "pmActions.needed", "Действия", pm.needed.length, () =>
    el("ul", { class: "list" }, pm.needed.map(a =>
      el("li", { class: "card" },
        el("div", { class: "card__top" },
          el("span", { class: "tag tag--blue", text: a.kindLabel }),
          el("span", { class: "card__title", text: a.text })),
        a.why ? el("div", { class: "card__body", text: a.why }) : null)))));

  kids.push(sub("Следующая проверка"));
  kids.push(el("div", { class: "lead__text" }, val(pm.nextCheck)));

  if (pm.comment) {
    kids.push(el("div", { class: "quote", style: "margin-top:10px" }, el("span", { text: pm.comment })));
  }
  return sec("5", "Что делать PM", null, kids);
}

function drawQuestions(sl) {
  const list = sl.questions;
  const open = list.filter(q => !q.answered).length;
  return sec("6", "Вопросы специалисту",
    list.length ? (open ? "без ответа: " + open : "все закрыты") : null,
    block(sl, "questions", "Вопросы", list.length, () =>
      ticks(list.map(q => {
        const body = el("div", {}, el("span", { class: "tick__text", text: q.text }));
        if (q.answered) body.append(val(q.answer));
        return tick(q.answered ? "☑" : "☐", q.answered, body,
          q.unlocks ? "разблокирует: " + q.unlocks : null, q.n);
      }))));
}

function drawArtifacts(sl) {
  const list = sl.artifacts;
  return sec(null, "Артефакты",
    list.length ? list.filter(a => a.present).length + " из " + list.length + " на руках" : null,
    block(sl, "artifacts", "Файлы", list.length, () =>
      ticks(list.map(a => tick(a.present ? "☑" : "☐", a.present,
        el("span", { class: "tick__text", text: a.name + (a.sizeText ? " · " + a.sizeText : "") }),
        a.present ? null : a.wouldGive)))));
}

function drawSlice(sl) {
  // Сводки разделов приходят из шапки среза: там они уже посчитаны и склонены.
  // Считать их второй раз здесь значило бы дать двум числам разойтись — ровно
  // тому, ради чего весь слой представления и заведён на сервере.
  return el("div", { class: "slice" },
    drawPassport(sl),
    drawGoal(sl),
    drawStatus(sl),
    drawTrouble(sl),
    drawActions(sl),
    drawQuestions(sl),
    drawArtifacts(sl));
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

// render раскладывает страницу задачи в две колонки: чтение слева, состояние и
// действия справа.
//
// Раньше всё шло одной лентой: сводка сверху, под ней ряд кнопок, под ним срез.
// У длинной задачи это значило, что через полтора экрана прокрутки на виду не
// остаётся ни готовности, ни срока, ни блокеров — а срез читают именно ради них
// и сверяются с ними по ходу. Кнопки уезжали туда же, и «удалить версию» никто
// не находил.
//
// Правая колонка прибита к верху окна и не уезжает. В ней всё, что отвечает на
// вопрос «где мы сейчас» и «что с этим делать»; слева остаётся только то, что
// читают подряд.
function render() {
  const b = state.board;

  const panels = [
    ["slice", "Срез", null, () => drawSlice(b.slice)],
    ["flows", "Схемы", b.diagrams.length, () => drawDiagrams(b.diagrams)],
    ["sources", "Источники", b.sources.length, () => drawSources(b.sources)],
    ["versions", "Версии", b.versions.length, () => drawVersions(b.versions)],
    ["facts", "Факты", b.facts, () => drawFacts(b.task.id)],
  ];

  const main = el("div", { class: "col" },
    el("div", { class: "crumb", text: b.task.project }),
    el("h1", { text: b.slice.head.title }),
    // Сбой разбора — строкой на самой странице, а не отказом всей страницы.
    // Иначе до задачи со сломавшимся разбором не добраться, а добраться надо
    // именно тогда: посмотреть источники, снять чат, удалить лишнюю.
    b.buildError
      ? el("div", { class: "err" },
        el("div", { text: "Срез не собран: " + b.buildError }),
        el("div", { class: "crumb", text: "Ниже — задача без среза. Попробуйте пересобрать: " +
          "отказ шлюза бывает временным." }))
      : null,
    drawChats(b.chats),
    ...tabbed(panels, "slice", "tab"));

  const page = el("div", { class: "page page--split" }, main, drawRail(b));
  $("#main").replaceChildren(page);
}

// drawRail собирает правую колонку: состояние задачи и действия над ней.
function drawRail(b) {
  const h = b.slice.head;
  const rail = el("aside", { class: "rail" });

  rail.append(el("div", { class: "rail__box" },
    el("div", { class: "rail__label", text: "Готовность" }),
    el("div", { class: "rail__big" }, val(h.readiness)),
    bar(h.readinessShare),
    el("div", { class: "rail__row" },
      el("span", { text: "Этап" }),
      el("span", { class: "rail__v" }, val(h.stage))),
    h.deadline
      ? el("div", { class: h.overdue ? "rail__row rail__row--warn" : "rail__row" },
        el("span", { text: "Срок" }),
        el("span", { class: "rail__v" }, h.deadline,
          h.deadlineNote ? el("div", { class: "crumb", text: h.deadlineNote }) : null))
      : null,
    h.budget
      ? el("div", { class: "rail__row" },
        el("span", { text: "Бюджет" }), el("span", { class: "rail__v", text: h.budget }))
      : null));

  const warns = drawWarns(h);
  if (warns) rail.append(el("div", { class: "rail__box" }, warns));

  rail.append(el("div", { class: "rail__box" },
    el("button", {
      class: "btn btn--primary btn--wide",
      type: "button",
      text: "Пересобрать срез",
      onclick: ev => rebuild(ev.currentTarget),
    }),
    el("button", { class: "btn btn--wide", type: "button", text: "Добавить источник", onclick: addSource }),
    // Кнопка есть только у задачи с закреплённым чатом: подтягивать неоткуда,
    // а кнопка, которая всегда отвечает «чата нет», — обещание, которого
    // интерфейс не сдержит.
    b.chats.length
      ? el("button", {
        class: "btn btn--wide",
        type: "button",
        text: "Подтянуть переписку",
        onclick: ev => pull(ev.currentTarget),
      })
      : null,
    // Выгрузка — ради неё срез и делают: его показывают заказчику и на
    // планёрке. До сих пор он жил только во вкладке браузера.
    el("div", { class: "rail__sep" }),
    el("button", { class: "btn btn--wide", type: "button", text: "Скопировать текстом", onclick: ev => copySlice(ev.currentTarget) }),
    el("a", {
      class: "btn btn--wide btn--link",
      href: "/api/tasks/" + encodeURIComponent(state.current) + "/slice.md",
      text: "Скачать файлом",
    }),
    el("button", { class: "btn btn--wide", type: "button", text: "Печать и PDF", onclick: () => window.print() }),

    // Удаление стоит здесь, а не в глубине вкладки «Версии», где его никто не
    // находил. Оно необратимое, поэтому отделено чертой и набрано красным.
    el("div", { class: "rail__sep" }),
    // Версии нулевой не бывает: ноль означает, что срез не собран вовсе, и
    // удалять нечего.
    h.version > 0
      ? el("button", {
        class: "btn btn--wide btn--danger",
        type: "button",
        text: "Удалить версию v" + h.version,
        onclick: () => dropVersion(h.version, b.versions.length === 1),
      })
      : null,
    el("button", {
      class: "btn btn--wide btn--danger",
      type: "button",
      text: "Удалить задачу",
      onclick: () => dropTask(b.task),
    }),
    b.slice.editedFields.length
      ? el("button", {
        class: "btn btn--wide",
        type: "button",
        text: "Снять все правки",
        onclick: () => dropCorrections(""),
      })
      : null));

  // Разбор и правка названы отдельно: поправленная версия всё равно стоит на
  // разборе — правка меняет одно поле из двадцати.
  rail.append(el("div", { class: "rail__note" },
    el("div", { text: "срез v" + h.version + " · " + h.builtAt }),
    el("div", { text: "разбор: " + h.analyst }),
    h.editedBy ? el("div", { text: "правка: " + h.editedBy }) : null,
    el("div", { text: "фактов в журнале: " + b.facts })));

  return rail;
}

// tabbed собирает ряд вкладок и тело под ним.
//
// Панели — [ключ, подпись, число у подписи, построить]. Построить может вернуть
// и обещание: журнал фактов запрашивается отдельно, он нужен редко и тащить его
// с каждым открытием задачи незачем.
//
// Ключ выбранной вкладки живёт в state под именем slot, а не внутри этой
// функции: иначе после пересборки среза страница возвращалась бы на первую
// вкладку, хотя человек смотрел третью.
function tabbed(panels, fallback, slot) {
  const tabs = el("div", { class: "tabs", role: "tablist" });
  const body = el("div", {});

  const show = key => {
    state[slot] = key;
    for (const btn of tabs.children) {
      btn.setAttribute("aria-selected", String(btn.dataset.tab === key));
    }
    const out = panels.find(p => p[0] === key)[3]();
    if (out instanceof Promise) {
      body.replaceChildren(el("div", { class: "empty", text: "Загрузка…" }));
      out.then(node => {
        // Пока ждали, могли уйти на другую вкладку — тогда ответ уже не к месту.
        if (state[slot] === key) body.replaceChildren(node);
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
      onclick: () => show(key),
    }, label, count ? el("span", { class: "tab__n", text: count }) : null));
  }

  show(panels.some(p => p[0] === state[slot]) ? state[slot] : fallback);
  return [tabs, body];
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
function ask(title, fields, extra) {
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
      // Через свойство, а не через атрибут: у textarea начальный текст — это
      // содержимое узла, и атрибут value на нём не значит ничего.
      if (f.value) input.value = f.value;
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
      // Через свойство, а не через атрибут: выбранный пункт списка — это его
      // состояние, и атрибут на самом select ничего не значит.
      if (f.value !== undefined && f.value !== null) input.value = f.value;
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

  const answer = openModal(dlg, extra, () => {
    const out = {};
    for (const [name, input] of Object.entries(inputs)) out[name] = input.value.trim();
    return out;
  });

  const first = Object.values(inputs)[0];
  if (first) first.focus();
  return answer;
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
        : null,
      el("button", {
        class: "btn btn--small btn--danger",
        type: "button",
        text: "удалить",
        onclick: () => dropVersion(v.version, list.length === 1),
      }));
    box.append(row);
  });

  return el("div", {}, box, out);
}

// copySlice кладёт срез текстом в буфер обмена.
//
// Текст берётся у сервера, а не собирается из разметки страницы: у одного среза
// должно быть одно изложение, и собранное здесь разошлось бы с файлом на первой
// же правке вёрстки.
async function copySlice(btn) {
  btn.disabled = true;
  const was = btn.textContent;
  try {
    const res = await fetch("/api/tasks/" + encodeURIComponent(state.current) + "/slice.md");
    if (!res.ok) throw new Error(res.status + " " + res.statusText);
    const text = await res.text();

    // Буфер обмена доступен не везде: браузер отдаёт его только по защищённому
    // соединению или на петле. Отказ здесь — не поломка, и человеку надо
    // сказать, чем воспользоваться вместо.
    if (!navigator.clipboard) {
      throw new Error("браузер не даёт доступ к буферу — нажмите «Скачать файлом»");
    }
    await navigator.clipboard.writeText(text);

    btn.textContent = "скопировано";
    setTimeout(() => { btn.textContent = was; }, 1500);
  } catch (e) {
    btn.textContent = was;
    flash(e.message);
  } finally {
    btn.disabled = false;
  }
}

// dropTask убирает задачу целиком.
//
// Единственное место, где реестр расстаётся с материалом, поэтому и спрашиваем
// подробно: что именно уйдёт и что останется. «Вы уверены?» тут не годится —
// человек уверен, он просто не знает, чего лишится.
async function dropTask(t) {
  const what = "Удалить задачу «" + t.title + "»?";
  const why = "Уйдут её источники, факты, схемы, все версии среза и правки. " +
    "Останутся записи журнала инцидентов — случай относится к человеку и дню, " +
    "а задача в нём только место.\n\nОтменить это будет нельзя.";
  if (!window.confirm(what + "\n\n" + why)) return;

  try {
    await api("/api/tasks/" + encodeURIComponent(t.id), { method: "DELETE" });
    // Возвращаемся к списку: страницы удалённой задачи больше нет, и оставить
    // её на экране значило бы показывать то, чего в реестре уже не существует.
    state.current = "";
    state.board = null;
    location.hash = "";
    await boot();
  } catch (e) {
    flash(e.message);
  }
}

// dropVersion снимает версию среза.
//
// Спрашиваем подтверждение, потому что это единственное необратимое действие в
// реестре: всё остальное только пополняется. Материал при этом остаётся —
// источники и факты не трогаются, и срез собирается из них заново.
async function dropVersion(version, last) {
  const what = "Удалить версию v" + version + "?";
  // Про деньги предупреждаем прямо: сняв единственную версию, задачу нельзя
  // открыть, не собрав срез заново, а сборка — платный вызов модели.
  const why = last
    ? "Она единственная, и при следующем открытии задачи срез соберётся заново — " +
      "это платный разбор. Источники и факты останутся на месте."
    : "Текущей станет предыдущая версия. Источники и факты останутся: " +
      "пропадёт только эта собранная картина.";
  if (!window.confirm(what + "\n\n" + why)) return;

  try {
    await api("/api/tasks/" + encodeURIComponent(state.current) + "/slices/" + version, {
      method: "DELETE",
    });
    await open(state.current);
  } catch (e) {
    flash(e.message);
  }
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
    state.journal = await api("/api/incidents" + periodQuery());
    renderJournal();
  } catch (e) {
    $("#main").replaceChildren(el("div", { class: "empty" }, el("div", { class: "err", text: e.message })));
  }
}

// periodQuery — выбранный отрезок в виде запроса. Пустой означает «показать
// всё»: это законный вид журнала, а не забытый фильтр.
function periodQuery() {
  const p = state.period;
  const parts = [];
  if (p.from) parts.push("from=" + encodeURIComponent(p.from));
  if (p.to) parts.push("to=" + encodeURIComponent(p.to));
  return parts.length ? "?" + parts.join("&") : "";
}

// month сдвигает выбранный отрезок на месяц: оценку ставят помесячно, и
// набирать две даты руками ради «прошлый месяц» человек не должен.
function setMonth(shift) {
  const now = new Date();
  const first = new Date(now.getFullYear(), now.getMonth() + shift, 1);
  const last = new Date(now.getFullYear(), now.getMonth() + shift + 1, 0);
  const iso = d => d.toISOString().slice(0, 10);
  state.period = { from: iso(first), to: iso(last) };
  openJournal();
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
    drawPeriod(j),
    el("div", { class: "actions" },
      el("button", {
        class: "btn btn--primary",
        type: "button",
        text: "Записать случай",
        onclick: addIncident,
      }),
      el("a", {
        class: "btn btn--link",
        href: "/api/incidents.md" + periodQuery(),
        text: "Выгрузить к разговору",
      }),
      el("span", { class: "actions__note", text: j.text })));

  if (!j.incidents.length) {
    page.append(el("div", { class: "empty", text: state.period.from || state.period.to
      ? "За этот отрезок записей нет." : "Пока ни одного случая." }));
    $("#main").replaceChildren(page);
    return;
  }

  page.append(...tabbed([
    ["rows", "Записи", j.incidents.length, () => journalTable(j.incidents)],
    ["people", "По людям", j.people.length, () => journalPeople(j)],
    ["blocks", "По блокам KPI", null, () => journalBlocks(j)],
  ], "rows", "journalTab"));
  $("#main").replaceChildren(page);
}

// drawPeriod — выбор отрезка. Оценку ставят за месяц, и журнал целиком для
// разговора не годится: к третьему месяцу в нём полсотни записей, из которых к
// делу относится десяток.
function drawPeriod(j) {
  const box = el("div", { class: "period" });

  const input = (name, label) => {
    const el_ = el("input", { type: "date", value: state.period[name] || "" });
    el_.addEventListener("change", () => {
      state.period[name] = el_.value;
      openJournal();
    });
    return el("label", { class: "period__field" },
      el("span", { class: "period__label", text: label }), el_);
  };

  // Через put, а не через append: append превращает null в строку «null» и
  // печатает её на экране, а необязательных кнопок здесь две.
  put(box, [
    el("button", { class: "btn btn--small", type: "button", text: "Этот месяц", onclick: () => setMonth(0) }),
    el("button", { class: "btn btn--small", type: "button", text: "Прошлый месяц", onclick: () => setMonth(-1) }),
    input("from", "с"),
    input("to", "по"),
    state.period.from || state.period.to
      ? el("button", {
        class: "btn btn--small", type: "button", text: "весь журнал",
        onclick: () => { state.period = {}; openJournal(); },
      })
      : null,
    j.periodText ? el("span", { class: "period__note", text: j.periodText }) : null,
  ]);
  return box;
}

// journalPeople — разрез по людям. Ради него журнал и ведут: оценку ставят
// человеку за месяц, а записи лежат по дням и по проектам, и сложить их глазами
// при полусотне строк нельзя.
function journalPeople(j) {
  const box = el("div", { class: "blocks" });
  for (const p of j.people) {
    // Эскалации названы рядом со счётом намеренно: без них разрез читался бы
    // обвинительным списком, а половина записей объясняет срыв, а не обвиняет.
    const counts = [p.countText, "в оценку идёт " + p.counted];
    if (p.external) counts.push("вне зоны контроля " + p.external);
    if (p.escalated) counts.push("сообщил сам " + p.escalated);

    box.append(el("div", { class: "kpi" },
      el("div", { class: "kpi__head" },
        el("span", { class: "kpi__title", text: p.employee }),
        el("span", { class: "kpi__count", text: counts.join(" · ") })),
      bar(p.share),
      (p.blocks || []).length
        ? el("div", { class: "kpi__people", text: p.blocks.join(" · ") })
        : null,
      (p.projects || []).length
        ? el("div", { class: "kpi__people", text: "проекты: " + p.projects.join(", ") })
        : null));
  }
  return box;
}

// journalBlocks показывает, где случаи накопились.
//
// Ради этого журнал и ведут: одна запись — повод для разговора, десять по
// одному блоку — повод менять работу. Читая записи подряд, этого не увидеть:
// они лежат по дням, а вопрос стоит по блокам.
function journalBlocks(j) {
  const box = el("div", { class: "blocks" });
  if (j.statsText) box.append(el("div", { class: "blocks__lead", text: j.statsText }));

  // people у пустого блока сервер не присылает вовсе: перечислять некого.
  for (const s of j.stats) {
    const people = s.people || [];
    // Отведённые случаи названы отдельной строкой, а не вычтены молча: решение
    // по блоку принимают по числу зачтённых, а объясняют отведёнными.
    const counts = [s.countText];
    if (s.external) counts.push("в оценку идёт " + s.counted + ", вне зоны контроля " + s.external);

    box.append(el("div", { class: s.note ? "kpi kpi--empty" : "kpi" },
      el("div", { class: "kpi__head" },
        el("span", { class: "kpi__title", text: s.label }),
        el("span", { class: "kpi__weight", text: "вес " + s.weightText }),
        el("span", { class: s.note ? "kpi__count miss" : "kpi__count", text: s.note || counts.join(" · ") })),
      bar(s.share),
      people.length ? el("div", { class: "kpi__people", text: people.join(" · ") }) : null));
  }
  return box;
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
      el("td", { class: "jr__who" },
        el("div", { text: in_.recordedBy }),
        // Правка и удаление стоят у самой записи: искать их в отдельном меню
        // ради опечатки в фамилии никто не станет, а запись с опечаткой хуже
        // отсутствующей — спорить будут с ней, а не с делом.
        el("div", { class: "jr__acts" },
          el("button", { class: "linkish", type: "button", text: "поправить", onclick: () => editIncident(in_) }),
          el("button", { class: "linkish linkish--drop", type: "button", text: "удалить", onclick: () => dropIncident(in_) }))));
  });

  return el("div", { class: "sheet" },
    el("table", { class: "journal" },
      el("thead", {}, head),
      el("tbody", {}, rows)));
}

// Даты в форме — в том виде, в каком их ждёт поле ввода. В журнале они
// показаны как 04.09.2026, а <input type=date> понимает только ISO.
function isoDate(text) {
  const m = /^(\d{2})\.(\d{2})\.(\d{4})$/.exec(text || "");
  return m ? m[3] + "-" + m[2] + "-" + m[1] : "";
}

// incidentFields — форма случая. Одна на запись и на правку: разойдясь, они
// дали бы журнал, в который нельзя внести то, что в нём уже лежит.
function incidentFields(j, in_) {
  const v = in_ || {};
  // Задача реестра необязательна: журнал ведут по всем работам сразу, а в
  // реестр заведены единицы. Пустой первый пункт — это «случай не в задаче
  // реестра», а не пропущенное поле.
  const tasks = [["", "— не в задаче реестра —"], ...state.tasks.map(t => [t.id, t.title])];

  return [
    { name: "at", label: "Дата", kind: "date", required: true, hint: "когда случилось", value: isoDate(v.at) },
    { name: "employee", label: "Специалист", required: true, hint: "кого касается", value: v.employee },
    { name: "project", label: "Проект", required: true, hint: "работа, в которой это случилось", value: v.project },
    { name: "taskId", label: "Задача реестра", kind: "select", options: tasks, value: v.taskId },
    // Ровно один блок: один случай не должен съедать несколько блоков сразу.
    { name: "block", label: "Блок KPI", kind: "select", options: j.blocks.map(b => [b.value, b.label]), value: v.block },
    { name: "text", label: "Что произошло", kind: "text", required: true, hint: "факты и последствия, а не оценка", value: v.text },
    {
      name: "control", label: "В зоне контроля специалиста", kind: "select",
      options: [["yes", "да"], ["no", "нет — помешал клиент или внешний фактор"]],
      value: v.external ? "no" : "yes",
      note: "«Нет» оставит случай в журнале как объяснение, но в оценку он не пойдёт.",
    },
    {
      name: "escalated", label: "Была эскалация", kind: "select",
      options: [["", "нет"], ["yes", "да — сообщили наверх"]],
      value: v.escalatedText && v.escalatedText !== "нет" ? "yes" : "",
      note: "Эскалация меняет знак случая: штрафуется не проблема, а молчание о ней.",
    },
    { name: "escalatedAt", label: "Когда эскалировано", kind: "date" },
    { name: "managerNote", label: "Комментарий руководителя", kind: "text", value: v.managerNote },
  ];
}

// incidentBody собирает тело запроса. Подпись «кто зафиксировал» не
// отправляется ни при записи, ни при правке: её ставит сервер из пропуска.
function incidentBody(got) {
  return JSON.stringify({
    ...got,
    external: got.control === "no",
    escalated: got.escalated === "yes",
  });
}

async function addIncident() {
  const got = await ask("Случай", incidentFields(state.journal, null));
  if (!got) return;

  try {
    await api("/api/incidents", { method: "POST", body: incidentBody(got) });
    await openJournal();
  } catch (e) {
    flash(e.message);
  }
}

// editIncident правит запись журнала.
//
// Журнал — рабочий документ руководителя, а не летопись событий. Запись с
// опечаткой в фамилии или с датой не того дня хуже отсутствующей: её показывают
// человеку как основание, и спорить он будет с опечаткой, а не с делом.
async function editIncident(in_) {
  const got = await ask("Поправить случай", incidentFields(state.journal, in_));
  if (!got) return;

  try {
    await api("/api/incidents/" + encodeURIComponent(in_.id), {
      method: "PUT",
      body: incidentBody(got),
    });
    await openJournal();
  } catch (e) {
    flash(e.message);
  }
}

// dropIncident убирает запись журнала.
async function dropIncident(in_) {
  const what = "Удалить запись от " + in_.at + " по «" + in_.employee + "»?";
  if (!window.confirm(what + "\n\nОтменить это будет нельзя.")) return;

  try {
    await api("/api/incidents/" + encodeURIComponent(in_.id), { method: "DELETE" });
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
    const me = await api("/api/me");
    login = me.login || "";
    // Журнал инцидентов — не для всех: в нём записи о работе конкретных людей.
    // Кнопку прячем, но не в ней дело: доступ закрывает сервер, а здесь просто
    // не предлагаем того, чего человеку всё равно не дадут.
    $("#open-journal").hidden = !me.manager;
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
