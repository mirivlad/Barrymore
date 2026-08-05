// Интерфейс оператора: тонкая поверхность над API.
//
// Здесь нет собственного состояния домена — только отображение того,
// что сообщает runtime, и различение факта, ожидания и вывода.

const $ = (id) => document.getElementById(id);

async function api(path, options = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const text = await res.text();
  let body = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = { detail: text };
  }
  if (!res.ok) {
    const message = body?.detail || body?.title || res.statusText;
    throw new Error(message);
  }
  return body;
}

function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);
}

function when(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString("ru-RU");
}

function ago(ts) {
  if (!ts) return "нет сигнала";
  const seconds = Math.round((Date.now() - new Date(ts).getTime()) / 1000);
  if (Number.isNaN(seconds)) return "нет сигнала";
  if (seconds < 60) return `${seconds} с назад`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} мин назад`;
  return `${Math.round(seconds / 3600)} ч назад`;
}

// ---------- технический режим ----------
//
// 07_USER_EXPERIENCE §1: интерфейс не должен выглядеть как панель оркестратора.
// Глубина доступна по раскрытию — но именно скрыта, а не вычищена: владелец
// в любой момент видит те же данные, что и раньше, без перезагрузки.
const techBox = $("tech-mode");
let techMode = localStorage.getItem("barrymore.tech") === "1";

function applyTech() {
  document.body.classList.toggle("tech", techMode);
  techBox.checked = techMode;
}
techBox.addEventListener("change", () => {
  techMode = techBox.checked;
  localStorage.setItem("barrymore.tech", techMode ? "1" : "0");
  applyTech();
  refresh(activeTab());
  // Открытая карточка перерисовывается тоже: иначе половина экрана осталась бы
  // в прежнем режиме, и владелец решил бы, что переключатель работает через раз.
  if (openThreadID) openThread(openThreadID);
});
applyTech();

// SAY переводит внутренние состояния на человеческий язык.
//
// Служебное имя остаётся доступным в техническом режиме: подменять его
// насовсем значило бы отнять у владельца возможность соотнести увиденное
// с журналом.
const SAY = {
  // нити
  active: "живёт", maturing: "зреет", waiting: "ждёт", blocked: "застряла",
  paused: "не договорили", resolved: "завершена", released: "отпущена",
  archived: "в архиве",
  // исполнители
  available: "доступен", likely_available: "скорее всего доступен",
  unknown: "неизвестно", quota_exhausted: "квота исчерпана",
  auth_required: "нужен вход", payment_confirmation_required: "просит подтвердить оплату",
  offline: "не отвечает", broken: "сломан",
  // поручения
  draft: "черновик", proposed: "предложено", approved: "разрешено",
  preparing: "готовится", running: "выполняется", awaiting_user: "ждёт вас",
  verifying: "проверяется", completed: "выполнено", failed: "не вышло",
  cancelled: "отменено",
  // проверки, ожидания, расхождения
  passed: "пройдена", skipped: "пропущена", pending: "в силе",
  satisfied: "сбылось", expired: "срок вышел", superseded: "заменено",
  open: "открыто", escalated: "передано вам",
  reacting: "восстанавливается", acknowledged: "принято к сведению",
  info: "к сведению", warning: "важно", critical: "тревога",
};

// У расхождения `resolved` значит «закрыто», а не «завершена», как у нити.
// Одно слово на два смысла звучало бы небрежно, поэтому словарь местный.
const SAY_DISCREPANCY = { ...SAY, resolved: "закрыто" };

function say(value, dict = SAY) {
  if (!value) return "—";
  if (techMode) return value;
  return dict[value] || value;
}

// Статусы окрашиваются осторожно: «неизвестно» не выглядит как «хорошо».
const TONE = {
  available: "ok", likely_available: "warn", unknown: "",
  quota_exhausted: "bad", auth_required: "bad", payment_confirmation_required: "warn",
  offline: "bad", broken: "bad",
  completed: "ok", failed: "bad", cancelled: "", running: "warn",
  verifying: "warn", proposed: "", approved: "warn", preparing: "warn",
  passed: "ok", skipped: "", pending: "",
  satisfied: "ok", expired: "bad",
  open: "warn", escalated: "bad", resolved: "ok", reacting: "warn", acknowledged: "",
  info: "", warning: "warn", critical: "bad",
};

function tag(text, tone, dict) {
  const cls = tone ?? TONE[text] ?? "";
  return `<span class="tag ${cls}">${esc(say(text, dict))}</span>`;
}

// techNote показывает служебную подробность только в техническом режиме.
function techNote(html) {
  return html ? `<span class="tech-only muted">${html}</span>` : "";
}

// ---------- вкладки ----------

const tabs = document.querySelectorAll("nav button");

function activeTab() {
  const current = document.querySelector('nav button[aria-current="true"]');
  return current ? current.dataset.tab : "talk";
}

function showTab(name, remember = true) {
  const btn = [...tabs].find((b) => b.dataset.tab === name);
  if (!btn) return;
  tabs.forEach((b) => b.setAttribute("aria-current", String(b === btn)));
  document.querySelectorAll("main section").forEach((s) => {
    s.hidden = s.id !== `tab-${name}`;
  });
  if (remember) localStorage.setItem("barrymore.tab", name);
  refresh(name);
}

tabs.forEach((btn) => btn.addEventListener("click", () => showTab(btn.dataset.tab)));

// Восстановление места живёт в конце файла: к тому времени определено всё,
// что оно трогает.
function restorePlace() {
  const savedTab = localStorage.getItem("barrymore.tab");
  if (savedTab && savedTab !== "talk") showTab(savedTab, false);

  // Открытая карточка нити тоже восстанавливается: возвращаться к списку и
  // заново искать нить после каждой перезагрузки — мелкая, но ежедневная обида.
  const savedThread = localStorage.getItem("barrymore.thread");
  if (savedThread && savedTab === "threads") openThread(savedThread);
}

// ---------- состояние ----------

// localModelCell показывает три разных положения дел, не смешивая их:
// модель отвечает, модель поднимается, модели нет. Загрузка весов идёт минутами,
// и выдавать её за отказ значило бы врать.
function localModelCell(m) {
  if (!m || !m.configured) {
    return `${tag("не ведётся", "")} <span class="muted">сервер модели поднимает владелец</span>`;
  }
  let state;
  if (m.serving) state = tag("отвечает", "ok");
  else if (m.loading) state = tag("поднимается", "warn");
  else state = tag("не работает", "bad");

  // Происхождение процесса отдельной строкой не выводится: причина уже
  // говорит, поднял его Бэрримор или нет, и повтор только зашумлял бы.
  const controls = m.serving
    ? m.managed
      ? `<button class="ghost" data-model-action="stop">остановить</button>`
      : ""
    : `<button class="ghost" data-model-action="start">поднять</button>`;

  return `${state} <span class="muted">${esc(m.reason || "")}</span>
    ${m.endpoint ? `<div class="muted">${esc(m.endpoint)}</div>` : ""}
    ${controls ? `<div style="margin-top:6px">${controls}</div>` : ""}`;
}

// Управление моделью делегируется от таблицы: разметка состояния
// перерисовывается целиком, и обработчики на кнопках не пережили бы обновления.
$("state-body").addEventListener("click", async (e) => {
  const action = e.target?.dataset?.modelAction;
  if (!action) return;
  e.target.disabled = true;
  try {
    await api(`/api/v1/local-model/${action}`, { method: "POST" });
  } catch (err) {
    alert(`Не вышло: ${err.message}`);
  }
  loadState();
});

async function loadState() {
  try {
    const s = await api("/api/v1/system/state");
    const iso = s.isolation || {};
    $("state-body").innerHTML = `
      <table>
        <tr><th>Изоляция запусков</th><td>${
          iso.bwrap
            ? `bubblewrap ${tag("доступен", "ok")}`
            : `bubblewrap ${tag("недоступен", "bad")} — audit-only поручения запускаться не будут`
        }</td></tr>
        <tr><th>Супервизия</th><td>${
          iso.systemd_run
            ? `systemd --user ${tag("доступен", "ok")}`
            : `systemd --user ${tag("недоступен", "warn")}`
        }</td></tr>
        <tr><th>Разрешённые каталоги</th><td>${
          (s.workspace_roots || []).length
            ? (s.workspace_roots || []).map(esc).join("<br>")
            : `<span class="tag bad">не заданы</span> поручения будут отклоняться`
        }</td></tr>
        <tr><th>Политика стоимости</th><td>${esc(s.model_policy || "—")}</td></tr>
        <tr><th>Память</th><td>${esc(s.memory_policy || "—")}</td></tr>
        <tr><th>Разговорный слой</th><td>${
          tag(s.conversation?.status || "неизвестно", PROVIDER_TONE[s.conversation?.status] || "")
        } ${esc(s.conversation?.reason || "")}</td></tr>
        <tr><th>Локальная модель</th><td>${localModelCell(s.local_model)}</td></tr>
        <tr><th>Событий в журнале</th><td>${esc(s.journal_head)}</td></tr>
        <tr><th>Активных запусков</th><td>${(s.active_runs || []).length}</td></tr>
        <tr><th>Виды ожиданий</th><td class="muted">${(s.expectation_kinds || []).map(esc).join(", ")}</td></tr>
        <tr><th>Локальные реакции</th><td class="muted">${(s.reflex_policies || []).map(esc).join(", ")}</td></tr>
      </table>
      ${
        (s.startup_notes || []).length
          ? `<div class="notes" style="margin-top:12px">${
              s.startup_notes.map((n) => `<div>${esc(n)}</div>`).join("")
            }</div>`
          : ""
      }`;
  } catch (err) {
    $("state-body").innerHTML = `<span class="tag bad">ошибка</span> ${esc(err.message)}`;
  }

  try {
    const d = await api("/api/v1/discrepancies?open=true");
    const items = d.items || [];
    $("discrepancies").innerHTML = items.length
      ? items.map(({ discrepancy: x, attempts }) => `
          <li>
            <div class="row">
              ${tag(x.severity)} ${tag(x.status, null, SAY_DISCREPANCY)}
              <strong>${esc(DISCREPANCY_LABEL[x.kind] || x.kind)}</strong>
              ${techNote(`· ${esc(x.kind)}`)}
              <span class="grow"></span>
              <span class="muted">${esc(x.occurrences)}× · ${ago(x.last_seen)}</span>
            </div>
            <div class="muted" style="margin-top:4px">
              ожидалось: ${esc(x.expected)}<br>наблюдалось: ${esc(x.observed)}
            </div>
            ${
              (attempts || []).length
                ? `<div class="muted" style="margin-top:4px">что Бэрримор пробовал: ${
                    attempts.map((a) => esc(a.detail || `${a.policy_id} #${a.attempt_no}`)).join("; ")
                  }</div>`
                : ""
            }
            ${
              x.status === "escalated"
                ? `<div class="muted" style="margin-top:4px">Восстановить самостоятельно
                   не вышло, поэтому решение за вами.</div>`
                : ""
            }
            <div class="row" style="margin-top:6px">
              ${
                x.subject_type === "worker_run"
                  ? `<button class="ghost" onclick="openRunSubject('${esc(x.subject_id)}')">Открыть поручение</button>`
                  : ""
              }
              <button class="ghost" onclick="ackDiscrepancy('${esc(x.id)}')">Принять к сведению</button>
            </div>
          </li>`).join("")
      : `<li class="muted">расхождений нет</li>`;
  } catch (err) {
    $("discrepancies").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }

  try {
    const a = await api("/api/v1/approvals/pending");
    const items = a.items || [];
    $("approvals").innerHTML = items.length
      ? items.map((x) => `
          <li>
            <div><strong>${esc(x.summary)}</strong></div>
            <div class="muted" style="margin-top:4px">
              исполнитель: ${esc(x.scope?.worker)} ·
              запись: ${esc(x.scope?.write_level)} ·
              сеть: ${x.scope?.network ? "да" : "нет"}<br>
              ${esc(x.scope?.notes || "")}
            </div>
            <div class="row" style="margin-top:8px">
              <button class="act" onclick="grant('${esc(x.id)}')">Разрешить</button>
              <button class="ghost" onclick="deny('${esc(x.id)}')">Отказать</button>
            </div>
          </li>`).join("")
      : `<li class="muted">решений не требуется</li>`;
  } catch (err) {
    $("approvals").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }
}

