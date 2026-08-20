// Интерфейс оператора: тонкая поверхность над API.
//
// Здесь нет собственного состояния домена — только отображение того,
// что сообщает runtime, и различение факта, ожидания и вывода.

import {
  formatTurnProgress,
  matchesTurn,
  restoreTurnProgress,
} from "./turn-progress.js";

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
    const error = new Error(message);
    error.status = res.status;
    throw error;
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
  // запуски
  starting: "запускается", exited: "завершился", orphaned: "потерян",
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
    const s2 = await api("/api/v1/system/state");
    const waiting = s2.pending_changes || [];
    $("pending-changes-card").hidden = !waiting.length;
    $("pending-changes").innerHTML = waiting.map((o) => {
      const ch = o.change_summary || {};
      return `
        <li class="clickable" onclick="showTab('orders');openOrder('${esc(o.id)}')">
          <div class="row">
            <strong>${esc(o.title)}</strong>
            <span class="grow"></span>
            <span class="muted">${(ch.files || []).length} файлов ·
              +${ch.insertions || 0}/−${ch.deletions || 0}</span>
          </div>
          <div class="muted">${esc(o.workspace_root)}</div>
        </li>`;
    }).join("");
  } catch {
    // Не главное на экране состояния: без этого списка остальное всё равно полезно.
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
// Ожидания названы тем, чего Бэрримор ждёт, а не именем вида в коде.
const EXPECTATION_LABEL = {
  "worker_run.starts": "исполнитель запустится",
  "worker_run.signal": "исполнитель подаёт признаки работы",
  "worker_run.no_writes": "каталог не будет изменён",
  "worker_run.report": "отчёт будет собран",
  "worker_run.cost_policy": "списаний не будет",
  "snapshot.fresh": "сведения останутся свежими",
  "local_model.serving": "локальная модель отвечает",
};

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

// threadLine — одна строка о нити. Цель важнее происхождения: «чего мы хотим»
// отвечает на вопрос владельца, а «почему нить появилась» — на вопрос историка.
function threadLine(t) {
  const text = t.canon?.goal || t.canon?.situation || t.origin || t.summary;
  return text ? `<div class="muted" style="margin-top:3px">${esc(text)}</div>` : "";
}

function threadRow(t) {
  return `
    <div class="card thread-row clickable" onclick="openThread('${esc(t.id)}')">
      <div class="row">
        <strong>${esc(t.title)}</strong>
        <span class="grow"></span>
        <span class="muted">${esc(KIND_LABEL[t.kind] || t.kind)}</span>
        ${techNote(`· ${esc(t.id)} · рев. ${t.revision}`)}
      </div>
      ${threadLine(t)}
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
  "thread.canon.updated": "состояние нити обновлено",
  "conversation.thread.attached": "разговор отнесён к нити",
  "conversation.thread.detached": "разговор отвязан от нити",
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
      ${canonBlock(t.canon || {}, t.id)}
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
  loadSkills();
}

// ---------- подключение незнакомого инструмента ----------
//
// Владелец называет команду — и это единственное, что он делает. Дальше
// показывается не форма для заполнения, а готовое предложение: что за
// инструмент, как его запускать, на каких строках справки это основано.
let harnessDraft = null;

$("harness-study").addEventListener("click", async () => {
  const name = $("harness-name").value.trim();
  const box = $("harness-result");
  if (!name) {
    $("harness-name").focus();
    return;
  }
  box.innerHTML = `<p class="muted" style="margin-top:10px">Смотрю, что это за ${esc(name)}…</p>`;
  try {
    const d = await api("/api/v1/harness/study", {
      method: "POST", body: JSON.stringify({ name }),
    });
    harnessDraft = d.draft;
    renderHarness(d.survey, d.draft);
  } catch (err) {
    harnessDraft = null;
    box.innerHTML = `<div class="notes" style="margin-top:10px">${esc(err.message)}</div>`;
  }
});

function renderHarness(survey, draft) {
  const box = $("harness-result");
  if (draft.refused) {
    // Отказ показывается целиком: владелец должен видеть, что именно Бэрримор
    // отверг и почему, а не только что «не вышло».
    box.innerHTML = `
      <div class="notes" style="margin-top:12px">
        <div><strong>Подключать не стану.</strong> ${esc(draft.refused)}</div>
        <div class="muted" style="margin-top:6px">Я принимаю только то, что
          инструмент напечатал о себе сам.</div>
      </div>`;
    return;
  }
  box.innerHTML = `
    <div class="card" style="margin-top:12px">
      <div class="row">
        <strong>${esc(draft.display_name)}</strong>
        ${survey.version ? tag(survey.version) : ""}
        ${draft.audit_args?.length ? tag("умеет только читать", "ok") : tag("режима только чтения нет", "warn")}
      </div>
      <div class="muted" style="margin-top:6px">${esc(draft.why)}</div>
      <dl class="kv" style="margin-top:10px">
        <dt>запуск</dt><dd><code>${esc([survey.name, ...(draft.run_args || [])].join(" "))}</code></dd>
        ${
          draft.audit_args?.length
            ? `<dt>только чтение</dt><dd><code>${esc(draft.audit_args.join(" "))}</code></dd>`
            : ""
        }
        <dt>доверие</dt><dd>только чтение рабочего каталога — большего
          новичку я не дам</dd>
        <dt>на чём основано</dt><dd>${
          (draft.evidence || []).map(esc).join("<br>") || "—"
        }</dd>
      </dl>
      <div class="row" style="margin-top:10px">
        <button class="act" id="harness-adopt">Принять в штат</button>
        <span class="muted">возможности заявлены справкой, а не проверены работой</span>
      </div>
      ${techNote(`<pre>${esc(survey.help || "")}</pre>`)}
    </div>`;
  $("harness-adopt").addEventListener("click", adoptHarness);
}

async function adoptHarness() {
  if (!harnessDraft) return;
  try {
    await api("/api/v1/harness/adopt", {
      method: "POST", body: JSON.stringify({ draft: harnessDraft }),
    });
    $("harness-result").innerHTML =
      `<p class="muted" style="margin-top:12px">Принял. Он появился в штате ниже —
       и пока с наименьшим доверием.</p>`;
    harnessDraft = null;
    $("harness-name").value = "";
    loadWorkers();
  } catch (err) {
    $("harness-result").insertAdjacentHTML("beforeend",
      `<div class="notes" style="margin-top:10px">${esc(err.message)}</div>`);
  }
}

// practiceWords переводит запись опыта в строку, которую можно прочитать.
//
// Она же уходит в контекст модели: владелец и Бэрримор смотрят на один и тот
// же довод, а не на разные.
function practiceWords(p) {
  if (!p) return "ещё не применялось";
  if (p.stale) return `больше не пользуюсь: ${p.stale_why}`;
  if (!p.applied) return "ещё не применялось";
  const base = `применялось ${p.applied} раз`;
  if (!p.failed) {
    return p.avg_ms ? `${base}, без осечек, обычно за ${p.avg_ms} мс` : `${base}, без осечек`;
  }
  return `${base}, из них неудачных ${p.failed}`;
}

// loadSkills показывает собственные умения Бэрримора рядом со штатом.
//
// Место выбрано намеренно: штат отвечает на вопрос «кто может это сделать»,
// и первым в списке должен стоять сам Бэрримор. Иначе выходит, что своих рук
// у него нет и остаётся только звать.
async function loadSkills() {
  const box = $("skills");
  if (!box) return;
  try {
    const d = await api("/api/v1/skills");
    const items = d.items || [];
    const used = {};
    for (const r of d.runs || []) {
      used[r.skill_id] = used[r.skill_id] || r;
    }
    // Опыт показывается рядом со способом: ровный перечень умений скрывал бы
    // главное — что одно работает годами, а другое перестало работать вчера.
    const record = {};
    for (const p of d.practices || []) record[`${p.kind}:${p.ref}`] = p;
    box.innerHTML = items.length
      ? items.map((sk) => {
          const last = used[sk.id];
          const live = sk.enabled && !sk.retired_why;
          return `
            <li>
              <div class="row">
                ${live ? tag("умеет", "ok") : tag("больше не пользуюсь", "bad")}
                <strong>${esc(sk.title)}</strong>
                ${sk.origin === "learned" ? tag("освоено") : ""}
                <span class="grow"></span>
                ${techNote(esc(sk.id))}
              </div>
              <div class="muted" style="margin-top:3px">отвечает на вопрос:
                ${esc(sk.question)}</div>
              ${
                sk.retired_why
                  ? `<div class="notes" style="margin-top:6px">${esc(sk.retired_why)}</div>`
                  : ""
              }
              <div class="muted" style="margin-top:4px">${
                esc(practiceWords(record[`own:${sk.id}`]))
              }${
                last ? ` · последний раз ${ago(last.started_at)}: ${esc(last.answer)}` : ""
              }</div>
              ${techNote(`<div style="margin-top:4px">шаги: ${
                (sk.steps || []).map((st) => esc(st.primitive)).join(" → ")}</div>`)}
            </li>`;
        }).join("")
      : `<li class="muted">умений нет</li>`;
  } catch (err) {
    box.innerHTML = `<li class="muted">${esc(err.message)}</li>`;
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
        allow_write: $("wo-write").checked,
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

// changesBlock показывает, что исполнитель сделал, и даёт решить судьбу.
//
// До решения владельца его каталог не тронут вообще — об этом сказано прямо,
// потому что «поручение с записью» звучит так, будто уже что-то изменилось.
function changesBlock(o) {
  if (o.audit_only) return "";
  const ch = o.change_summary || {};
  const files = ch.files || [];

  if (o.change_state === "applied") {
    return `<h2 style="margin-top:16px">Изменения</h2>
      <div>${tag("применены", "ok")} <span class="muted">${when(o.change_decided_at)}
      · наложены в ваш каталог и остались незакоммиченными</span></div>
      ${o.change_decision_note ? `<div class="muted">${esc(o.change_decision_note)}</div>` : ""}`;
  }
  if (o.change_state === "discarded") {
    return `<h2 style="margin-top:16px">Изменения</h2>
      <div>${tag("отброшены", "")} <span class="muted">${when(o.change_decided_at)}
      · копия удалена, каталог остался таким, каким был</span></div>`;
  }
  if (!files.length) {
    return `<h2 style="margin-top:16px">Изменения</h2>
      <p class="muted">Исполнитель ничего не изменил — решать нечего.</p>`;
  }

  return `
    <h2 style="margin-top:16px">Изменения ждут вашего решения</h2>
    <p class="muted">Исполнитель работал в копии. Ваш каталог пока не тронут:
      ${files.length} файлов, +${ch.insertions || 0}/−${ch.deletions || 0}.</p>
    <ul class="plain">${files.map((f) => `
      <li><span class="muted">${esc(f.status)}</span> <code>${esc(f.path)}</code></li>`).join("")}</ul>
    ${
      ch.truncated
        ? `<div class="notes" style="margin-top:8px">Дифф слишком велик и показан
           не целиком, поэтому применить его отсюда нельзя. Изменения лежат
           в копии: <code>${esc(o.work_copy_path || "")}</code></div>`
        : `<details style="margin-top:8px">
             <summary class="muted">посмотреть дифф</summary>
             <pre>${esc(ch.patch || "")}</pre>
           </details>`
    }
    <div class="row" style="margin-top:10px">
      ${
        ch.truncated
          ? ""
          : `<button class="act" onclick="applyChanges('${esc(o.id)}')">Применить к моему каталогу</button>`
      }
      <button class="ghost" onclick="discardChanges('${esc(o.id)}')">Отбросить</button>
      ${techNote(`ветка в копии: ${esc(o.work_copy_branch || "—")}`)}
    </div>`;
}

window.applyChanges = async (id) => {
  if (!confirm("Наложить изменения на ваш каталог? Они останутся незакоммиченными.")) return;
  try {
    const res = await api(`/api/v1/work-orders/${id}/changes/apply`, {
      method: "POST", body: JSON.stringify({ note: "принято владельцем" }),
    });
    alert(res.detail || "Изменения наложены.");
  } catch (err) {
    alert(`Не применено: ${err.message}`);
  }
  openOrder(id);
};

window.discardChanges = async (id) => {
  if (!confirm("Отбросить изменения? Копия будет удалена, каталог останется прежним.")) return;
  try {
    await api(`/api/v1/work-orders/${id}/changes/discard`, {
      method: "POST", body: JSON.stringify({ note: "отклонено владельцем" }),
    });
  } catch (err) {
    alert(`Не отброшено: ${err.message}`);
  }
  openOrder(id);
};

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
        ${o.audit_only ? tag("только чтение", "ok") : tag("с записью в копию", "warn")}
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

      ${changesBlock(o)}

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
              <li><div class="row">${tag(e.status)}
              <strong>${esc(EXPECTATION_LABEL[e.kind] || e.kind)}</strong>
              ${techNote(`· ${esc(e.kind)}`)}
              <span class="grow"></span>
              <span class="muted">если не сбудется — ${esc(say(e.severity_if_missed))}</span></div>
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

// Отчёт — это результат работы, ради которого всё затевалось. Показывать его
// сырым JSON значит заставлять владельца читать машинный формат там, где
// он ждёт ответа на свой вопрос. Сырой текст остаётся под раскрытием.
const FINDING_TONE = {
  critical: "bad", high: "bad", major: "bad",
  medium: "warn", moderate: "warn", warning: "warn",
  low: "", minor: "", info: "",
};

window.showReport = async (id) => {
  const box = $("order-report");
  box.innerHTML = `<div class="muted" style="margin-top:12px">загрузка…</div>`;
  try {
    const d = await api(`/api/v1/work-orders/${id}/report`);
    const r = d.report || {};
    const findings = r.findings || [];
    box.innerHTML = `
      <div class="notes" style="margin-top:12px">${esc(d.note)}</div>
      ${r.summary ? `<p style="margin-top:10px">${esc(r.summary)}</p>` : ""}
      ${
        findings.length
          ? `<h2 style="margin-top:12px">Находки (${findings.length})</h2>
             <ul class="plain">${findings.map((f) => `
               <li>
                 <div class="row">
                   ${tag(f.severity || "info", FINDING_TONE[String(f.severity).toLowerCase()])}
                   <strong>${esc(f.title || "без заголовка")}</strong>
                   ${f.path ? `<span class="muted"><code>${esc(f.path)}</code></span>` : ""}
                 </div>
                 ${f.evidence ? `<div class="muted" style="margin-top:4px">${esc(f.evidence)}</div>` : ""}
               </li>`).join("")}</ul>`
          : `<p class="muted" style="margin-top:10px">Находок нет.</p>`
      }
      ${
        (r.checked_paths || []).length
          ? `<div class="muted" style="margin-top:10px">Просмотрено: ${
              r.checked_paths.map(esc).join(", ")
            }</div>`
          : ""
      }
      ${
        r.limitations
          ? `<div class="notes" style="margin-top:10px">Чего проверить не удалось:
             ${esc(r.limitations)}</div>`
          : ""
      }
      <details style="margin-top:10px">
        <summary class="muted">исходный отчёт как есть</summary>
        <pre>${esc(JSON.stringify(r, null, 2))}</pre>
      </details>`;
  } catch (err) {
    box.innerHTML = `<div class="muted" style="margin-top:12px">${esc(err.message)}</div>`;
  }
};


// ---------- приёмная ----------
//
// Приёмная — это разговор, и ничего кроме него. Всё, что произошло в доме,
// пока владелец разговаривал, лежит справа и открывается по кнопке.
//
// Прежняя вёрстка складывала в вертикальный поток карточку нити, уведомления
// и предложения поручений — и собеседник оказывался где-то посередине
// операторского дашборда. Разделение простое: центр отвечает на «с кем я
// сейчас говорю», сайдбар — на «что случилось, пока мы говорили».

let currentConversation = null;
let sending = false;
let providerReady = false;
let activeTurn = null;
let activeProgress = null;
let progressReceivedAt = 0;
let progressTimer = null;
const terminalTurns = new Set();

const PROVIDER_TONE = { ready: "ok", unreachable: "bad", not_configured: "warn", broken: "bad" };

// ---------- сайдбар дел ----------

// Три раздела отвечают на три разных вопроса и не смешиваются: «что от меня
// требуется», «что изменилось без меня», «что происходит прямо сейчас».
const AFFAIR_GROUPS = [
  { id: "needs", title: "Требует вашего решения", empty: "Ничего не ждёт вашего слова." },
  { id: "changed", title: "Изменилось", empty: "Пока ничего не менялось." },
  { id: "running", title: "Сейчас выполняется", empty: "Никто ничего не делает." },
];

const affairsBox = $("affairs");
const affairsToggle = $("affairs-toggle");

// Раскрытые дела и заметки о них переживают перерисовку: список обновляется
// каждые десять секунд, и захлопывать открытое владельцем под руками нельзя.
const openAffairs = new Set();
const affairNotes = new Map();
let affairItems = [];
let turnAffairs = [];
let detailKind = "";

function setAffairs(open) {
  affairsBox.hidden = !open;
  document.body.classList.toggle("affairs-open", open);
  affairsToggle.setAttribute("aria-expanded", String(open));
  if (!open) closeDetail();
}

function affairsOpen() {
  return !affairsBox.hidden;
}

affairsToggle.addEventListener("click", () => setAffairs(!affairsOpen()));

window.closeDetail = () => {
  detailKind = "";
  $("affairs-detail").innerHTML = "";
};

$("affairs-groups").addEventListener("click", (e) => {
  const head = e.target.closest(".affair-head");
  if (!head) return;
  const id = head.dataset.affair;
  if (openAffairs.has(id)) openAffairs.delete(id);
  else openAffairs.add(id);
  renderAffairs();
});

function affairNote(id) {
  const note = affairNotes.get(id);
  return note ? `<div class="notes" style="margin-top:8px">${esc(note)}</div>` : "";
}

function affairRow(it) {
  const open = openAffairs.has(it.id);
  return `<li class="affair">
    <button class="affair-head" data-affair="${esc(it.id)}" aria-expanded="${open}">
      <span class="affair-what">${esc(it.what)}</span>
      <span class="affair-which">${esc(it.which || "вне дел")}${
        it.when ? ` · ${ago(it.when)}` : ""
      }${it.need ? ` · <span class="tag warn">нужно решение</span>` : ""}</span>
    </button>
    ${open ? `<div class="affair-body">${it.body || ""}${affairNote(it.id)}</div>` : ""}
  </li>`;
}

// renderAffairs рисует сайдбар и счётчик на кнопке.
//
// Счётчик считает только то, о чём Бэрримор обращается: решения и перемены.
// Идущая работа в счётчик не входит — она никуда не зовёт.
function renderAffairs() {
  const groups = AFFAIR_GROUPS.map((g) => ({
    ...g, items: affairItems.filter((i) => i.group === g.id),
  }));
  const decide = groups[0].items.length;
  const changed = groups[1].items.length;
  const total = decide + changed;

  $("affairs-count").textContent = String(total);
  affairsToggle.classList.toggle("hot", decide > 0);
  affairsToggle.classList.toggle("warm", decide === 0 && changed > 0);
  affairsToggle.title = decide
    ? `${decide} ждёт вашего решения`
    : total ? "есть перемены" : "ничего нового";

  // Значок в шапке — короткий путь в Приёмную с других вкладок. На самой
  // Приёмной он не нужен: там уже есть кнопка дел, и два счётчика рядом
  // читаются как противоречие.
  const badge = $("notice-badge");
  badge.hidden = total === 0 || activeTab() === "talk";
  badge.textContent = decide ? `${decide} ждёт решения` : `${total} от Бэрримора`;

  $("affairs-groups").innerHTML = groups.map((g) => `
    <h3>${esc(g.title)} <span class="count">${g.items.length}</span></h3>
    ${
      g.items.length
        ? `<ul class="plain">${g.items.map(affairRow).join("")}</ul>`
        : `<p class="empty">${esc(g.empty)}</p>`
    }`).join("");
}

// refreshAffairs собирает дела из всех источников сразу.
//
// Источник правды — сервер: предложения текущего хода живут в журнале,
// подтверждения — в списке ожидающих, обращения — в инициативе. Браузер
// ничего не додумывает и после перезагрузки показывает то же самое.
async function refreshAffairs() {
  const items = [...turnAffairs];
  const [notices, orders, approvals, state, skills] = await Promise.all([
    api("/api/v1/notices").catch(() => ({})),
    api("/api/v1/work-orders").catch(() => ({})),
    api("/api/v1/approvals/pending").catch(() => ({})),
    api("/api/v1/system/state").catch(() => ({})),
    api("/api/v1/skills").catch(() => ({})),
  ]);

  const orderByID = new Map((orders.items || []).map((o) => [o.id, o]));
  const dealOf = (o) => (o && threadTitles.get(o.thread_id)) || (o ? o.title : "");

  // Подтверждения — первое, что должен увидеть владелец: без его слова
  // исполнитель не двинется.
  for (const a of approvals.items || []) {
    const o = orderByID.get(a.work_order_id);
    items.push({
      id: `approval-${a.id}`, group: "needs", need: true,
      what: "Запустить исполнителя?", which: dealOf(o), when: a.requested_at,
      body: approvalBody(a, o),
    });
  }

  for (const n of notices.waiting || []) {
    const o = n.subject_type === "work_order" ? orderByID.get(n.subject_id) : null;
    items.push({
      id: `notice-${n.id}`,
      group: n.level === "urgent" ? "needs" : "changed",
      need: n.level === "urgent",
      what: n.title, which: dealOf(o), when: n.created_at,
      body: noticeBody(n),
    });
  }

  for (const o of state.pending_changes || []) {
    items.push({
      id: `changes-${o.id}`, group: "needs", need: true,
      what: "Изменения ждут вашего решения", which: dealOf(o), when: o.updated_at,
      body: `<div>${esc(o.title)}: исполнитель поработал в копии, ваш каталог
        не тронут.</div>
        <div class="row">
          <button class="ghost" onclick="showTab('orders');openOrder('${esc(o.id)}')">
            Посмотреть изменения</button>
        </div>
        <div class="muted" style="margin-top:6px">Что именно изменилось, видно
          в разделе «Поручения»: там же они применяются или отклоняются.</div>`,
    });
  }

  // Идущая работа. Здесь нет кнопок: она не требует решения, а просто идёт.
  for (const o of orders.items || []) {
    if (!RUNNING_STATES.has(o.state)) continue;
    items.push({
      id: `run-${o.id}`, group: "running", what: o.title, which: dealOf(o),
      when: o.started_at || o.created_at,
      body: `<div>${esc(o.goal)}</div>
        <div class="muted" style="margin-top:6px">${esc(say(o.state))}</div>
        <div class="row">
          <button class="ghost" onclick="showTab('orders');openOrder('${esc(o.id)}')">
            Подробности поручения</button>
        </div>`,
    });
  }

  // Освоение нового способа — то, о чём Бэрримор просит сам. Это не отчёт
  // и не напоминание: он заметил повторяющийся порядок действий и предлагает
  // дать ему имя.
  for (const sg of skills.suggestions || []) {
    items.push({
      id: `learn-${sg.id}`, group: "needs", need: true,
      what: "Могу освоить новый способ", which: sg.title,
      body: `<div>${esc(sg.why || "")}</div>
        <div class="muted" style="margin-top:6px">сложится из того, что уже умею:
          ${(sg.titles || []).map(esc).join(" → ")}</div>
        <div class="row">
          <button class="act" onclick="learnSkill('${esc(sg.id)}')">Осваивайте</button>
        </div>`,
    });
  }

  // Способ, признанный негодным, — тоже перемена, и молчать о ней нельзя:
  // владелец должен узнать, что Бэрримор перестал чем-то пользоваться.
  for (const p of skills.practices || []) {
    if (!p.stale) continue;
    items.push({
      id: `stale-${p.id}`, group: "changed",
      what: "Перестал пользоваться прежним способом", which: p.title || p.ref,
      when: p.last_at,
      body: `<div>${esc(p.stale_why || "")}</div>
        <div class="muted" style="margin-top:6px">способ остался в разделе
          «Штат» вместе с причиной — вернуть его можно там.</div>`,
    });
  }

  for (const gap of readinessGaps(state)) items.push(gap);

  affairItems = items;
  renderAffairs();
}

const RUNNING_STATES = new Set(["approved", "preparing", "running", "verifying", "awaiting_user"]);

// readinessGaps говорит о том, чего не хватает, словами владельца.
//
// Раньше это была карточка первого знакомства посреди разговора. Но нехватка
// рабочего каталога — не приветствие, а именно то, что требует решения:
// без неё поручения будут отклоняться, и лучше узнать об этом здесь.
function readinessGaps(s) {
  if (!s || !s.conversation) return [];
  const gaps = [];
  if (s.conversation.status !== "ready") {
    gaps.push({
      id: "gap-provider", group: "needs", need: true,
      what: "Бэрримор сейчас не разговаривает", which: "настройки",
      body: `<div>Нити, штат, поручения и наблюдение работают без этого.
        Разговор вернётся, как только поднимется модель.</div>
        ${techNote(`<div style="margin-top:6px">${esc(s.conversation.reason || "")}</div>`)}
        <div class="row">
          <button class="ghost" onclick="showTab('settings')">Открыть настройки</button>
        </div>`,
    });
  }
  if (!(s.workspace_roots || []).length) {
    gaps.push({
      id: "gap-roots", group: "needs", need: true,
      what: "Не задан ни один рабочий каталог", which: "настройки",
      body: `<div>Пока их нет, любое поручение будет отклонено политикой:
        доступ ко всему диску не является значением по умолчанию.</div>
        <div class="row">
          <button class="ghost" onclick="showTab('settings')">Разрешить каталог</button>
        </div>`,
    });
  }
  if (s.isolation && !s.isolation.bwrap) {
    gaps.push({
      id: "gap-isolation", group: "needs", need: true,
      what: "Изоляция запусков недоступна", which: "хост",
      body: `<div>Без bubblewrap поручение «только чтение» нельзя удержать
        только на чтении, поэтому Бэрримор не запускает исполнителей вовсе.</div>`,
    });
  }
  return gaps;
}

// approvalBody показывает то, что решается, без служебных подробностей.
//
// Заголовок подтверждения, который составляет сервер, называет модель — она
// владельцу при решении не нужна и уезжает в технический режим. Остаётся то,
// что действительно меняет решение: кто, где и с каким правом.
function approvalBody(a, o) {
  const scope = a.scope || {};
  return `
    <div>${esc(scope.worker || "исполнитель")} ${
      scope.write_level === "none"
        ? "прочитает каталог"
        : "поработает в копии каталога"
    } <code style="font-size:12px">${esc(scope.workspace_root || "")}</code>.</div>
    <div style="margin-top:6px">${costTag(scope.cost_tier, false)} ${
      o && o.audit_only ? tag("только чтение", "ok") : tag("с правкой файлов", "warn")
    }</div>
    ${scope.notes ? `<div class="muted" style="margin-top:6px">${esc(scope.notes)}</div>` : ""}
    ${o && o.worker_rationale
      ? `<div class="muted" style="margin-top:6px">${esc(o.worker_rationale)}</div>` : ""}
    ${techNote(`<div style="margin-top:6px">${esc(a.summary || "")}</div>`)}
    <div class="row">
      <button class="act" onclick="approveAndStart('${esc(a.id)}','${esc(a.work_order_id)}')">
        Подтвердить и запустить</button>
      <button class="ghost" onclick="denyFromTalk('${esc(a.id)}')">Не сейчас</button>
    </div>`;
}

function noticeBody(n) {
  return `
    <div>Почему сейчас: ${esc(n.why)}</div>
    <div class="row">
      ${
        n.subject_type === "work_order"
          ? `<button class="ghost" onclick="showTab('orders');openOrder('${esc(n.subject_id)}')">Открыть поручение</button>`
          : ""
      }
      ${
        n.subject_type === "memory"
          ? `<button class="ghost" onclick="showTab('memory')">Открыть память</button>`
          : ""
      }
      <button class="ghost" onclick="readNotice('${esc(n.id)}')">Понятно</button>
      <button class="ghost" onclick="muteNotice('${esc(n.kind)}')">Не сообщать о таком</button>
    </div>
    ${techNote(`<div style="margin-top:6px">${esc(n.kind)}</div>`)}`;
}

window.readNotice = async (id) => {
  try {
    await api(`/api/v1/notices/${id}/read`, { method: "POST" });
  } catch (err) {
    affairNotes.set(`notice-${id}`, err.message);
  }
  refreshAffairs();
};

window.muteNotice = async (kind) => {
  if (!confirm("Больше не сообщать о таком? Наблюдать Бэрримор не перестанет — " +
      "просто не будет обращаться первым.")) return;
  try {
    await api("/api/v1/notices/mute", {
      method: "POST", body: JSON.stringify({ kind }),
    });
  } catch (err) {
    alert(err.message);
  }
  refreshAffairs();
};

$("notice-badge").addEventListener("click", () => {
  showTab("talk");
  setAffairs(true);
});

window.showTab = (name) => showTab(name);

// ---------- разговор ----------

function turnStorageKey(conversationID) {
  return `barrymore.turn.${conversationID}`;
}

function setComposerState() {
  const busy = sending || Boolean(activeTurn);
  $("talk-send").disabled = !providerReady || busy;
  $("talk-input").disabled = Boolean(activeTurn);
}

function removeProgressRow() {
  document.getElementById("turn-progress")?.remove();
}

function clearActiveTurn() {
  activeTurn = null;
  activeProgress = null;
  progressReceivedAt = 0;
  if (progressTimer) clearInterval(progressTimer);
  progressTimer = null;
  removeProgressRow();
  setComposerState();
}

function renderActiveProgress() {
  if (!activeTurn || activeTurn.conversation_id !== currentConversation || !activeProgress) return;
  let row = document.getElementById("turn-progress");
  if (!row) {
    row = document.createElement("div");
    row.id = "turn-progress";
    row.className = "turn-progress";
    row.setAttribute("role", "status");
    row.setAttribute("aria-live", "polite");
    $("chat").append(row);
  }
  const elapsed = Number(activeProgress.elapsed_ms) || 0;
  const liveElapsed = elapsed + Math.max(0, Date.now() - progressReceivedAt);
  row.textContent = formatTurnProgress({ ...activeProgress, elapsed_ms: liveElapsed });
  row.title = row.textContent;
  $("chat").scrollTop = $("chat").scrollHeight;
}

function setActiveTurn(turn, progress = null) {
  if (!turn || turn.conversation_id !== currentConversation) return;
  activeTurn = turn;
  localStorage.setItem(turnStorageKey(turn.conversation_id), turn.id);
  activeProgress = restoreTurnProgress(turn, progress || turn.progress);
  progressReceivedAt = Date.now();
  if (progressTimer) clearInterval(progressTimer);
  progressTimer = setInterval(renderActiveProgress, 1000);
  setComposerState();
  renderActiveProgress();
}

function updateLiveProgress(progress) {
  if (!matchesTurn(progress, activeTurn)) return;
  activeProgress = progress;
  progressReceivedAt = Date.now();
  renderActiveProgress();
}

async function handleTerminalTurn(turn) {
  if (!turn || terminalTurns.has(turn.id)) return;
  terminalTurns.add(turn.id);
  const isCurrent = turn.conversation_id === currentConversation;
  if (turn.status === "completed") {
    localStorage.removeItem(turnStorageKey(turn.conversation_id));
  }
  if (activeTurn?.id === turn.id) clearActiveTurn();
  if (!isCurrent) {
    terminalTurns.delete(turn.id);
    return;
  }

  await loadChat(false);
  if (currentConversation !== turn.conversation_id) {
    terminalTurns.delete(turn.id);
    return;
  }
  if (turn.status === "completed" && turn.result) {
    takeTurn(turn.result);
    await loadThreadState();
    await refreshAffairs();
    loadMemory();
  } else {
    const detail = turn.error_message ||
      (turn.status === "interrupted" ? "Ход был прерван перезапуском." : "Ответ получить не удалось.");
    $("chat").insertAdjacentHTML("beforeend",
      `<div class="bubble barrymore"><span class="tag bad">не отвечено</span>
       <div class="said" style="margin-top:6px">${esc(detail)}</div></div>`);
    $("chat").scrollTop = $("chat").scrollHeight;
  }
  setComposerState();
  terminalTurns.delete(turn.id);
}

async function refreshTurn(turnID) {
  if (!currentConversation || !turnID) return;
  const conversationID = currentConversation;
  const turn = await api(`/api/v1/conversations/${conversationID}/turns/${turnID}`);
  if (currentConversation !== conversationID) return;
  if (["completed", "failed", "interrupted"].includes(turn.status)) {
    await handleTerminalTurn(turn);
    return;
  }
  setActiveTurn(turn, turn.progress);
}

async function restoreCurrentTurn() {
  if (!currentConversation) {
    clearActiveTurn();
    return;
  }
  const conversationID = currentConversation;
  try {
    const turn = await api(`/api/v1/conversations/${conversationID}/turns/active`);
    if (currentConversation === conversationID) setActiveTurn(turn, turn.progress);
    return;
  } catch (err) {
    if (err.status !== 404) {
      if (currentConversation === conversationID) {
        clearActiveTurn();
        activeTurn = { id: "", conversation_id: conversationID };
        setComposerState();
        $("chat").insertAdjacentHTML("beforeend",
          `<div class="turn-progress" id="turn-progress" role="status" aria-live="polite">
           Состояние хода не читается: ${esc(err.message)}</div>`);
      }
      return;
    }
  }

  const remembered = localStorage.getItem(turnStorageKey(conversationID));
  if (remembered) {
    try {
      await refreshTurn(remembered);
      return;
    } catch (err) {
      if (err.status !== 404) return;
      localStorage.removeItem(turnStorageKey(conversationID));
    }
  }
  if (currentConversation === conversationID) clearActiveTurn();
}

async function loadTalk() {
  try {
    const d = await api("/api/v1/conversations");
    const p = d.provider || {};
    const ready = p.status === "ready";
    providerReady = ready;
    setComposerState();
    $("talk-status").innerHTML = ready
      ? techNote(`${esc(p.model || "")}${
          p.latency ? ` · отклик ${Math.round(p.latency / 1e6)} мс` : ""
        }`)
      : `Бэрримор сейчас не разговаривает. Остальное работает без него.
         ${techNote(`· ${esc(p.status || "")} · ${esc(p.reason || "")}`)}`;

    const items = d.items || [];
    if (!currentConversation && items.length) currentConversation = items[0].id;
    if (currentConversation) {
      await loadChat();
      await restoreCurrentTurn();
    } else {
      clearActiveTurn();
      $("chat").innerHTML = greeting();
    }
    await loadThreadState();
    await refreshAffairs();
  } catch (err) {
    $("talk-status").innerHTML = `<span class="tag bad">ошибка</span> ${esc(err.message)}`;
  }
}

// greeting — то, с чего начинается пустой экран.
//
// Это приветствие, а не мастер настройки: чего не хватает, сказано в сайдбаре,
// потому что нехватка требует решения, а знакомство — нет.
function greeting() {
  return `<div class="bubble barrymore" style="max-width:100%">Здравствуйте. \
Я Бэрримор. Я помню ваши нити и разговоры, обращаюсь к внешним исполнителям \
и ничего не делаю без вашего ведома. Напишите, с чем помочь.</div>`;
}

// ---------- нить разговора ----------

// Нить обозначена строкой над разговором, а не карточкой в потоке.
//
// Строка отвечает ровно на один вопрос — «о чём этот разговор». Полное
// состояние Бэрримор показывает по нажатию, в сайдбаре, не уводя владельца
// с Приёмной.
let threadContext = null;

async function loadThreadState() {
  const line = $("thread-line");
  if (!currentConversation) {
    threadContext = null;
    line.hidden = true;
    if (detailKind === "thread") closeDetail();
    return;
  }
  try {
    const d = await api(`/api/v1/conversations/${currentConversation}`);
    const t = d.thread?.thread;
    if (!t) {
      threadContext = null;
      line.hidden = true;
      if (detailKind === "thread") closeDetail();
      return;
    }
    threadContext = d;
    line.hidden = false;
    line.innerHTML = `Нить: <span class="name">${esc(t.title)}</span> · ${esc(say(t.state))}`;
    line.title = "показать состояние нити";
    if (detailKind === "thread") showThreadDetail();
  } catch {
    // Нить — не главное в разговоре: без неё разговор всё равно работает.
    line.hidden = true;
  }
}

$("thread-line").addEventListener("click", () => {
  if (detailKind === "thread") {
    closeDetail();
    return;
  }
  showThreadDetail();
});

// ---------- прошлые разговоры ----------
//
// Разговор не должен пропадать оттого, что начался следующий. Список живёт
// в сайдбаре по той же причине, что и всё остальное: смотреть прошлое —
// не повод уходить с Приёмной, а держать его постоянно на виду незачем.
$("talk-history").addEventListener("click", () => {
  if (detailKind === "history") {
    closeDetail();
    return;
  }
  showHistoryDetail();
});

async function showHistoryDetail() {
  detailKind = "history";
  setAffairs(true);
  $("affairs-detail").innerHTML =
    `<div class="affair-body">Смотрю, о чём говорили…</div>`;
  try {
    // Нити подгружаются заодно: разговор называют по делу, к которому он
    // отнесён, а не по своему идентификатору.
    await loadThreads();
    const d = await api("/api/v1/conversations");
    // Пустые разговоры не показываются: начатый и брошенный экран — не память
    // о разговоре, а след нажатия кнопки. Текущий виден всегда.
    const items = (d.items || []).filter(
      (c) => c.id === currentConversation || c.updated_at !== c.created_at);
    if (detailKind !== "history") return;
    $("affairs-detail").innerHTML = `
      <div class="affair-body" style="border-bottom:1px solid var(--line);
           padding-bottom:12px; margin-bottom:4px">
        <div class="row">
          <strong>Прошлые разговоры</strong>
          <span class="grow"></span>
          <button class="ghost" onclick="closeDetail()" title="свернуть"
            style="padding:2px 9px">×</button>
        </div>
        ${
          items.length
            ? `<ul class="plain">${items.map((c) => `
                <li class="clickable" onclick="openConversation('${esc(c.id)}')">
                  <div>${
                    c.id === currentConversation ? `${tag("этот", "ok")} ` : ""
                  }${esc(c.title || threadTitles.get(c.thread_id) || "Разговор без нити")}</div>
                  <div class="muted">${when(c.updated_at)}</div>
                </li>`).join("")}</ul>`
            : `<p class="muted">Разговоров пока не было.</p>`
        }
      </div>`;
  } catch (err) {
    $("affairs-detail").innerHTML =
      `<div class="affair-body">Список разговоров не читается: ${esc(err.message)}</div>`;
  }
}

window.openConversation = async (id) => {
  if (id === currentConversation) {
    closeDetail();
    return;
  }
  clearActiveTurn();
  currentConversation = id;
  turnAffairs = [];
  closeDetail();
  await loadChat();
  await restoreCurrentTurn();
  await loadThreadState();
  await refreshAffairs();
};

function showThreadDetail() {
  const d = threadContext;
  if (!d?.thread?.thread) return;
  const t = d.thread.thread;
  const open = (d.thread.questions || []).filter((q) => q.status === "open");
  const decided = d.thread.decisions || [];
  // Действующие позиции: устаревшие получают срок действия, а не удаляются.
  const live = (d.thread.positions || []).filter((x) => !x.valid_until);
  const orders = (d.orders || []).filter((o) => o.state !== "cancelled");

  detailKind = "thread";
  setAffairs(true);
  $("affairs-detail").innerHTML = `
    <div class="affair-body" style="border-bottom:1px solid var(--line);
         padding-bottom:12px; margin-bottom:4px">
      <div class="row">
        <strong>${esc(t.title)}</strong> ${tag(say(t.state))}
        <span class="grow"></span>
        <button class="ghost" onclick="closeDetail()" title="свернуть"
          style="padding:2px 9px">×</button>
      </div>
      ${canonBlock(t.canon || {}, t.id)}
      ${
        decided.length
          ? `<div style="margin-top:10px"><strong style="font-size:13px">О чём договорились</strong>
             <ul class="plain">${decided.slice(0, 3).map((x) => `
               <li>${esc(x.statement)}
               <span class="muted">— ${x.decided_by === "person" ? "решили вы" : "решил Бэрримор"}</span>
               </li>`).join("")}</ul></div>`
          : ""
      }
      ${
        live.length
          ? `<div style="margin-top:10px">
             ${sideColumn("Вы", live.filter((x) => x.owner === "person"))}
             ${sideColumn("Бэрримор", live.filter((x) => x.owner === "barrymore"))}
             </div>`
          : ""
      }
      ${
        open.length
          ? `<div style="margin-top:10px"><strong style="font-size:13px">Открытые вопросы</strong>
             <ul class="plain">${open.map((q) => `<li>${esc(q.question)}</li>`).join("")}</ul></div>`
          : ""
      }
      ${
        orders.length
          ? `<div class="muted" style="margin-top:10px">Поручения по нити:
             ${orders.map((o) => `<a href="#" onclick="openOrderFromTalk('${esc(o.id)}');return false">${
               esc(o.title)}</a> ${esc(say(o.state))}`).join(" · ")}</div>`
          : ""
      }
      <div class="row">
        <button class="ghost" onclick="openThreadFromTalk('${esc(t.id)}')">Открыть в «Нитях»</button>
        <button class="ghost" onclick="detachThread()">Не про эту нить</button>
      </div>
      ${techNote(`<div style="margin-top:6px">${esc(t.id)} · рев. ${t.revision}</div>`)}
    </div>`;
}

// canonBlock показывает состояние нити так, как о нём спрашивают вслух.
//
// Идентификатор нужен только для отмены: у ещё не заведённой нити его нет,
// и отменять там нечего.
function canonBlock(c, threadID) {
  const rows = [
    ["Чего хотим", c.goal],
    ["Где остановились", c.situation],
    ["Что мешает", (c.obstacles || []).join("; ")],
    ["Чего ждём", (c.waiting || []).join("; ")],
    ["Следующий шаг", c.next_step],
  ].filter(([, v]) => v);

  if (!rows.length) {
    return `<p class="muted" style="margin:8px 0 0">Бэрримор пока ничего не
      утверждает об этой нити — состояние появится, как только будет о чём сказать.</p>`;
  }
  const src = c.source && threadID
    ? `<div class="muted" style="margin-top:8px">записано по источнику «${esc(c.source)}»${
        c.updated_at ? ` · ${ago(c.updated_at)}` : ""
      } · <a href="#" onclick="undoCanon('${esc(threadID)}');return false">вернуть прежнее</a></div>`
    : "";
  return `<dl class="kv" style="margin:10px 0 0">
    ${rows.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join("")}
  </dl>${src}`;
}

window.openThreadFromTalk = (id) => {
  showTab("threads");
  openThread(id);
};

window.openOrderFromTalk = (id) => {
  showTab("orders");
  openOrder(id);
};

window.detachThread = async () => {
  if (!currentConversation) return;
  if (!confirm("Отвязать разговор от нити? Сама нить останется на месте.")) return;
  await api(`/api/v1/conversations/${currentConversation}/thread`, {
    method: "POST", body: JSON.stringify({ thread_id: "", why: "владелец отвязал" }),
  });
  closeDetail();
  turnAffairs = turnAffairs.filter((i) => i.id !== "thread-attached");
  await loadThreadState();
  await refreshAffairs();
};

window.undoCanon = async (threadID) => {
  if (!threadID) return;
  await api(`/api/v1/threads/${threadID}/canon/undo`, { method: "POST", body: "{}" });
  await loadThreadState();
  if (openThreadID === threadID) openThread(threadID);
};

// ---------- реплики ----------

// modelName сокращает модель до имени. Полный путь к весам в подписи реплики
// не сообщает ничего, кроме того, что кто-то поленился его сократить.
function modelName(v) {
  const base = String(v).split("/").pop();
  return base.replace(/\.gguf$/i, "");
}

function bubble(m) {
  const who = m.role === "person" ? "Вы" : "Бэрримор";
  // Модель, задержка и токены — не часть разговора. В обычном режиме их
  // не видно вовсе; след извлечения объясняет, почему ответ получился таким.
  const meta = [];
  if (m.model) meta.push(esc(modelName(m.model)));
  if (m.latency_ms) meta.push(`${Math.round(m.latency_ms / 1000)} с`);
  if (m.output_tokens) meta.push(`${m.prompt_tokens}+${m.output_tokens} токенов`);
  const trace = (m.retrieval_trace || []).length
    ? `<div class="meta">подано в контекст: ${m.retrieval_trace.map(esc).join("; ")}</div>`
    : "";
  const performance = [];
  if (m.role === "barrymore" && (m.prompt_tokens || m.output_tokens)) {
    performance.push(`${m.prompt_tokens || 0} вход · ${m.output_tokens || 0} выход`);
  }
  if (m.role === "barrymore" && m.generation_tokens_per_second) {
    performance.push(`${Number(m.generation_tokens_per_second).toFixed(1)} ток/с`);
  }
  if (m.role === "barrymore" && m.turn_latency_ms) {
    performance.push(`${Math.round(m.turn_latency_ms / 1000)} с`);
  }
  const performanceLine = performance.length
    ? `<div class="turn-metrics">${performance.map(esc).join(" · ")}</div>`
    : "";
  return `<div class="bubble ${esc(m.role)}"><div class="meta">${who} · ${
    when(m.created_at)}${meta.length ? techNote(` · ${meta.join(" · ")}`) : ""
  }</div><div class="said">${esc(m.content)}</div>${performanceLine}${
    trace ? `<span class="tech-only">${trace}</span>` : ""}</div>`;
}

// restore=false нужен, когда следом всё равно пересобирается сайдбар:
// два подряд обновления подряд дают заметный мигающий скачок.
//
// Активный ход после перерисовки восстанавливается из TurnRun; DOM больше не
// является единственным местом, где хранится факт выполняющейся работы.
async function loadChat(restore = true) {
  if (!currentConversation) return;
  try {
    const d = await api(`/api/v1/conversations/${currentConversation}/messages`);
    const items = d.items || [];
    $("chat").innerHTML = items.length ? items.map(bubble).join("") : greeting();
    $("chat").scrollTop = $("chat").scrollHeight;
  } catch (err) {
    $("chat").innerHTML = `<div class="muted">${esc(err.message)}</div>`;
  }
  if (restore) await restoreProposals();
}

// restoreProposals возвращает последнее предложение после перезагрузки.
//
// Предложение живёт в журнале, а не в браузере. Терять из-за обновления
// вкладки готовое к одному нажатию поручение было бы обидно и незачем.
async function restoreProposals() {
  try {
    const d = await api(`/api/v1/conversations/${currentConversation}/proposal`);
    const conv = await api(`/api/v1/conversations/${currentConversation}`);
    const t = conv.thread?.thread;

    // Уже оформленное поручение не предлагается заново: после перезагрузки
    // кнопка «Поручить» рядом с созданным поручением звала бы завести второе
    // такое же.
    // Номер предложения сервер читает из журнала, поэтому фильтрация не должна
    // его сдвигать: исходная позиция едет вместе с предложением.
    const done = new Set((conv.orders || []).map((o) => o.goal));
    const proposal = { ...d.proposal,
      work_order_proposals: (d.proposal.work_order_proposals || [])
        .map((o, i) => ({ ...o, _index: i }))
        .filter((o) => !done.has(o.goal)) };

    takeTurn({
      proposal,
      reply: { id: d.message_id },
      memory_candidates: [],
      // Что стало с нитью, видно по самой нити: предложение заводить её
      // показывается лишь до тех пор, пока разговор ни к чему не отнесён.
      thread: t
        ? { thread_id: t.id, title: t.title }
        : threadOutcomeFrom(d.proposal),
    });
  } catch {
    turnAffairs = [];
  }
}

function threadOutcomeFrom(p) {
  const m = p?.thread_match;
  if (!m || !m.new_thread_title) return {};
  return {
    proposed: {
      title: m.new_thread_title, kind: m.new_thread_kind, why: m.why,
      state: p.thread_state || {},
    },
    why: m.why,
  };
}

$("talk-new").addEventListener("click", async () => {
  try {
    const c = await api("/api/v1/conversations", {
      method: "POST",
      body: JSON.stringify({ thread_id: "", title: "" }),
    });
    clearActiveTurn();
    currentConversation = c.id;
    turnAffairs = [];
    await loadChat(false);
    await restoreCurrentTurn();
    // Карточка нити принадлежит разговору, а не экрану. Без этого на новом
    // разговоре оставалась нить прежнего — и владелец читал бы состояние
    // одного дела, разговаривая о другом.
    await loadThreadState();
    await refreshAffairs();
  } catch (err) {
    alert(`Разговор не начат: ${err.message}`);
  }
});

async function send() {
  if (sending || activeTurn) return;
  const text = $("talk-input").value.trim();
  if (!text) return;

  if (!currentConversation) {
    const c = await api("/api/v1/conversations", {
      method: "POST",
      body: JSON.stringify({ thread_id: "", title: "" }),
    });
    clearActiveTurn();
    currentConversation = c.id;
  }

  sending = true;
  setComposerState();
  $("talk-input").value = "";
  // Приветствие — не реплика: как только разговор начался, оно уходит.
  // Реплика владельца есть в любом непустом разговоре, поэтому её отсутствие
  // и означает «здесь пока только приветствие».
  if (!$("chat").querySelector(".bubble.person")) $("chat").innerHTML = "";
  $("chat").insertAdjacentHTML("beforeend",
    `<div class="bubble person"><div class="meta">Вы · только что</div>
     <div class="said">${esc(text)}</div></div>`);
  $("chat").scrollTop = $("chat").scrollHeight;

  try {
    const conversationID = currentConversation;
    const accepted = await api(`/api/v1/conversations/${conversationID}/messages`, {
      method: "POST", body: JSON.stringify({ text }),
    });
    sending = false;
    await loadChat(false);
    if (currentConversation !== conversationID) return;
    setActiveTurn({
      id: accepted.turn_id,
      conversation_id: conversationID,
      status: accepted.status,
      stage: "queued",
      stage_label: "Готовлю ход",
      created_at: new Date().toISOString(),
    });
    await refreshTurn(accepted.turn_id);
  } catch (err) {
    sending = false;
    removeProgressRow();
    $("chat").insertAdjacentHTML("beforeend",
      `<div class="bubble barrymore"><span class="tag bad">не отвечено</span>
      <div style="margin-top:6px">${esc(err.message)}</div></div>`);
  } finally {
    sending = false;
    setComposerState();
  }
}

// takeTurn превращает сказанное Бэрримором в дела для сайдбара.
//
// Каждое дело доводится до конца одним нажатием: нить заводится вместе
// с состоянием, поручение оформляется вместе с целью, причиной, каталогом
// и критериями. Ничего из этого владелец не вводит повторно — сервер берёт
// предложение из журнала, то есть из того, что Бэрримор действительно сказал.
let lastMessageID = "";
let lastTurn = null;

function takeTurn(turn) {
  const p = turn.proposal || {};
  const cands = turn.memory_candidates || [];
  const orders = p.work_order_proposals || [];
  const questions = p.open_questions || [];
  const th = turn.thread || {};
  lastMessageID = turn.reply?.id || "";
  lastTurn = turn;

  const out = [];

  if (th.refused) {
    // Отказ без выхода из положения — тупик. Назвать нить владелец может сам,
    // и делать это на другой вкладке ему незачем.
    out.push({
      id: "thread-refused", group: "needs", need: true,
      what: "Разговор не удалось отнести к нити", which: "нити",
      body: `<div>${esc(th.refused)}</div>
        <div class="row">
          <input id="thread-manual" class="grow" placeholder="как назвать нить">
        </div>
        <div class="row">
          <button class="ghost" onclick="startThreadFromTalk(true)">Завести нить</button>
        </div>`,
    });
  } else if (th.proposed) {
    out.push({
      id: "thread-new", group: "needs", need: true,
      what: `Похоже, это новое дело: «${th.proposed.title}»`, which: "новая нить",
      body: `${th.proposed.why ? `<div class="muted">${esc(th.proposed.why)}</div>` : ""}
        ${canonBlock({
          goal: th.proposed.state?.goal, situation: th.proposed.state?.situation,
          next_step: th.proposed.state?.next_step,
          obstacles: th.proposed.state?.obstacles, waiting: th.proposed.state?.waiting,
        })}
        <div class="row">
          <button class="act" onclick="startThreadFromTalk()">Завести нить</button>
        </div>
        <div class="muted" style="margin-top:6px">заведу с этим названием
          и состоянием; править можно потом</div>`,
    });
  } else if (th.attached) {
    // Связывание Бэрримор делает сам, и молчать об этом нельзя: незаметное
    // действие, о котором владелец не знает, доверия не добавляет.
    out.push({
      id: "thread-attached", group: "changed",
      what: "Отнёс разговор к нити", which: th.title || th.thread_id,
      body: `<div>${th.why ? esc(th.why) : "выбрана по смыслу разговора"}</div>
        <div class="row">
          <button class="ghost" onclick="detachThread()">Не про эту нить</button>
        </div>`,
    });
  }

  // Собственные умения идут первыми: если Бэрримор может посмотреть сам,
  // владелец должен увидеть это раньше предложения кого-то звать.
  (turn.own_actions || []).forEach((a, i) => {
    if (a.refused) {
      out.push({
        id: `own-refused-${i}`, group: "changed",
        what: "Не смог сделать это сам", which: "умения",
        body: `<div>${esc(a.refused)}</div>`,
      });
      return;
    }
    out.push({
      id: `own-${i}`, group: "needs", need: true,
      what: `Могу посмотреть сам: ${a.title}`,
      which: th.title || (th.proposed ? th.proposed.title : "") || "разговор",
      body: ownActionBody(a, i),
    });
  });

  orders.forEach((o, i) => {
    const idx = o._index ?? i;
    out.push({
      id: `proposal-${idx}`, group: "needs", need: true,
      what: `Предлагаю поручить: ${o.title || o.goal}`,
      which: th.title || (th.proposed ? th.proposed.title : "") || "разговор",
      body: orderProposalBody(o, idx),
    });
  });

  for (const c of cands) {
    out.push(c.auto
      ? {
          id: `mem-${c.item_id}`, group: "changed",
          what: "Записал в память", which: c.type,
          body: `<div>${esc(c.content)}</div>
            <div class="muted" style="margin-top:4px">${esc(c.reason || "")}</div>
            <div class="row">
              <button class="ghost" onclick="forgetMemory('${esc(c.item_id)}')">Удалить из памяти</button>
            </div>`,
        }
      : {
          id: `mem-${c.id}`, group: "needs", need: true,
          what: "Запомнить это?", which: c.type,
          body: `<div>${esc(c.content)}</div>
            <div class="muted" style="margin-top:4px">${esc(c.reason || "")}</div>
            <div class="row">
              <button class="act" onclick="acceptMemory('${esc(c.id)}')">Запомнить</button>
              <button class="ghost" onclick="rejectMemory('${esc(c.id)}')">Не надо</button>
            </div>`,
        });
  }

  // Открытые вопросы, попавшие в нить, уже видны в её состоянии. Показать их
  // ещё и здесь значило бы предложить владельцу прочитать одно и то же дважды.
  // Предложенная нить считается так же: вопросы уедут в неё при заведении.
  if (questions.length && !th.thread_id && !th.attached && !th.proposed) {
    out.push({
      id: "questions", group: "changed",
      what: "Остались открытые вопросы", which: "разговор",
      body: `<ul class="plain">${questions.map((q) => `<li>${esc(q)}</li>`).join("")}</ul>`,
    });
  }

  turnAffairs = out;
  // Предложение — ответ на только что сказанное владельцем, поэтому сайдбар
  // открывается сам. Требование «закрыт по умолчанию» относится к приходу
  // на экран, а не к ответу на собственное действие.
  if (out.some((i) => i.need)) {
    setAffairs(true);
    for (const i of out) if (i.need) openAffairs.add(i.id);
  }
}

// ownActionBody объясняет, что именно Бэрримор посмотрит и чего это стоит.
//
// Цена названа прямо и не случайно: она и есть довод в пользу того, чтобы
// не звать исполнителя. Секунда против минуты — разница, которую владелец
// должен видеть, а не принимать на веру.
function ownActionBody(a, i) {
  return `
    <div>${esc(a.question)}</div>
    ${a.why ? `<div class="muted" style="margin-top:4px">почему: ${esc(a.why)}</div>` : ""}
    <dl class="kv" style="margin-top:8px">
      <dt>каталог</dt><dd>${
        a.target
          ? esc(a.target)
          : `<input id="skill-target-${i}" placeholder="/home/…/git/rollboard">`
      }</dd>
      <dt>чем это будет</dt><dd>своими средствами, сразу и бесплатно;
        читаю, ничего не меняю</dd>
    </dl>
    <div class="row">
      <button class="act" onclick="runSkill('${esc(a.skill_id)}', ${i}, '${esc(a.target || "")}')">
        Посмотрите</button>
    </div>`;
}

// orderProposalBody показывает поручение целиком — до того, как оно создано.
//
// Владелец видит ровно то, что уйдёт исполнителю, и решает по существу:
// каталог и право менять файлы названы прямо, а не спрятаны в форму.
function orderProposalBody(o, i) {
  const write = o.needs_write;
  return `
    <div>${esc(o.goal)}</div>
    <div class="muted" style="margin-top:4px">почему: ${esc(o.why)}</div>
    <dl class="kv" style="margin-top:8px">
      <dt>каталог</dt><dd>${
        o.workspace_hint
          ? esc(o.workspace_hint)
          : `<input id="wo-root-${i}" placeholder="/home/…/git/rollboard">`
      }</dd>
      ${
        (o.acceptance_criteria || []).length
          ? `<dt>что считается сделанным</dt><dd>${
              o.acceptance_criteria.map(esc).join("; ")}</dd>`
          : ""
      }
      <dt>доступ</dt><dd>${
        write
          ? `${tag("нужна правка файлов", "warn")} исполнитель работает в копии; ваш каталог
             не тронут, изменения дойдут только по вашему решению`
          : "только чтение"
      }</dd>
    </dl>
    <div class="row">
      <button class="act" onclick="orderFromTalk(${i})">Поручить</button>
      <label class="switch"><input type="checkbox" id="wo-write-${i}" ${
        write ? "checked" : ""
      }> разрешить менять код</label>
    </div>`;
}

// runSkill применяет умение Бэрримора и возвращает ответ в разговор.
//
// Подтверждения здесь нет намеренно: умение только читает и только то, что
// владелец уже разрешил. Спрашивать разрешения на каждый взгляд в каталог
// значило бы превратить собственное умение в ту же бюрократию, ради обхода
// которой оно и заведено.
window.runSkill = async (skillID, index, target) => {
  const field = document.getElementById(`skill-target-${index}`);
  const where = (field ? field.value : target || "").trim();
  if (!where) {
    field?.focus();
    return;
  }
  affairNotes.set(`own-${index}`, "Смотрю…");
  renderAffairs();
  try {
    await api(`/api/v1/skills/${skillID}/apply`, {
      method: "POST",
      body: JSON.stringify({ target: where, conversation_id: currentConversation }),
    });
    turnAffairs = turnAffairs.filter((it) => it.id !== `own-${index}`);
    affairNotes.delete(`own-${index}`);
    await loadChat(false);
    await loadThreadState();
    await refreshAffairs();
  } catch (err) {
    affairNotes.set(`own-${index}`, `Посмотреть не вышло: ${err.message}`);
    renderAffairs();
  }
};

// learnSkill принимает предложенный способ работы.
window.learnSkill = async (suggestionID) => {
  try {
    await api("/api/v1/skills/learn", {
      method: "POST", body: JSON.stringify({ suggestion_id: suggestionID }),
    });
    await refreshAffairs();
  } catch (err) {
    affairNotes.set(`learn-${suggestionID}`, `Не освоено: ${err.message}`);
    renderAffairs();
  }
};

// startThreadFromTalk заводит нить по предложению. Название, вид и состояние
// сервер берёт из журнала — переносить их через браузер незачем.
window.startThreadFromTalk = async (manual) => {
  const body = { message_id: lastMessageID };
  if (manual) {
    const field = document.getElementById("thread-manual");
    body.title = (field?.value || "").trim();
    if (!body.title) {
      field?.focus();
      return;
    }
  }
  try {
    const th = await api(`/api/v1/conversations/${currentConversation}/threads`, {
      method: "POST", body: JSON.stringify(body),
    });
    // Предложение стало нитью: пересобираем тот же ход, чтобы кнопка
    // «Завести нить» не осталась висеть рядом с уже заведённой.
    if (lastTurn) {
      lastTurn.thread = {
        thread_id: th.id, title: th.title, attached: true,
        why: "нить заведена по вашему решению",
      };
      takeTurn(lastTurn);
    }
    await loadThreadState();
    await loadThreads();
    await refreshAffairs();
  } catch (err) {
    affairNotes.set(manual ? "thread-refused" : "thread-new", `Нить не заведена: ${err.message}`);
    renderAffairs();
  }
};

// orderFromTalk оформляет поручение и сразу показывает, что именно
// подтверждается: исполнитель, каталог и право менять файлы.
window.orderFromTalk = async (index) => {
  const rootField = document.getElementById(`wo-root-${index}`);
  const writeField = document.getElementById(`wo-write-${index}`);
  const body = { message_id: lastMessageID, index };
  if (rootField) body.workspace_root = rootField.value.trim();
  if (writeField) body.allow_write = writeField.checked;

  try {
    const p = await api(`/api/v1/conversations/${currentConversation}/work-orders`, {
      method: "POST", body: JSON.stringify(body),
    });
    // Предложение стало поручением и больше не предложение; подтверждение
    // раскрывается сразу — оно и есть следующий шаг владельца.
    turnAffairs = turnAffairs.filter((i) => i.id !== `proposal-${index}`);
    affairNotes.delete(`proposal-${index}`);
    if (p.approval?.id) openAffairs.add(`approval-${p.approval.id}`);
    await loadOrders();
    await refreshAffairs();
  } catch (err) {
    affairNotes.set(`proposal-${index}`, `Поручение не создано: ${err.message}`);
    renderAffairs();
  }
};

window.approveAndStart = async (approvalID, orderID) => {
  try {
    await api(`/api/v1/approvals/${approvalID}/grant`, {
      method: "POST", body: JSON.stringify({ decided_by: "владелец" }),
    });
    await api(`/api/v1/work-orders/${orderID}/start`, { method: "POST", body: "{}" });
    // Подтверждение исчезает из ожидающих, а поручение появляется среди
    // идущих — раскрытым, чтобы владелец увидел, куда делась кнопка.
    openAffairs.add(`run-${orderID}`);
    await loadOrders();
    await loadThreadState();
    await refreshAffairs();
  } catch (err) {
    affairNotes.set(`approval-${approvalID}`, `Не запущено: ${err.message}`);
    renderAffairs();
  }
};

window.denyFromTalk = async (approvalID) => {
  await api(`/api/v1/approvals/${approvalID}/deny`, {
    method: "POST", body: JSON.stringify({ decided_by: "владелец", reason: "не сейчас" }),
  });
  await loadOrders();
  await refreshAffairs();
};

$("talk-send").addEventListener("click", send);
$("talk-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) send();
});

// ---------- память ----------

window.acceptMemory = async (id) => {
  await api(`/api/v1/memory/candidates/${id}/accept`, { method: "POST", body: "{}" });
  loadMemory();
  turnAffairs = turnAffairs.filter((i) => i.id !== `mem-${id}`);
  refreshAffairs();
};

window.rejectMemory = async (id) => {
  await api(`/api/v1/memory/candidates/${id}/reject`, { method: "POST", body: "{}" });
  loadMemory();
  turnAffairs = turnAffairs.filter((i) => i.id !== `mem-${id}`);
  refreshAffairs();
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

  // Исправная связь — служебная подробность и в обычном режиме не видна.
  // Потерянная видна всегда: молча отставший экран хуже честной надписи.
  src.onopen = () => {
    $("live").textContent = "поток: подключён";
    $("live").className = "tag ok tech-only";
    if (activeTurn?.id) refreshTurn(activeTurn.id).catch(() => {});
  };

  src.onmessage = (msg) => handleEvent(msg);
  // Именованные события приходят с типом события домена.
  src.addEventListener("error", () => {
    $("live").textContent = "связь потеряна, восстанавливаю";
    $("live").className = "tag warn";
  });

  src.addEventListener("conversation.turn.progress", (msg) => {
    try {
      updateLiveProgress(JSON.parse(msg.data));
    } catch {
      // Повреждённый ephemeral snapshot не меняет durable состояние хода.
    }
  });

  // EventSource сам переподключается и присылает Last-Event-ID,
  // поэтому пропущенные события догоняются из журнала.
  src.addEventListener("message", handleEvent);
  ["thread.created", "work_order.proposed", "worker_run.started", "worker_run.exited",
   "discrepancy.detected", "reflex.started", "reflex.completed", "reflex.failed",
   "escalation.requested", "verification.completed", "work_order.state.changed",
   "observation.recorded", "expectation.created", "expectation.satisfied",
   "conversation.turn.queued", "conversation.turn.started",
   "conversation.turn.stage.changed", "conversation.turn.completed",
   "conversation.turn.failed", "conversation.turn.interrupted",
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
    // Поручение дошло до конца — обращение об этом должно появиться в делах
    // сразу, а не через десять секунд опроса.
    refreshAffairs();
  }
  // Нить Бэрримор обновляет сам — в том числе пока владелец смотрит на неё.
  // Карточка, отставшая от журнала, показывала бы вчерашнее положение дел.
  if (env.event_type === "thread.canon.updated" ||
      env.event_type.startsWith("conversation.thread.")) {
    loadThreadState();
    if (openThreadID) openThread(openThreadID);
  }
  if (env.event_type.startsWith("conversation.turn.")) {
    const turn = env.payload;
    if (!turn || turn.conversation_id !== currentConversation) return;
    if (["completed", "failed", "interrupted"].includes(turn.status)) {
      if (activeTurn?.id === turn.id ||
          localStorage.getItem(turnStorageKey(turn.conversation_id)) === turn.id) {
        handleTerminalTurn(turn);
      }
    } else if (activeTurn?.id === turn.id) {
      setActiveTurn(turn);
    }
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
    renderCostPolicy(s);
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

// renderCostPolicy показывает выбор в тех словах, в каких он принимается.
//
// Не «free / prefer-free / any», а «чем можно платить»: владелец решает про
// свои деньги, а не про имя политики в коде.
function renderCostPolicy(d) {
  const box = $("cost-policy");
  if (!box) return;
  const current = d.model_policy || "";
  box.innerHTML = (d.model_policies || []).map((p) => `
    <li>
      <label class="switch" style="align-items:flex-start">
        <input type="radio" name="cost-policy" value="${esc(p.name)}"
          ${current === describeCost(p.name) ? "checked" : ""}>
        <span>
          <span style="color:var(--ink)">${esc(p.title)}</span>
          <span class="muted" style="display:block">${esc(p.why)}</span>
        </span>
      </label>
    </li>`).join("");
  box.querySelectorAll("input[name=cost-policy]").forEach((el) => {
    el.addEventListener("change", async () => {
      try {
        await api("/api/v1/settings/model-policy", {
          method: "POST", body: JSON.stringify({ name: el.value }),
        });
        loadSettings();
      } catch (err) {
        alert(`Политика не изменена: ${err.message}`);
      }
    });
  });
}

// describeCost повторяет формулировку сервера: сравнивать надо с тем, что
// сервер и вернул, иначе отметка не встанет ни на один вариант.
function describeCost(name) {
  switch (name) {
    case "free": return "только бесплатные модели";
    case "any": return "разрешены любые модели, включая платные; бесплатные предпочтительнее";
    default: return "бесплатные и входящие в подписку; платные запрещены";
  }
}

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
  // Дела пересобираются на любой вкладке: обращение Бэрримора не должно
  // ждать, пока владелец сам заглянет в Приёмную.
  refreshAffairs();
}, 10000);
