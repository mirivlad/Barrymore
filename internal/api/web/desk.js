// Первый настоящий предмет Стола: наблюдаемое состояние машины.
//
// Данные приходят типизированным JSON от runtime. Модель не генерирует ни
// значения, ни HTML, ни SVG. График строит доверенный интерфейс из чисел,
// которые Barrymore сам прочитал из /proc/statfs-источников.

const desk = document.getElementById("affairs");
const groups = document.getElementById("affairs-groups");

if (desk && groups) {
  const card = document.createElement("div");
  card.id = "desk-machine";
  card.className = "card";
  card.style.marginBottom = "12px";
  card.innerHTML = `
    <div class="row">
      <div>
        <strong>Машина сейчас</strong>
        <div id="desk-machine-host" class="muted">ещё не смотрел</div>
      </div>
      <span class="grow"></span>
      <span id="desk-machine-age" class="muted"></span>
    </div>
    <div id="desk-machine-metrics" style="margin-top:10px"></div>
  `;
  groups.before(card);

  const host = card.querySelector("#desk-machine-host");
  const age = card.querySelector("#desk-machine-age");
  const metrics = card.querySelector("#desk-machine-metrics");
  const loadHistory = [];
  let timer = null;
  let loading = false;

  function esc(value) {
    return String(value ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    })[c]);
  }

  function bytes(value) {
    let n = Number(value || 0);
    if (!Number.isFinite(n) || n <= 0) return "—";
    const units = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
    let u = 0;
    while (n >= 1024 && u < units.length - 1) {
      n /= 1024;
      u++;
    }
    return `${n >= 10 || u === 0 ? n.toFixed(0) : n.toFixed(1)} ${units[u]}`;
  }

  function duration(seconds) {
    const s = Number(seconds || 0);
    if (!Number.isFinite(s) || s <= 0) return "";
    if (s < 3600) return `${Math.floor(s / 60)} мин`;
    if (s < 86400) return `${Math.floor(s / 3600)} ч`;
    return `${Math.floor(s / 86400)} сут`;
  }

  function percent(part, total) {
    const p = Number(part || 0);
    const t = Number(total || 0);
    if (!t) return 0;
    return Math.max(0, Math.min(100, Math.round((p / t) * 100)));
  }

  function sparkline(values, cpus) {
    if (values.length < 2) {
      return `<div class="muted" style="height:44px;display:flex;align-items:center">` +
        `график появится через несколько измерений</div>`;
    }
    const width = 260;
    const height = 44;
    // Верхняя граница — хотя бы число CPU, чтобы load=1 на 14-ядерной машине
    // не выглядел как авария. Если наблюдался больший load, график расширяется.
    const ceiling = Math.max(Number(cpus || 1), ...values, 1);
    const points = values.map((v, i) => {
      const x = values.length === 1 ? 0 : (i / (values.length - 1)) * width;
      const y = height - (Math.max(0, Number(v)) / ceiling) * (height - 4) - 2;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(" ");
    return `<svg viewBox="0 0 ${width} ${height}" width="100%" height="44" ` +
      `role="img" aria-label="История load average за последние измерения">` +
      `<polyline points="${points}" fill="none" stroke="currentColor" stroke-width="2" ` +
      `vector-effect="non-scaling-stroke"></polyline></svg>`;
  }

  function metric(label, value, detail = "") {
    return `<div style="min-width:120px;flex:1;padding:8px 10px;border:1px solid var(--line);` +
      `border-radius:10px"><div class="muted">${esc(label)}</div>` +
      `<div style="font-size:1.2rem;margin-top:2px">${esc(value)}</div>` +
      `${detail ? `<div class="muted" style="margin-top:2px">${esc(detail)}</div>` : ""}</div>`;
  }

  async function refresh() {
    if (loading || desk.hidden) return;
    loading = true;
    try {
      const response = await fetch("/api/v1/desk/ambient");
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const body = await response.json();
      const s = body.snapshot || {};

      loadHistory.push(Number(s.load_1 || 0));
      if (loadHistory.length > 24) loadHistory.shift();

      host.textContent = [
        s.hostname || "машина",
        s.uptime_seconds ? `работает ${duration(s.uptime_seconds)}` : "",
      ].filter(Boolean).join(" · ");
      age.textContent = "сейчас";

      const memoryFree = Number(s.memory_available || 0);
      const memoryTotal = Number(s.memory_total || 0);
      const memoryUsedPct = memoryTotal ? 100 - percent(memoryFree, memoryTotal) : 0;
      const disks = Array.isArray(s.disks) ? s.disks : [];
      const root = disks.find((d) => d.mount === "/") || disks[0];
      const load = Number(s.load_1 || 0);

      metrics.innerHTML = `
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          ${metric("Load 1 мин", load.toFixed(2), `${s.cpus || "?"} CPU · 5м ${Number(s.load_5 || 0).toFixed(2)} · 15м ${Number(s.load_15 || 0).toFixed(2)}`)}
          ${metric("Память", memoryTotal ? `${memoryUsedPct}% занято` : "—",
            memoryTotal ? `${bytes(memoryFree)} доступно из ${bytes(memoryTotal)}` : "нет наблюдения")}
          ${metric(root ? `Диск ${root.mount}` : "Диск", root ? `${percent(root.free, root.total)}% свободно` : "—",
            root ? `${bytes(root.free)} из ${bytes(root.total)}` : "нет наблюдения")}
        </div>
        <div style="margin-top:8px;color:var(--muted)">
          <div class="muted" style="margin-bottom:3px">Load · последние ${loadHistory.length} измерений</div>
          ${sparkline(loadHistory, s.cpus)}
        </div>`;
    } catch (err) {
      host.textContent = "не удалось посмотреть состояние машины";
      metrics.innerHTML = `<div class="muted">${esc(err.message)}</div>`;
    } finally {
      loading = false;
    }
  }

  function syncPolling() {
    if (!desk.hidden) {
      refresh();
      if (!timer) timer = setInterval(refresh, 5000);
      return;
    }
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }

  const observer = new MutationObserver(syncPolling);
  observer.observe(desk, { attributes: true, attributeFilter: ["hidden"] });
  syncPolling();
}