// Расхождения названы тем, что случилось, а не именем вида в коде.
const DISCREPANCY_LABEL = {
  "worker_run.starts": "исполнитель не запустился",
  "worker_run.signal": "исполнитель молчит",
  "worker_run.no_writes": "изменения в каталоге, которого нельзя менять",
  "worker_run.report": "отчёт не собран",
  "worker_run.cost_policy": "появилось списание там, где его быть не должно",
  "snapshot.fresh": "сведения устарели",
  "local_model.serving": "локальная модель не отвечает",
};

// openRunSubject ведёт от расхождения к поручению, по которому оно возникло.
//
// Иначе владелец видит «исполнитель молчит» и не может ничего сделать, не зная,
// о каком поручении речь.
window.openRunSubject = async (runID) => {
  try {
    const d = await api("/api/v1/work-orders");
    for (const o of d.items || []) {
      const full = await api(`/api/v1/work-orders/${o.id}`);
      if ((full.order.runs || []).some((r) => r.id === runID)) {
        showTab("orders");
        openOrder(o.id);
        return;
      }
    }
    alert("Поручение не найдено: возможно, оно уже завершено.");
  } catch (err) {
    alert(`Не вышло: ${err.message}`);
  }
};

window.ackDiscrepancy = async (id) => {
  await api(`/api/v1/discrepancies/${id}/acknowledge`, {
    method: "POST", body: JSON.stringify({ note: "принято оператором" }),
  });
  loadState();
};

window.grant = async (id) => {
  await api(`/api/v1/approvals/${id}/grant`, { method: "POST" });
  loadState();
  loadOrders();
};

window.deny = async (id) => {
  const reason = prompt("Причина отказа:") ?? "";
  await api(`/api/v1/approvals/${id}/deny`, {
    method: "POST", body: JSON.stringify({ reason }),
  });
  loadState();
  loadOrders();
};

// ---------- нити ----------

// Группы нитей из 07_USER_EXPERIENCE §2. Порядок важен: сверху то, что живёт
// сейчас, снизу — отпущенное. Пояснение к группе избавляет от угадывания.
const THREAD_GROUPS = [
  { state: "active", title: "Сейчас живёт", why: "этим вы заняты; здесь ждут вашего внимания" },
  { state: "maturing", title: "Зреет", why: "замысел ещё не оформился, но и не заброшен" },
  { state: "waiting", title: "Ждёт", why: "нужно чужое действие или наступление срока" },
  { state: "blocked", title: "Застряло", why: "движение невозможно, пока препятствие не снято" },
  { state: "paused", title: "Мы не договорили", why: "разговор оборван на середине" },
  { state: "resolved", title: "Завершено", why: "вопрос закрыт", quiet: true },
  { state: "released", title: "Отпущено", why: "решено больше не заниматься; это не провал", quiet: true },
  { state: "archived", title: "В архиве", why: "убрано с глаз, но не удалено", quiet: true },
];

const KIND_LABEL = {
  project: "проект", idea: "идея", problem: "проблема", decision: "решение",
  conversation: "разговор", research: "исследование", waiting: "ожидание",
  personal: "личное", relationship: "отношения", other: "прочее",
};

// threadTitles позволяет показать связь названием другой нити, а не её
// идентификатором: связь между «Аудит mirvmon» и «thr_06fw…» читается по-разному.
const threadTitles = new Map();

function threadRow(t) {
  return `
    <div class="card thread-row clickable" onclick="openThread('${esc(t.id)}')">
      <div class="row">
        <strong>${esc(t.title)}</strong>
        <span class="grow"></span>
        <span class="muted">${esc(KIND_LABEL[t.kind] || t.kind)}</span>
        ${techNote(`· ${esc(t.id)} · рев. ${t.revision}`)}
      </div>
      ${t.summary ? `<div class="muted" style="margin-top:3px">${esc(t.summary)}</div>`
        : t.origin ? `<div class="muted" style="margin-top:3px">${esc(t.origin)}</div>` : ""}
      <div class="muted" style="margin-top:3px">
        последнее движение ${ago(t.last_meaningful_activity_at || t.updated_at)}
      </div>
    </div>`;
}

