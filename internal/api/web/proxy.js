// Живая настройка сети внешнего персонала.
//
// Отдельный модуль намеренно ничего не знает о внутренностях Нитей/Поручений:
// пользователь меняет одно понятное правило — через что персонал выходит в
// сеть. Runtime сам отвечает за остановку текущих worker'ов и fail-closed
// переключение маршрута.

function proxyCard() {
  return [...document.querySelectorAll("#tab-settings .card")].find((card) =>
    card.querySelector("h2")?.textContent?.trim() === "Сеть персонала");
}

async function request(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const text = await response.text();
  let body = {};
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    body = { detail: text };
  }
  if (!response.ok) {
    throw new Error(body.detail || body.title || response.statusText);
  }
  return body;
}

const card = proxyCard();
if (card) {
  card.innerHTML = `
    <h2>Сеть персонала</h2>
    <p class="muted">
      Это правило относится только к внешним исполнителям — Codex, Claude,
      OpenCode, Hermes и подключённым CLI. Сам Бэрримор и локальная модель его
      не используют.
    </p>
    <div class="row" style="margin-top:10px">
      <div class="grow">
        <label for="worker-proxy">Прокси</label>
        <input id="worker-proxy" spellcheck="false" autocomplete="off"
               placeholder="http://127.0.0.1:12334">
      </div>
      <button class="act" id="worker-proxy-apply">Применить</button>
    </div>
    <p class="muted" style="margin:8px 0 0">
      Пустое поле отключает прокси. Если адрес задан, персонал запускается в
      отдельной сетевой песочнице: недоступный прокси означает отказ запуска,
      а не попытку выйти напрямую. Изменение останавливает уже работающий
      внешний персонал, чтобы одновременно не существовало разных маршрутов.
    </p>
    <div id="worker-proxy-status" class="muted" style="margin-top:8px">загрузка…</div>
  `;

  const input = card.querySelector("#worker-proxy");
  const apply = card.querySelector("#worker-proxy-apply");
  const status = card.querySelector("#worker-proxy-status");
  let activeRuns = 0;
  let effectiveProxy = "";

  function show(message, tone = "") {
    status.textContent = message;
    status.style.color = tone === "bad" ? "var(--bad)" :
      tone === "ok" ? "var(--ok)" : "var(--muted)";
  }

  async function refresh() {
    try {
      const [policy, state] = await Promise.all([
        request("/api/v1/settings/worker-proxy"),
        request("/api/v1/system/state"),
      ]);
      effectiveProxy = policy.worker_proxy || "";
      input.value = effectiveProxy;
      activeRuns = (state.active_runs || []).length;

      const overrideNote = policy.overridden
        ? " Действующее значение было задано при запуске и отличается от settings.json; " +
          "нажатие «Применить» сохранит выбранное здесь значение."
        : "";
      if (effectiveProxy) {
        show(
          `Прокси включён. Работающих внешних процессов сейчас: ${activeRuns}.${overrideNote}`,
          "ok",
        );
      } else {
        show(`Прокси выключен. Работающих внешних процессов сейчас: ${activeRuns}.${overrideNote}`);
      }
    } catch (err) {
      show(`Состояние сети не прочитано: ${err.message}`, "bad");
    }
  }

  apply.addEventListener("click", async () => {
    const next = input.value.trim();
    if (next === effectiveProxy) {
      // POST всё равно нужен при command-line override: он синхронизирует
      // settings.json с уже действующей policy. GET сообщает это явно.
      try {
        const policy = await request("/api/v1/settings/worker-proxy");
        if (!policy.overridden) {
          show("Ничего не изменилось.");
          return;
        }
      } catch {
        // Основной POST ниже даст пользователю нормальную ошибку.
      }
    }

    if (activeRuns > 0 && next !== effectiveProxy) {
      const ok = confirm(
        `Сейчас работает внешних процессов: ${activeRuns}. ` +
        "Изменение прокси остановит текущую работу персонала. Продолжить?"
      );
      if (!ok) return;
    }

    apply.disabled = true;
    input.disabled = true;
    show("Меняю сетевую политику персонала…");
    try {
      const result = await request("/api/v1/settings/worker-proxy", {
        method: "POST",
        body: JSON.stringify({ proxy: next }),
      });
      effectiveProxy = result.worker_proxy || "";
      input.value = effectiveProxy;
      const stopped = Number(result.stopped_runs || 0);
      show(
        `${result.note}${stopped ? ` Остановлено текущих запусков: ${stopped}.` : ""}`,
        "ok",
      );
      activeRuns = 0;
    } catch (err) {
      show(`Прокси не изменён: ${err.message}`, "bad");
      input.value = effectiveProxy;
    } finally {
      apply.disabled = false;
      input.disabled = false;
    }
  });

  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") apply.click();
  });

  refresh();
}
