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

function tag(text, tone) {
  const cls = tone ?? TONE[text] ?? "";
  return `<span class="tag ${cls}">${esc(text)}</span>`;
}

// ---------- вкладки ----------

const tabs = document.querySelectorAll("nav button");
tabs.forEach((btn) => {
  btn.addEventListener("click", () => {
    tabs.forEach((b) => b.setAttribute("aria-current", String(b === btn)));
    document.querySelectorAll("main section").forEach((s) => {
      s.hidden = s.id !== `tab-${btn.dataset.tab}`;
    });
    refresh(btn.dataset.tab);
  });
});

// ---------- состояние ----------

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
        <tr><th>Разговорный слой</th><td>${esc(s.conversation_status)}</td></tr>
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
              ${tag(x.severity)} ${tag(x.status)}
              <strong>${esc(x.kind)}</strong>
              <span class="grow"></span>
              <span class="muted">${esc(x.occurrences)}× · ${ago(x.last_seen)}</span>
            </div>
            <div class="muted" style="margin-top:4px">
              ожидалось: ${esc(x.expected)}<br>наблюдалось: ${esc(x.observed)}
            </div>
            ${
              (attempts || []).length
                ? `<div class="muted" style="margin-top:4px">попытки восстановления: ${
                    attempts.map((a) => `${esc(a.policy_id)} #${a.attempt_no} → ${esc(a.outcome)}`).join("; ")
                  }</div>`
                : ""
            }
            <button class="ghost" style="margin-top:6px"
              onclick="ackDiscrepancy('${esc(x.id)}')">Принять к сведению</button>
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

async function loadThreads() {
  try {
    const d = await api("/api/v1/threads");
    const items = d.items || [];
    $("threads").innerHTML = items.length
      ? items.map((t) => `
          <li class="clickable" onclick="openThread('${esc(t.id)}')">
            <div class="row">
              ${tag(t.state)} <strong>${esc(t.title)}</strong>
              <span class="grow"></span>
              <span class="muted">${esc(t.kind)} · рев. ${t.revision}</span>
            </div>
            ${t.origin ? `<div class="muted" style="margin-top:3px">${esc(t.origin)}</div>` : ""}
          </li>`).join("")
      : `<li class="muted">нитей пока нет</li>`;

    const select = $("wo-thread");
    select.innerHTML = items
      .map((t) => `<option value="${esc(t.id)}">${esc(t.title)}</option>`)
      .join("");
  } catch (err) {
    $("threads").innerHTML = `<li class="muted">${esc(err.message)}</li>`;
  }
}

window.openThread = async (id) => {
  const box = $("thread-detail");
  box.hidden = false;
  box.innerHTML = `<span class="muted">загрузка…</span>`;
  try {
    const { thread: d, work_orders: orders } = await api(`/api/v1/threads/${id}`);
    const positions = (d.positions || []).filter((p) => !p.valid_until);
    box.innerHTML = `
      <h2>${esc(d.thread.title)}</h2>
      <div class="row">${tag(d.thread.state)} <span class="muted">${esc(d.thread.kind)}</span></div>
      ${d.thread.origin ? `<p class="muted">${esc(d.thread.origin)}</p>` : ""}

      <h2 style="margin-top:16px">Позиции сторон</h2>
      ${
        positions.length
          ? `<ul class="plain">${positions.map((p) => `
              <li><strong>${p.owner === "person" ? "Владелец" : "Бэрримор"}</strong>
              <span class="muted">уверенность ${p.confidence}</span>
              <div>${esc(p.statement)}</div>
              ${p.basis ? `<div class="muted">основание: ${esc(p.basis)}</div>` : ""}</li>`).join("")}</ul>`
          : `<p class="muted">позиции не зафиксированы</p>`
      }
      <div class="row" style="margin-top:8px">
        <input id="pos-text" class="grow" placeholder="сформулировать позицию">
        <select id="pos-owner" style="flex:0 0 150px">
          <option value="person">владелец</option>
          <option value="barrymore">Бэрримор</option>
        </select>
        <button class="ghost" onclick="addPosition('${esc(id)}')">Записать</button>
      </div>

      <h2 style="margin-top:16px">Решения</h2>
      ${
        (d.decisions || []).length
          ? `<ul class="plain">${d.decisions.map((x) => `
              <li><div>${esc(x.statement)}</div>
              <div class="muted">${esc(x.decided_by)}${
                x.rationale ? ` · ${esc(x.rationale)}` : ""
              }</div></li>`).join("")}</ul>`
          : `<p class="muted">решений нет</p>`
      }

      <h2 style="margin-top:16px">Открытые вопросы</h2>
      ${
        (d.questions || []).filter((q) => q.status === "open").length
          ? `<ul class="plain">${d.questions.filter((q) => q.status === "open")
              .map((q) => `<li>${esc(q.question)}</li>`).join("")}</ul>`
          : `<p class="muted">вопросов нет</p>`
      }

      <h2 style="margin-top:16px">Поручения нити</h2>
      ${
        (orders || []).length
          ? `<ul class="plain">${orders.map((o) => `
              <li class="clickable" onclick="openOrder('${esc(o.id)}')">
                ${tag(o.state)} ${esc(o.title)}</li>`).join("")}</ul>`
          : `<p class="muted">поручений нет</p>`
      }`;
  } catch (err) {
    box.innerHTML = `<span class="tag bad">ошибка</span> ${esc(err.message)}`;
  }
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
              доверие: ${esc(v.worker.trust_level)} ·
              учётная запись: ${esc(v.worker.auth_state)} ·
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

function refresh(tab) {
  if (tab === "state") loadState();
  if (tab === "threads") loadThreads();
  if (tab === "staff") loadWorkers();
  if (tab === "orders") { loadOrders(); loadThreads(); }
}

loadState();
loadThreads();
connectStream();
setInterval(() => {
  const current = document.querySelector('nav button[aria-current="true"]');
  if (current) refresh(current.dataset.tab);
}, 10000);