async function loadThreads() {
  try {
    const d = await api("/api/v1/threads");
    const items = d.items || [];
    items.forEach((t) => threadTitles.set(t.id, t.title));

    if (!items.length) {
      $("threads-groups").innerHTML =
        `<p class="muted">Нитей пока нет. Первая появится сама, как только вы
         о чём-нибудь заговорите с Бэрримором, — или заведите её вручную.</p>`;
    } else {
      // Пустые группы не показываются: перечислять то, чего нет, — шум.
      // Незнакомое состояние не теряется, а выносится отдельной группой,
      // иначе нить исчезла бы из виду без всякого следа.
      const known = new Set(THREAD_GROUPS.map((g) => g.state));
      const stray = items.filter((t) => !known.has(t.state));
      const groups = THREAD_GROUPS.map((g) => ({ ...g, items: items.filter((t) => t.state === g.state) }))
        .filter((g) => g.items.length);
      if (stray.length) {
        groups.push({ title: "Состояние не распознано", items: stray,
          why: "интерфейс не знает такого состояния; подробности — в техническом режиме" });
      }
      $("threads-groups").innerHTML = groups.map((g) => `
        <div class="group ${g.quiet ? "quiet" : ""}">
          <h3>${esc(g.title)} <span class="count">${g.items.length}</span></h3>
          <p class="why">${esc(g.why)}</p>
          ${g.items.map(threadRow).join("")}
        </div>`).join("");
    }

    const select = $("wo-thread");
    if (select) {
      select.innerHTML = items
        .map((t) => `<option value="${esc(t.id)}">${esc(t.title)}</option>`)
        .join("");
    }
  } catch (err) {
    $("threads-groups").innerHTML = `<p class="muted">${esc(err.message)}</p>`;
  }
}

$("th-toggle-new").addEventListener("click", () => {
  const box = $("th-new");
  box.hidden = !box.hidden;
  if (!box.hidden) $("th-title").focus();
});

// Позиции сторон намеренно разведены по колонкам: в домене они хранятся
// раздельно, и слияние их в один список стирало бы главное — что стороны
// могут не соглашаться.
function sideColumn(title, list) {
  if (!list.length) {
    return `<div class="side"><h4>${esc(title)}</h4>
      <p class="muted" style="margin:0">ничего не сформулировано</p></div>`;
  }
  return `<div class="side"><h4>${esc(title)}</h4>
    ${list.map((p) => `
      <div style="margin-bottom:8px">
        <div>${esc(p.statement)}</div>
        <div class="muted">${confidenceWord(p.confidence)}${
          p.basis ? ` · ${esc(p.basis)}` : ""
        }</div>
      </div>`).join("")}</div>`;
}

// Число уверенности само по себе ничего не говорит владельцу; в техническом
// режиме оно остаётся видимым как есть.
function confidenceWord(v) {
  if (techMode) return `уверенность ${v}`;
  if (v >= 0.9) return "уверенно";
  if (v >= 0.6) return "скорее так";
  return "предположение";
}

const LINK_LABEL = {
  depends_on: "зависит от", conflicts_with: "противоречит",
  derived_from: "выросла из", related_to: "связана с",
  supersedes: "заменяет", blocks: "мешает", inspired_by: "навеяна",
};

const EVENT_LABEL = {
  "thread.created": "нить заведена",
  "thread.updated": "нить изменена",
  "thread.state.changed": "состояние изменилось",
  "thread.position.updated": "записана позиция",
  "thread.decision.recorded": "принято решение",
  "thread.question.opened": "задан вопрос",
  "thread.question.resolved": "вопрос закрыт",
  "thread.linked": "связана с другой нитью",
  "thread.released": "нить отпущена",
};

let openThreadID = null;

window.openThread = async (id) => {
  const box = $("thread-detail");
  box.hidden = false;
  openThreadID = id;
  localStorage.setItem("barrymore.thread", id);
  box.innerHTML = `<span class="muted">загрузка…</span>`;
  try {
    const { thread: d, work_orders: orders } = await api(`/api/v1/threads/${id}`);
    const t = d.thread;
    const live = (d.positions || []).filter((p) => !p.valid_until);
    const open = (d.questions || []).filter((q) => q.status === "open");
    const answered = (d.questions || []).filter((q) => q.status !== "open");
    const group = THREAD_GROUPS.find((g) => g.state === t.state);

    let timeline = [];
    try {
      timeline = (await api(`/api/v1/threads/${id}/timeline`)).items || [];
    } catch {
      // История — не главное в карточке: без неё карточка всё равно полезна.
    }

    box.innerHTML = `
      <div class="row">
        <h2 style="margin:0">${esc(t.title)}</h2>
        <span class="grow"></span>
        <button class="ghost" onclick="closeThread()">Закрыть</button>
      </div>
      <div class="row" style="margin-top:6px">
        ${tag(t.state)}
        <span class="muted">${esc(KIND_LABEL[t.kind] || t.kind)}</span>
        ${group ? `<span class="muted">· ${esc(group.why)}</span>` : ""}
        ${techNote(`· ${esc(t.id)} · рев. ${t.revision}`)}
      </div>
      ${t.summary ? `<p style="margin:10px 0 0">${esc(t.summary)}</p>` : ""}
      ${t.origin ? `<p class="muted" style="margin:6px 0 0">почему появилась: ${esc(t.origin)}</p>` : ""}
      ${t.released_reason ? `<p class="muted" style="margin:6px 0 0">отпущена: ${esc(t.released_reason)}</p>` : ""}
      <div class="muted" style="margin-top:6px">
        заведена ${when(t.created_at)} · последнее движение ${ago(t.last_meaningful_activity_at || t.updated_at)}
      </div>

      <h2 style="margin-top:18px">Позиции сторон</h2>
      <div class="sides">
        ${sideColumn("Вы", live.filter((p) => p.owner === "person"))}
        ${sideColumn("Бэрримор", live.filter((p) => p.owner === "barrymore"))}
      </div>
      <div class="row" style="margin-top:8px">
        <input id="pos-text" class="grow" placeholder="сформулировать позицию">
        <select id="pos-owner" style="flex:0 0 150px">
          <option value="person">от вас</option>
          <option value="barrymore">от Бэрримора</option>
        </select>
        <button class="ghost" onclick="addPosition('${esc(id)}')">Записать</button>
      </div>

      <h2 style="margin-top:18px">Решения</h2>
      ${
        (d.decisions || []).length
          ? `<ul class="plain">${d.decisions.map((x) => `
              <li><div>${esc(x.statement)}</div>
              <div class="muted">${x.decided_by === "person" ? "решили вы" : "решил Бэрримор"}${
                x.rationale ? ` · ${esc(x.rationale)}` : ""
              }${x.decided_at ? ` · ${when(x.decided_at)}` : ""}</div></li>`).join("")}</ul>`
          : `<p class="muted">решений пока нет</p>`
      }

      <h2 style="margin-top:18px">Вопросы</h2>
      ${
        open.length
          ? `<ul class="plain">${open.map((q) => `<li>${esc(q.question)}</li>`).join("")}</ul>`
          : `<p class="muted">открытых вопросов нет</p>`
      }
      ${
        answered.length
          ? `<details style="margin-top:6px"><summary class="muted">закрытые вопросы (${answered.length})</summary>
              <ul class="plain">${answered.map((q) => `
                <li class="muted">${esc(q.question)} — ${esc(say(q.status))}</li>`).join("")}</ul></details>`
          : ""
      }

      <h2 style="margin-top:18px">Поручения</h2>
      ${
        (orders || []).length
          ? `<ul class="plain">${orders.map((o) => `
              <li class="clickable" onclick="openOrder('${esc(o.id)}')">
                ${tag(o.state)} ${esc(o.title)}</li>`).join("")}</ul>`
          : `<p class="muted">по этой нити никому ничего не поручалось</p>`
      }

      ${
        (d.links || []).length
          ? `<h2 style="margin-top:18px">Связи</h2>
             <ul class="plain">${d.links.map((l) => {
               const other = l.from_id === t.id ? l.to_id : l.from_id;
               return `
               <li class="clickable" onclick="openThread('${esc(other)}')">
                 <span class="muted">${esc(LINK_LABEL[l.kind] || l.kind)}</span>
                 ${esc(threadTitles.get(other) || other)}
                 ${l.note ? `<div class="muted">${esc(l.note)}</div>` : ""}
               </li>`;
             }).join("")}</ul>`
          : ""
      }

      ${
        timeline.length
          ? `<details style="margin-top:18px">
              <summary class="muted">как эта нить менялась (${timeline.length})</summary>
              <ul class="plain">${timeline.map((e) => `
                <li class="muted">
                  ${when(e.occurred_at)} — ${esc(EVENT_LABEL[e.event_type] || e.event_type)}
                  ${techNote(`· ${esc(e.event_type)} · seq ${e.seq}`)}
                </li>`).join("")}</ul>
             </details>`
          : ""
      }`;
    box.scrollIntoView({ behavior: "smooth", block: "start" });
  } catch (err) {
    box.innerHTML = `<span class="tag bad">ошибка</span> ${esc(err.message)}`;
  }
};

window.closeThread = () => {
  openThreadID = null;
  localStorage.removeItem("barrymore.thread");
  $("thread-detail").hidden = true;
};

window.addPosition = async (threadID) => {
  const statement = $("pos-text").value.trim();
  if (!statement) return;
  await api(`/api/v1/threads/${threadID}/positions`, {
    method: "POST",
    body: JSON.stringify({
      owner: $("pos-owner").value, statement, confidence: 0.8, basis: "введено оператором",
    }),
  });
  openThread(threadID);
};

$("th-create").addEventListener("click", async () => {
  try {
    await api("/api/v1/threads", {
      method: "POST",
      body: JSON.stringify({
        title: $("th-title").value.trim(),
        kind: $("th-kind").value,
        origin: $("th-origin").value.trim(),
        summary: "",
      }),
    });
    $("th-title").value = "";
    $("th-origin").value = "";
    loadThreads();
  } catch (err) {
    alert(`Нить не создана: ${err.message}`);
  }
});

// ---------- штат ----------

async function loadWorkers() {
  try {
    const d = await api("/api/v1/workers");
    renderWorkers(d.items || []);
  } catch (err) {
    $("workers").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }
}

// Класс исполнителя объясняется словами, а не жаргоном.
const CLASS_LABEL = {
  routine: "повседневный исполнитель",
  specialist: "мастер по вызову",
};

// Уровень доверия говорит, что исполнителю позволено, а не как он называется
// внутри. `worktree_write` ничего не сообщает тому, кто не читал спецификацию.
const TRUST_LABEL = {
  observe: "только наблюдение",
  workspace_read: "разрешено только читать",
  proposal_only: "разрешено только предлагать",
  worktree_write: "разрешено писать в отдельную ветку",
  workspace_write: "разрешено писать в рабочий каталог",
  external_side_effects: "разрешены действия вовне",
};

const AUTH_LABEL = {
  configured: "учётная запись настроена",
  missing: "учётная запись не настроена",
  unknown: "про учётную запись ничего не известно",
};

function costTag(tier, charged) {
  if (charged) return tag("списывала деньги", "bad");
  switch (tier) {
    case "free": return tag("бесплатно", "ok");
    case "subscription": return tag("квота подписки", "warn");
    case "paid": return tag("платно", "bad");
    default: return tag("стоимость неизвестна", "");
  }
}

function renderWorkers(items) {
  $("workers").innerHTML = items.length
    ? items.map((v) => {
        const a = v.availability || {};
        const caps = (v.capabilities || [])
          .filter((c) => c.evidence !== "declared")
          .map((c) => `${esc(c.capability)} <span class="muted">(${esc(c.evidence)})</span>`);
        const models = v.models || [];
        const free = models.filter((m) => m.cost_tier === "free" && !(m.verified_at && m.last_cost > 0));
        const charged = models.filter((m) => m.verified_at && m.last_cost > 0);
        const isSpecialist = v.worker.class === "specialist";

        return `
          <li class="card">
            <div class="row">
              <strong>${esc(v.worker.display_name)}</strong>
              ${tag(CLASS_LABEL[v.worker.class] || v.worker.class, isSpecialist ? "warn" : "ok")}
              ${tag(a.status || "unknown")}
              ${v.availability_fresh ? "" : tag("снимок просрочен", "warn")}
              <span class="grow"></span>
              <span class="muted">${esc(v.worker.version || "версия неизвестна")}</span>
            </div>
            <div class="muted" style="margin-top:6px">
              ${esc(v.worker.executable_path || "путь неизвестен")}<br>
              ${esc(say(v.worker.trust_level, TRUST_LABEL))} ·
              ${esc(say(v.worker.auth_state, AUTH_LABEL))} ·
              проверено ${ago(v.worker.last_probe_at)}
            </div>
            <div class="muted" style="margin-top:6px">${esc(a.reason || "")}</div>
            <div class="muted" style="margin-top:4px">
              ${a.quota_known ? "квота известна" : esc(a.quota_note || "состояние квоты неизвестно")}
            </div>

            <div style="margin-top:10px">
              <strong style="font-size:13px">Модели</strong>
              ${
                models.length
                  ? `<div class="muted" style="margin-top:4px">
                       всего ${models.length}, бесплатных ${free.length}
                       ${charged.length ? `, со списаниями ${charged.length}` : ""}
                       · каталог обновлён ${ago(v.worker.models_refreshed_at)}
                     </div>
                     <ul class="plain" style="margin-top:6px">${
                       free.slice(0, 5).map((m) => `
                         <li style="padding:4px 0">
                           ${costTag(m.cost_tier, false)}
                           <code style="font-size:12px">${esc(m.ref)}</code>
                           ${v.worker.preferred_model === m.ref ? tag("выбрана вручную", "ok") : ""}
                           <div class="muted">${esc(m.evidence || "")}</div>
                         </li>`).join("")
                     }${
                       free.length > 5
                         ? `<li class="muted" style="padding:4px 0">…и ещё ${free.length - 5} бесплатных</li>`
                         : ""
                     }${
                       charged.map((m) => `
                         <li style="padding:4px 0">
                           ${costTag(m.cost_tier, true)}
                           <code style="font-size:12px">${esc(m.ref)}</code>
                           <div class="muted">${esc(m.evidence || "")}</div>
                         </li>`).join("")
                     }${
                       free.length === 0 && charged.length === 0
                         ? `<li class="muted" style="padding:4px 0">бесплатных моделей нет</li>`
                         : ""
                     }</ul>`
                  : `<div class="muted" style="margin-top:4px">каталог моделей пуст</div>`
              }
            </div>

            ${
              caps.length
                ? `<div style="margin-top:8px" class="muted">подтверждено: ${caps.join(", ")}</div>`
                : `<div style="margin-top:8px" class="muted">подтверждённых возможностей нет</div>`
            }
            <div class="row" style="margin-top:8px">
              <button class="ghost" onclick="probe('${esc(v.worker.id)}')">Перепроверить</button>
              <button class="ghost" onclick="refreshModels('${esc(v.worker.id)}')">Обновить модели</button>
            </div>
          </li>`;
      }).join("")
    : `<li class="muted">исполнители не обнаружены</li>`;
}

window.refreshModels = async (id) => {
  await api(`/api/v1/workers/${id}/models/refresh`, { method: "POST" });
  loadWorkers();
};

window.probe = async (id) => {
  await api(`/api/v1/workers/${id}/probe`, { method: "POST" });
  loadWorkers();
};

$("discover").addEventListener("click", async () => {
  $("workers").innerHTML = `<li class="muted">обнаружение…</li>`;
  try {
    const res = await api("/api/v1/workers/discover", { method: "POST" });
    renderWorkers(res.found || []);
    if ((res.missing || []).length) {
      $("workers").insertAdjacentHTML("beforeend",
        `<li class="muted">не найдены: ${res.missing.map(esc).join(", ")}</li>`);
    }
  } catch (err) {
    $("workers").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }
});

// ---------- поручения ----------

async function loadOrders() {
  try {
    const d = await api("/api/v1/work-orders");
    const items = d.items || [];
    $("orders").innerHTML = items.length
      ? items.map((o) => `
          <li class="clickable" onclick="openOrder('${esc(o.id)}')">
            <div class="row">
              ${tag(o.state)} <strong>${esc(o.title)}</strong>
              <span class="grow"></span>
              <span class="muted">${when(o.created_at)}</span>
            </div>
            <div class="muted" style="margin-top:3px">${esc(o.goal)}</div>
            ${o.failure_reason ? `<div class="muted">причина: ${esc(o.failure_reason)}</div>` : ""}
          </li>`).join("")
      : `<li class="muted">поручений нет</li>`;
  } catch (err) {
    $("orders").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }
}

$("wo-create").addEventListener("click", async () => {
  try {
    const p = await api("/api/v1/work-orders", {
      method: "POST",
      body: JSON.stringify({
        thread_id: $("wo-thread").value,
        title: "",
        goal: $("wo-goal").value.trim(),
        why: "",
        workspace_root: $("wo-root").value.trim(),
        worker_id: "",
        constraints: [],
      }),
    });
    $("wo-candidates").innerHTML = `
      <div class="card" style="margin-top:12px">
        <h2>Почему выбран ${esc(p.candidates.find((c) => c.view.worker.id === p.order.worker_id)?.view.worker.display_name || "исполнитель")}</h2>
        <div class="muted">${esc(p.order.worker_rationale)}</div>
        <h2 style="margin-top:12px">Все кандидаты</h2>
        <ul class="plain">${p.candidates.map((c) => `
          <li>
            <div class="row">
              ${c.blocked ? tag("не может взять", "bad") : tag(`оценка ${c.score.toFixed(1)}`, "ok")}
              <strong>${esc(c.view.worker.display_name)}</strong>
              ${tag(CLASS_LABEL[c.view.worker.class] || c.view.worker.class,
                    c.view.worker.class === "specialist" ? "warn" : "ok")}
            </div>
            ${c.blocked ? "" : `<div>${costTag(c.model?.cost_tier, false)} <code style="font-size:12px">${esc(c.model?.ref || "")}</code></div>`}
            <div class="muted">${esc(c.blocked ? c.block_reason : (c.reasons || []).join("; "))}</div>
          </li>`).join("")}</ul>
      </div>`;
    loadOrders();
    loadState();
  } catch (err) {
    $("wo-candidates").innerHTML =
      `<div class="card" style="margin-top:12px"><span class="tag bad">поручение не сформировано</span>
       <div class="muted" style="margin-top:6px">${esc(err.message)}</div></div>`;
  }
});

window.openOrder = async (id) => {
  const box = $("order-detail");
  box.hidden = false;
  box.innerHTML = `<span class="muted">загрузка…</span>`;
  try {
    const d = await api(`/api/v1/work-orders/${id}`);
    const o = d.order.order;
    const runs = d.order.runs || [];
    const last = runs[runs.length - 1];

    box.innerHTML = `
      <div class="row">
        <h2 style="margin:0">${esc(o.title)}</h2>
        ${tag(o.state)}
        ${o.audit_only ? tag("только чтение", "ok") : tag("с записью", "warn")}
        <span class="grow"></span>
        ${
          o.state === "approved"
            ? `<button class="act" onclick="startOrder('${esc(id)}')">Запустить</button>`
            : ""
        }
        ${
          o.state === "running"
            ? `<button class="ghost" onclick="cancelOrder('${esc(id)}')">Остановить</button>`
            : ""
        }
      </div>
      <p class="muted">${esc(o.goal)}</p>
      <table>
        <tr><th>Выбор исполнителя</th><td>${esc(o.worker_rationale)}</td></tr>
        <tr><th>Модель</th><td>
          <code>${esc(o.model || "—")}</code> ${costTag(o.model_cost_tier, false)}
          <div class="muted">${esc(o.model_rationale || "")}</div>
        </td></tr>
        <tr><th>Рабочий каталог</th><td>${esc(o.workspace_root)}</td></tr>
        <tr><th>HEAD на момент запуска</th><td class="muted">${esc(o.workspace_git_head || "—")}</td></tr>
        <tr><th>Пакет контекста</th><td class="muted">${esc(o.context_pack_checksum || "не собран")}</td></tr>
        ${o.failure_reason ? `<tr><th>Причина неудачи</th><td>${esc(o.failure_reason)}</td></tr>` : ""}
      </table>

      ${
        last
          ? `<h2 style="margin-top:16px">Запуск</h2>
             <table>
               <tr><th>Состояние</th><td>${tag(last.status)} ${
                 d.attached ? tag("вывод читается", "ok") : tag("вывод не читается", "warn")
               }</td></tr>
               <tr><th>Последний сигнал</th><td>${ago(last.last_signal_at)}</td></tr>
               <tr><th>Процесс</th><td class="muted">${esc(last.unit_name || `pid ${last.pid}`)}</td></tr>
               <tr><th>Код завершения</th><td>${
                 last.exit_code === undefined || last.exit_code === null ? "—" : last.exit_code
               }</td></tr>
             </table>`
          : ""
      }

      <h2 style="margin-top:16px">Ожидания</h2>
      ${
        (d.expectations || []).length
          ? `<ul class="plain">${d.expectations.map((e) => `
              <li><div class="row">${tag(e.status)} <strong>${esc(e.kind)}</strong>
              <span class="grow"></span>
              <span class="muted">важность при нарушении: ${esc(e.severity_if_missed)}</span></div>
              <div class="muted">${esc(e.basis)}</div></li>`).join("")}</ul>`
          : `<p class="muted">ожиданий нет</p>`
      }

      <h2 style="margin-top:16px">Проверки</h2>
      ${
        (d.order.verifications || []).length
          ? `<ul class="plain">${d.order.verifications.map((v) => `
              <li><div class="row">${tag(v.status)} <strong>${esc(v.name)}</strong></div>
              <div class="muted">${esc(v.detail)}</div></li>`).join("")}</ul>`
          : `<p class="muted">проверки не выполнялись</p>`
      }

      <h2 style="margin-top:16px">Артефакты</h2>
      ${
        (d.order.artifacts || []).length
          ? `<ul class="plain">${d.order.artifacts.map((a) => `
              <li><strong>${esc(a.name)}</strong>
              <span class="muted"> · ${a.size} байт · ${esc(a.checksum.slice(0, 23))}…</span>
              <div class="muted">${esc(a.path)}</div></li>`).join("")}</ul>`
          : `<p class="muted">артефактов нет</p>`
      }

      <h2 style="margin-top:16px">Наблюдаемые действия исполнителя</h2>
      ${
        (d.observations || []).length
          ? `<ul class="plain">${d.observations.slice(0, 40).map((ob) => {
              let summary = ob.kind;
              try {
                const p = JSON.parse(ob.payload || "{}");
                if (p.summary) summary = `${ob.kind}: ${p.summary}`;
              } catch { /* payload не разобран — показываем вид наблюдения */ }
              return `<li><span class="muted">${when(ob.observed_at)} · ${
                ob.source_quality === "reported" ? "со слов исполнителя" : "наблюдение runtime"
              }</span><div>${esc(summary)}</div></li>`;
            }).join("")}</ul>`
          : `<p class="muted">наблюдений нет</p>`
      }

      <div class="row" style="margin-top:12px">
        <button class="ghost" onclick="showReport('${esc(id)}')">Показать отчёт исполнителя</button>
      </div>
      <div id="order-report"></div>`;
  } catch (err) {
    box.innerHTML = `<span class="tag bad">ошибка</span> ${esc(err.message)}`;
  }
};

window.startOrder = async (id) => {
  try {
    await api(`/api/v1/work-orders/${id}/start`, { method: "POST" });
    openOrder(id);
    loadOrders();
  } catch (err) {
    alert(`Поручение не запущено: ${err.message}`);
  }
};

window.cancelOrder = async (id) => {
  await api(`/api/v1/work-orders/${id}/cancel`, {
    method: "POST", body: JSON.stringify({ reason: "остановлено оператором" }),
  });
  openOrder(id);
  loadOrders();
};

window.showReport = async (id) => {
  const box = $("order-report");
  try {
    const d = await api(`/api/v1/work-orders/${id}/report`);
    box.innerHTML = `
      <div class="notes" style="margin-top:12px">${esc(d.note)}</div>
      <pre>${esc(JSON.stringify(d.report, null, 2))}</pre>`;
  } catch (err) {
    box.innerHTML = `<div class="muted" style="margin-top:12px">${esc(err.message)}</div>`;
  }
};


// ---------- приёмная ----------

let currentConversation = null;
let sending = false;

// Локальная модель отвечает десятками секунд: интерфейс обязан честно
// показывать ожидание, а не притворяться, что ничего не происходит.
const PROVIDER_TONE = { ready: "ok", unreachable: "bad", not_configured: "warn", broken: "bad" };

// loadWelcome показывает первое знакомство, пока разговоров ещё не было.
//
// Это не мастер настройки: он ничего не настраивает за владельца, а честно
// перечисляет, что уже готово и чего не хватает, со ссылкой на то место,
// где это правится. Прятать пробелы за бодрым «всё отлично» — то же враньё.
async function loadWelcome(conversationCount) {
  const box = $("welcome");
  if (conversationCount > 0 || localStorage.getItem("barrymore.welcomed") === "1") {
    box.hidden = true;
    return;
  }
  try {
    const s = await api("/api/v1/system/state");
    const checks = [
      {
        ok: s.conversation?.status === "ready",
        good: "Бэрримор может разговаривать.",
        bad: "Разговорный слой не отвечает — модель ещё не поднята или не выбрана.",
        where: "settings",
      },
      {
        ok: (s.workspace_roots || []).length > 0,
        good: `Разрешённые каталоги заданы: ${(s.workspace_roots || []).join(", ")}.`,
        bad: "Не задан ни один рабочий каталог — поручения будут отклоняться политикой.",
        where: "settings",
      },
      {
        ok: !!s.isolation?.bwrap,
        good: "Изоляция запусков доступна: поручения только на чтение действительно только читают.",
        bad: "bubblewrap недоступен — поручения запускаться не будут.",
        where: "state",
      },
    ];
    box.hidden = false;
    box.innerHTML = `
      <h2>Здравствуйте</h2>
      <p>Я Бэрримор. Я помню ваши нити и разговоры, обращаюсь к внешним
      исполнителям и ничего не делаю без вашего ведома. Начните с любого вопроса —
      или посмотрите, что уже готово:</p>
      <ol>
        ${checks.map((c) => `
          <li>
            ${c.ok ? tag("готово", "ok") : tag("не хватает", "warn")}
            ${esc(c.ok ? c.good : c.bad)}
            ${c.ok ? "" : ` <a href="#" onclick="showTab('${c.where}');return false">поправить</a>`}
          </li>`).join("")}
      </ol>
      <div class="row" style="margin-top:12px">
        <button class="ghost" id="welcome-done">Понятно, больше не показывать</button>
      </div>`;
    $("welcome-done").addEventListener("click", () => {
      localStorage.setItem("barrymore.welcomed", "1");
      box.hidden = true;
    });
  } catch {
    box.hidden = true;
  }
}

window.showTab = (name) => showTab(name);

async function loadTalk() {
  try {
    const d = await api("/api/v1/conversations");
    const p = d.provider || {};
    const ready = p.status === "ready";
    $("talk-provider").innerHTML = `
      <div class="row">
        <strong>Разговорный слой</strong>
        ${tag(p.status || "неизвестно", PROVIDER_TONE[p.status] || "")}
        <span class="grow"></span>
        <span class="muted">${esc(p.model || "")}${
          p.latency ? ` · отклик ${Math.round(p.latency / 1e6)} мс` : ""
        }</span>
      </div>
      <div class="muted" style="margin-top:6px">${esc(p.reason || "")}</div>
      ${
        ready
          ? ""
          : `<div class="notes" style="margin-top:10px">Бэрримор сейчас не разговаривает.
             Нити, штат, поручения и предиктивный контур работают без него.</div>`
      }`;
    $("talk-send").disabled = !ready;

    const items = d.items || [];
    if (!currentConversation && items.length) currentConversation = items[0].id;
    if (currentConversation) await loadChat();
    else $("chat").innerHTML = `<div class="muted">Начните новый разговор.</div>`;
    await loadWelcome(items.length);
  } catch (err) {
    $("talk-provider").innerHTML = `<span class="tag bad">ошибка</span> ${esc(err.message)}`;
  }

  try {
    const t = await api("/api/v1/threads");
    const select = $("talk-thread");
    select.innerHTML = `<option value="">без нити</option>` +
      (t.items || []).map((x) => `<option value="${esc(x.id)}">${esc(x.title)}</option>`).join("");
  } catch { /* список нитей не критичен для разговора */ }
}

function bubble(m) {
  const who = m.role === "person" ? "Вы" : "Бэрримор";
  const meta = [];
  if (m.model) meta.push(esc(m.model));
  if (m.latency_ms) meta.push(`${Math.round(m.latency_ms / 1000)} с`);
  if (m.output_tokens) meta.push(`${m.prompt_tokens}+${m.output_tokens} токенов`);
  const trace = (m.retrieval_trace || []).length
    ? `<div class="meta">подано в контекст: ${m.retrieval_trace.map(esc).join("; ")}</div>`
    : "";
  return `<div class="bubble ${esc(m.role)}">
    <div class="meta">${who} · ${when(m.created_at)}${meta.length ? " · " + meta.join(" · ") : ""}</div>
    ${esc(m.content)}
    ${trace}
  </div>`;
}

async function loadChat() {
  if (!currentConversation) return;
  try {
    const d = await api(`/api/v1/conversations/${currentConversation}/messages`);
    const items = d.items || [];
    $("chat").innerHTML = items.length
      ? items.map(bubble).join("")
      : `<div class="muted">Разговор пуст. Напишите первым.</div>`;
    $("chat").scrollTop = $("chat").scrollHeight;
  } catch (err) {
    $("chat").innerHTML = `<div class="muted">${esc(err.message)}</div>`;
  }
}

$("talk-new").addEventListener("click", async () => {
  try {
    const c = await api("/api/v1/conversations", {
      method: "POST",
      body: JSON.stringify({ thread_id: $("talk-thread").value, title: "" }),
    });
    currentConversation = c.id;
    $("talk-proposals").hidden = true;
    await loadChat();
  } catch (err) {
    alert(`Разговор не начат: ${err.message}`);
  }
});

async function send() {
  if (sending) return;
  const text = $("talk-input").value.trim();
  if (!text) return;

  if (!currentConversation) {
    const c = await api("/api/v1/conversations", {
      method: "POST",
      body: JSON.stringify({ thread_id: $("talk-thread").value, title: "" }),
    });
    currentConversation = c.id;
  }

  sending = true;
  $("talk-send").disabled = true;
  $("talk-input").value = "";
  $("chat").insertAdjacentHTML("beforeend",
    `<div class="bubble person"><div class="meta">Вы · только что</div>${esc(text)}</div>
     <div class="thinking" id="thinking">Бэрримор думает… на локальной модели это занимает до минуты.</div>`);
  $("chat").scrollTop = $("chat").scrollHeight;

  const started = Date.now();
  const timer = setInterval(() => {
    const el = document.getElementById("thinking");
    if (el) el.textContent = `Бэрримор думает… ${Math.round((Date.now() - started) / 1000)} с`;
  }, 1000);

  try {
    const turn = await api(`/api/v1/conversations/${currentConversation}/messages`, {
      method: "POST", body: JSON.stringify({ text }),
    });
    clearInterval(timer);
    await loadChat();
    renderProposals(turn);
    loadMemory();
  } catch (err) {
    clearInterval(timer);
    const el = document.getElementById("thinking");
    if (el) el.outerHTML = `<div class="bubble barrymore"><span class="tag bad">не отвечено</span>
      <div style="margin-top:6px">${esc(err.message)}</div></div>`;
  } finally {
    sending = false;
    $("talk-send").disabled = false;
  }
}

// renderProposals показывает предложения отдельно от ответа: они ничего
// не меняют до вашего решения.
// lastProposals хранит предложения последнего хода, чтобы кнопка «оформить»
// могла взять их целиком, не вытаскивая текст обратно из разметки.
let lastProposals = [];
let lastThreadID = "";

function renderProposals(turn) {
  const p = turn.proposal || {};
  const cands = turn.memory_candidates || [];
  const orders = p.work_order_proposals || [];
  const questions = p.open_questions || [];
  const pos = p.thread_position;
  lastProposals = orders;
  lastThreadID = turn.reply?.thread_id || "";

  if (!cands.length && !orders.length && !questions.length && !pos) {
    $("talk-proposals").hidden = true;
    return;
  }
  $("talk-proposals").hidden = false;
  $("talk-proposals").innerHTML = `
    <h2>Предложения Бэрримора</h2>
    <p class="muted">Поручения и позиции не применены. Записи с пометкой
      «записано» Бэрримор сделал сам — их можно сразу удалить.</p>
    ${
      pos
        ? `<div style="margin-top:8px"><strong style="font-size:13px">Его позиция по нити</strong>
           <div>${esc(pos.statement)}</div>
           <div class="muted">${confidenceWord(pos.confidence)} · ${esc(pos.basis)}</div></div>`
        : ""
    }
    ${
      cands.length
        ? `<div style="margin-top:10px"><strong style="font-size:13px">Память</strong>
           <ul class="plain">${cands.map((c) => `
             <li><div>${tag(c.type)} ${c.auto ? tag("записано", "ok") : ""} ${esc(c.content)}</div>
             <div class="muted" style="margin-top:4px">${esc(c.reason || "")}</div>
             <div class="row" style="margin-top:6px">
               ${
                 c.auto
                   ? `<button class="ghost" onclick="forgetMemory('${esc(c.item_id)}')">Удалить из памяти</button>`
                   : `<button class="act" onclick="acceptMemory('${esc(c.id)}')">Запомнить</button>
                      <button class="ghost" onclick="rejectMemory('${esc(c.id)}')">Не надо</button>`
               }
             </div></li>`).join("")}</ul></div>`
        : ""
    }
    ${
      orders.length
        ? `<div style="margin-top:10px"><strong style="font-size:13px">Поручить исполнителю</strong>
           <ul class="plain">${orders.map((o, i) => `
             <li>
               <div>${esc(o.goal)}</div>
               <div class="muted">${esc(o.why)}</div>
               <div class="row" style="margin-top:6px">
                 <button class="ghost" onclick="draftOrder(${i})">Оформить поручение</button>
                 ${o.workspace_hint ? `<span class="muted">${esc(o.workspace_hint)}</span>` : ""}
               </div>
             </li>`).join("")}
           </ul>
           <div class="muted">Само поручение не создаётся: сначала выбирается исполнитель
           и модель, и требуется ваше подтверждение.</div></div>`
        : ""
    }
    ${
      questions.length
        ? `<div style="margin-top:10px"><strong style="font-size:13px">Открытые вопросы</strong>
           <ul class="plain">${questions.map((q) => `<li>${esc(q)}</li>`).join("")}</ul></div>`
        : ""
    }`;
}

// draftOrder переносит предложение в форму поручения.
//
// Именно переносит, а не создаёт: исполнитель, модель и подтверждение владельца
// остаются впереди. Заставлять перепечатывать цель руками — единственное, чего
// здесь можно избежать без потери контроля.
window.draftOrder = (index) => {
  const p = lastProposals[index];
  if (!p) return;
  showTab("orders");
  $("wo-goal").value = p.goal;
  if (p.workspace_hint) $("wo-root").value = p.workspace_hint;
  const select = $("wo-thread");
  if (lastThreadID && select) {
    // Выбор нити переживает перерисовку списка: она загружается асинхронно.
    const apply = () => {
      if ([...select.options].some((o) => o.value === lastThreadID)) {
        select.value = lastThreadID;
      } else {
        setTimeout(apply, 200);
      }
    };
    apply();
  }
  $("wo-goal").focus();
};

$("talk-send").addEventListener("click", send);
$("talk-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) send();
});

// ---------- память ----------

window.acceptMemory = async (id) => {
  await api(`/api/v1/memory/candidates/${id}/accept`, { method: "POST", body: "{}" });
  loadMemory();
  $("talk-proposals").hidden = true;
};

window.rejectMemory = async (id) => {
  await api(`/api/v1/memory/candidates/${id}/reject`, { method: "POST", body: "{}" });
  loadMemory();
  $("talk-proposals").hidden = true;
};

window.forgetMemory = async (id) => {
  if (!confirm("Удалить эту запись? Бэрримор перестанет её знать и использовать.")) return;
  await api(`/api/v1/memories/${id}`, { method: "DELETE" });
  loadMemory();
};

$("mem-add").addEventListener("click", async () => {
  const content = $("mem-new").value.trim();
  if (!content) return;
  try {
    await api("/api/v1/memories", {
      method: "POST",
      body: JSON.stringify({ content, type: $("mem-type").value, thread_id: "" }),
    });
    $("mem-new").value = "";
    loadMemory();
  } catch (err) {
    alert(`Не записано: ${err.message}`);
  }
});

$("mem-new").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("mem-add").click();
});

async function loadMemory() {
  try {
    const st = await api("/api/v1/system/state");
    $("mem-policy").textContent = `Режим: ${st.memory_policy || "—"}.`;
  } catch { /* режим не критичен для работы раздела */ }

  try {
    const d = await api("/api/v1/memory/candidates");
    const items = d.items || [];
    $("mem-candidates").innerHTML = items.length
      ? items.map((c) => `
          <li>
            <div class="row">${tag(c.type)} ${tag(c.sensitivity)} <strong>${esc(c.content)}</strong></div>
            <div class="muted" style="margin-top:4px">${esc(c.reason || "")}
              · уверенность ${c.confidence} · ${when(c.created_at)}</div>
            <div class="muted">${esc(c.auto_decision || "")}</div>
            <div class="row" style="margin-top:8px">
              <button class="act" onclick="acceptMemory('${esc(c.id)}')">Запомнить</button>
              <button class="ghost" onclick="rejectMemory('${esc(c.id)}')">Отклонить</button>
            </div>
          </li>`).join("")
      : `<li class="muted">решений не требуется</li>`;
  } catch (err) {
    $("mem-candidates").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }

  try {
    const q = $("mem-search").value.trim();
    const d = await api(q
      ? `/api/v1/memories?q=${encodeURIComponent(q)}`
      : "/api/v1/memories?forgotten=true");
    const items = d.items || [];
    renderForgotten(d.forgotten || []);
    $("mem-items").innerHTML = items.length
      ? items.map((m) => `
          <li${m.revoked_at ? ' style="opacity:.55"' : ""}>
            <div class="row">
              ${tag(m.type)}
              ${m.forgotten_at ? tag("удалено", "bad") : ""}
              ${
                !m.forgotten_at && m.provenance?.proposed_by === "barrymore"
                  ? tag("записал сам", "")
                  : ""
              }
              ${
                !m.forgotten_at && m.provenance?.proposed_by === "person"
                  ? tag("по вашей просьбе", "ok")
                  : ""
              }
              <strong>${esc(m.content)}</strong>
            </div>
            <div class="muted" style="margin-top:4px">
              ${
                m.forgotten_at
                  ? `удалено ${when(m.forgotten_at)}; отметка о том, что запись была,
                     остаётся в журнале`
                  : `откуда: ${esc(m.provenance?.source || "—")}, предложил
                     ${esc(m.provenance?.proposed_by || "—")}, принял
                     ${esc(m.provenance?.accepted_by || "—")} · ${when(m.created_at)}
                     ${m.provenance?.reason ? `<br>основание: ${esc(m.provenance.reason)}` : ""}`
              }
            </div>
            ${
              m.forgotten_at
                ? ""
                : `<button class="ghost" style="margin-top:6px"
                     onclick="forgetMemory('${esc(m.id)}')">Удалить</button>`
            }
          </li>`).join("")
      : `<li class="muted">память пуста</li>`;
  } catch (err) {
    $("mem-items").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }
}

// renderForgotten показывает надгробия отдельно и в свёрнутом виде.
//
// В общем списке они выглядели бы так, будто удаление не сработало. Но и
// прятать их совсем нельзя: событие из журнала удалить невозможно, и делать
// вид, будто записи никогда не было, Бэрримор не станет.
function renderForgotten(items) {
  const box = $("mem-forgotten");
  if (!box) return;
  if (!items.length) {
    box.hidden = true;
    return;
  }
  box.hidden = false;
  box.innerHTML = `
    <details>
      <summary class="muted">удалённые записи (${items.length})</summary>
      <p class="muted" style="margin:6px 0">Содержание удалено и в поиске
        не участвует. В журнале остаётся отметка о том, что запись была
        и была удалена: событие оттуда удалить нельзя.</p>
      <ul class="plain">${items.map((m) => `
        <li class="muted">
          ${tag(m.type)} удалено ${when(m.forgotten_at)}
          ${m.revoke_reason ? `· ${esc(m.revoke_reason)}` : ""}
        </li>`).join("")}</ul>
    </details>`;
}

$("mem-search").addEventListener("input", () => {
  clearTimeout(window.__memTimer);
  window.__memTimer = setTimeout(loadMemory, 300);
});

// ---------- журнал и живой поток ----------

const journal = $("journal");
let lastSeq = 0;

function connectStream() {
  const src = new EventSource(`/api/v1/stream?from=${lastSeq}`);

  src.onopen = () => {
    $("live").textContent = "поток: подключён";
    $("live").className = "tag ok";
  };

  src.onmessage = (msg) => handleEvent(msg);
  // Именованные события приходят с типом события домена.
  src.addEventListener("error", () => {
    $("live").textContent = "поток: переподключение";
    $("live").className = "tag warn";
  });

  // EventSource сам переподключается и присылает Last-Event-ID,
  // поэтому пропущенные события догоняются из журнала.
  src.addEventListener("message", handleEvent);
  ["thread.created", "work_order.proposed", "worker_run.started", "worker_run.exited",
   "discrepancy.detected", "reflex.started", "reflex.completed", "reflex.failed",
   "escalation.requested", "verification.completed", "work_order.state.changed",
   "observation.recorded", "expectation.created", "expectation.satisfied",
  ].forEach((type) => src.addEventListener(type, handleEvent));
}

function handleEvent(msg) {
  let env;
  try {
    env = JSON.parse(msg.data);
  } catch {
    return;
  }
  if (env.seq <= lastSeq) return;
  lastSeq = env.seq;

  const li = document.createElement("li");
  li.innerHTML = `<div class="row"><span class="muted">#${env.seq} ${when(env.occurred_at)}</span>
    <strong>${esc(env.event_type)}</strong>
    <span class="grow"></span>
    <span class="muted">${esc(env.actor?.type || "")}</span></div>`;
  journal.prepend(li);
  while (journal.children.length > 200) journal.lastChild.remove();

  // Значимые события обновляют текущие представления.
  if (env.event_type.startsWith("discrepancy") || env.event_type.startsWith("reflex") ||
      env.event_type.startsWith("escalation") || env.event_type.startsWith("approval")) {
    loadState();
  }
  if (env.event_type.startsWith("work_order") || env.event_type.startsWith("worker_run") ||
      env.event_type.startsWith("verification")) {
    loadOrders();
  }
}

// ---------- запуск ----------

// ---------- настройки ----------

function gigabytes(bytes) {
  if (!bytes) return "";
  return `${(bytes / 1024 ** 3).toFixed(1)} ГБ`;
}

async function loadSettings() {
  try {
    const d = await api("/api/v1/local-model/available");
    const items = d.items || [];
    $("models-note").textContent = d.dir
      ? `Каталог моделей: ${d.dir}. ${d.note}`
      : "Каталог моделей не задан, выбирать не из чего. " +
        "Укажите его при запуске флагом -local-models-dir.";
    $("models-list").innerHTML = items.length
      ? items.map((m) => `
          <li>
            <div class="row">
              ${m.current ? tag("выбрана", "ok") : ""}
              <strong>${esc(m.name)}</strong>
              <span class="muted">${gigabytes(m.size_bytes)}</span>
              <span class="grow"></span>
              ${m.current ? "" :
                `<button class="ghost" onclick="selectModel('${esc(m.path)}')">Выбрать</button>`}
            </div>
            ${techNote(esc(m.path))}
          </li>`).join("")
      : d.dir
        ? `<li class="muted">в каталоге нет файлов .gguf</li>`
        : "";
  } catch (err) {
    $("models-note").textContent = err.message;
  }

  try {
    const s = await api("/api/v1/settings");
    const lm = s.local_model || {};
    $("tune-context").value = lm.context_size ?? "";
    $("tune-threads").value = lm.threads ?? 0;
    $("tune-gpu").value = lm.gpu_layers ?? 0;
    $("tune-moe").value = lm.cpu_moe ?? 0;

    const roots = s.workspace_roots || [];
    $("roots-list").innerHTML = roots.length
      ? roots.map((p) => `
          <li>
            <div class="row">
              <span>${esc(p)}</span>
              <span class="grow"></span>
              <button class="ghost" onclick="removeRoot('${esc(p)}')">Убрать</button>
            </div>
          </li>`).join("")
      : `<li><span class="tag bad">ни одного</span>
         <span class="muted">поручения будут отклоняться политикой</span></li>`;

    $("settings-launch").innerHTML = `
      <table>
        <tr><th>Адрес интерфейса</th><td>${esc(s.addr)}</td></tr>
        <tr><th>Каталог данных</th><td>${esc(s.data_root)}</td></tr>
        <tr><th>Файл настроек</th><td>${esc(s.path)}</td></tr>
        <tr><th>Политика стоимости</th><td>${esc(s.model_policy)}</td></tr>
        <tr><th>Память</th><td>${esc(s.memory_policy)}</td></tr>
        <tr><th>Порт локальной модели</th><td>${esc(lm.port)}</td></tr>
      </table>
      <p style="margin-top:10px">Меняется только перезапуском: ${
        (s.restart_required || []).map(esc).join(", ")
      }.</p>`;
  } catch (err) {
    $("settings-launch").textContent = err.message;
  }

  try {
    const d = await api("/api/v1/workers");
    const items = d.items || [];
    $("settings-workers").innerHTML = items.length
      ? items.map((v) => `
          <li>
            <div class="row">
              <strong>${esc(v.worker.display_name)}</strong>
              ${tag(CLASS_LABEL[v.worker.class] || v.worker.class,
                    v.worker.class === "specialist" ? "warn" : "ok")}
              ${v.worker.enabled ? "" : tag("отключён вами", "bad")}
              <span class="grow"></span>
              <button class="ghost" onclick="toggleWorker('${esc(v.worker.id)}', ${!v.worker.enabled})">
                ${v.worker.enabled ? "Не привлекать" : "Привлекать"}
              </button>
            </div>
            <div class="muted">${esc(v.availability?.reason || "")}</div>
          </li>`).join("")
      : `<li class="muted">штат пуст: нажмите «Обнаружить исполнителей» в разделе «Штат»</li>`;
  } catch (err) {
    $("settings-workers").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }
}

window.selectModel = async (path) => {
  if (!confirm("Выбрать эту модель? Текущая будет остановлена, новая поднимется за минуты.")) return;
  try {
    await api("/api/v1/local-model/select", {
      method: "POST", body: JSON.stringify({ path }),
    });
  } catch (err) {
    alert(`Модель не выбрана: ${err.message}`);
  }
  loadSettings();
};

$("root-add").addEventListener("click", async () => {
  const path = $("root-new").value.trim();
  if (!path) return;
  try {
    await api("/api/v1/settings/workspace-roots", {
      method: "POST", body: JSON.stringify({ path }),
    });
    $("root-new").value = "";
  } catch (err) {
    alert(`Каталог не разрешён: ${err.message}`);
  }
  loadSettings();
});

window.removeRoot = async (path) => {
  if (!confirm(`Убрать ${path} из разрешённых? Новые поручения по нему будут отклоняться.`)) return;
  try {
    await api(`/api/v1/settings/workspace-roots?path=${encodeURIComponent(path)}`,
      { method: "DELETE" });
  } catch (err) {
    alert(`Не убрано: ${err.message}`);
  }
  loadSettings();
};

window.toggleWorker = async (id, enabled) => {
  try {
    await api(`/api/v1/workers/${id}/enabled`, {
      method: "POST", body: JSON.stringify({ enabled, reason: "решение владельца" }),
    });
  } catch (err) {
    alert(`Не вышло: ${err.message}`);
  }
  loadSettings();
};

$("tune-apply").addEventListener("click", async () => {
  const num = (id) => {
    const v = parseInt($(id).value, 10);
    return Number.isFinite(v) ? v : 0;
  };
  try {
    // Путь не передаётся: сервер оставляет выбранную модель как есть.
    await api("/api/v1/local-model/select", {
      method: "POST",
      body: JSON.stringify({
        context_size: num("tune-context"),
        threads: num("tune-threads"),
        gpu_layers: num("tune-gpu"),
        cpu_moe: num("tune-moe"),
      }),
    });
  } catch (err) {
    alert(`Не применено: ${err.message}`);
  }
  loadSettings();
});

function refresh(tab) {
  if (tab === "talk") loadTalk();
  if (tab === "state") loadState();
  if (tab === "threads") loadThreads();
  if (tab === "staff") loadWorkers();
  if (tab === "orders") { loadOrders(); loadThreads(); }
  if (tab === "memory") loadMemory();
  if (tab === "settings") loadSettings();
}

loadTalk();
restorePlace();
connectStream();
setInterval(() => {
  // Пока идёт ответ модели, перерисовывать разговор нельзя: это стёрло бы
  // индикатор ожидания и введённый текст.
  if (sending) return;
  const current = document.querySelector('nav button[aria-current="true"]');
  if (current && current.dataset.tab !== "talk") refresh(current.dataset.tab);
}, 10000);
