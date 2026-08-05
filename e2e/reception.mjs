// E2E-проверка Приёмной: настоящий браузер, настоящий сервер, настоящая база.
//
// Проверяется главное обещание продукта: владелец начинает с разговора и
// получает нить и поручение, ни разу не открыв вкладок «Нити» и «Поручения».
// Unit-тесты API этого не показывают — они ходят тем же путём, но не глазами.
//
// Подделан ровно один слой — провайдер модели: ответ по контракту стоит минуты
// машинного времени и к поведению экрана отношения не имеет.
//
// Запуск: make e2e
import { chromium } from "playwright";
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

const PORT = Number(process.env.E2E_PORT || 7788);
const PROVIDER_PORT = Number(process.env.E2E_PROVIDER_PORT || 18099);
const BASE = `http://127.0.0.1:${PORT}`;

const started = [];
let failures = 0;

function ok(what) {
  console.log(`  ✓ ${what}`);
}

function fail(what, detail) {
  failures++;
  console.error(`  ✗ ${what}\n    ${detail}`);
}

async function check(what, fn) {
  try {
    await fn();
    ok(what);
  } catch (err) {
    fail(what, err.message);
  }
}

async function waitFor(what, fn, timeout = 30000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > deadline) throw new Error(`не дождались: ${what}`);
    await new Promise((r) => setTimeout(r, 250));
  }
}

function launch(cmd, args, env) {
  const p = spawn(cmd, args, { env: { ...process.env, ...env }, stdio: "pipe" });
  started.push(p);
  p.stderr.on("data", (d) => {
    const s = String(d);
    if (s.includes("не запустился") || s.includes("panic")) process.stderr.write(s);
  });
  return p;
}

async function main() {
  const dataRoot = await mkdtemp(path.join(tmpdir(), "barrymore-e2e-"));
  const workspace = path.join(dataRoot, "rollboard");
  await mkdir(workspace, { recursive: true });
  await writeFile(path.join(workspace, "README.md"), "тестовый репозиторий\n");

  launch("node", ["e2e/fake-provider.mjs"], {
    E2E_PROVIDER_PORT: String(PROVIDER_PORT),
    E2E_WORKSPACE: workspace,
  });
  launch("./bin/barrymored", [
    "-addr", `127.0.0.1:${PORT}`,
    "-data-root", dataRoot,
    "-workspace-roots", workspace,
    "-provider", `http://127.0.0.1:${PROVIDER_PORT}`,
    "-provider-model", "e2e",
    "-tick", "2s",
  ], {});

  await waitFor("сервер поднялся", async () => {
    try {
      return (await fetch(`${BASE}/healthz`)).ok;
    } catch {
      return false;
    }
  });

  // Обнаружение исполнителей — шаг установки, а не часть сценария: владелец
  // делает его однажды, кнопкой на вкладке «Штат». Здесь он выполняется
  // запросом, чтобы проверка «вкладки не открывались» осталась честной.
  await fetch(`${BASE}/api/v1/workers/discover`, {
    method: "POST", headers: { "content-type": "application/json" }, body: "{}",
  }).catch(() => {});

  const browser = await chromium.launch();
  const page = await browser.newPage();
  const consoleErrors = [];
  page.on("pageerror", (e) => consoleErrors.push(String(e)));
  await page.goto(BASE);

  console.log("Приёмная как единственный экран:");

  await check("нить не выбирают до разговора", async () => {
    const selects = await page.locator("#tab-talk select").count();
    if (selects > 0) throw new Error(`в Приёмной ${selects} выпадающих списков`);
  });

  await check("разговор начинается с одной реплики", async () => {
    await page.locator("#talk-input").fill("У меня Rollboard завис в worktree");
    await page.locator("#talk-send").click();
    await page.locator(".bubble.barrymore").first().waitFor({ timeout: 30000 });
  });

  await check("нить предложена с готовым состоянием", async () => {
    const box = page.locator("#talk-proposals");
    await box.getByText("Похоже, это новое дело").waitFor({ timeout: 10000 });
    for (const field of ["Чего хотим", "Где остановились", "Следующий шаг"]) {
      if (!(await box.getByText(field).count())) {
        throw new Error(`в предложении нет поля «${field}»`);
      }
    }
  });

  await check("нить заводится одним нажатием", async () => {
    await page.getByRole("button", { name: "Завести нить" }).click();
    await page.locator("#thread-state").waitFor({ state: "visible", timeout: 10000 });
    const text = await page.locator("#thread-state").innerText();
    if (!text.includes("Rollboard")) throw new Error(`в карточке нити: ${text.slice(0, 120)}`);
    if (!text.includes("разобраться")) throw new Error("состояние не перенесено в нить");
    if (!text.includes("вернуть прежнее")) throw new Error("автоматическая правка необратима");
  });

  await check("вкладки «Нити» и «Поручения» не открывались", async () => {
    const current = await page.locator("nav button[aria-current='true']").innerText();
    if (current !== "Приёмная") throw new Error(`активна вкладка «${current}»`);
  });

  // Поручение требует установленного исполнителя. Его отсутствие — не провал
  // интерфейса, и притворяться, что проверка прошла, нельзя: на машине без
  // исполнителей эта часть честно пропускается, а не тихо считается пройденной.
  const workers = await (await fetch(`${BASE}/api/v1/workers`)).json();
  const runnable = (workers.items || []).some(
    (w) => (w.worker ?? w).auth_state === "configured" && (w.worker ?? w).enabled);

  if (!runnable) {
    console.log("  ~ поручение не проверено: на машине нет настроенного исполнителя");
  } else {
    console.log("Поручение из разговора:");
    await check("предложение содержит всё, что нужно для решения", async () => {
      const box = page.locator("#talk-proposals");
      await box.getByText("Предлагаю поручить").waitFor({ timeout: 10000 });
      const text = await box.innerText();
      for (const field of ["каталог", "что считается сделанным", "доступ"]) {
        if (!text.includes(field)) throw new Error(`нет поля «${field}»`);
      }
    });

    await check("подтверждение появляется здесь же", async () => {
      await page.getByRole("button", { name: "Поручить" }).click();
      await page.locator("#talk-approval").getByText("Запустить исполнителя?")
        .waitFor({ timeout: 20000 });
      const text = await page.locator("#talk-approval").innerText();
      if (!text.includes(workspace)) throw new Error("в подтверждении не назван каталог");
      if (!text.includes("только чтение")) throw new Error("не сказано, что запуск без записи");
    });

    await check("вкладка так и не сменилась", async () => {
      const current = await page.locator("nav button[aria-current='true']").innerText();
      if (current !== "Приёмная") throw new Error(`активна вкладка «${current}»`);
    });
  }

  await check("состояние нити переживает перезагрузку страницы", async () => {
    await page.reload();
    await page.locator("#thread-state").waitFor({ state: "visible", timeout: 10000 });
    const text = await page.locator("#thread-state").innerText();
    if (!text.includes("Rollboard")) throw new Error("после перезагрузки нить пропала");
  });

  console.log("Узнавание уже существующей нити:");

  await check("новый разговор сам находит прежнюю нить", async () => {
    await page.getByRole("button", { name: "Новый разговор" }).click();
    await page.locator("#talk-input").fill("Вернёмся к Rollboard: что там с worktree?");
    await page.locator("#talk-send").click();

    // Связь Бэрримор делает сам — нажимать нечего, и это главное отличие
    // от прежнего порядка, где нить выбирали из списка до первой реплики.
    await page.locator("#thread-state").waitFor({ state: "visible", timeout: 30000 });
    const text = await page.locator("#thread-state").innerText();
    if (!text.includes("Rollboard")) throw new Error(`карточка нити: ${text.slice(0, 120)}`);
  });

  await check("владельцу сказано, что разговор отнесён к нити", async () => {
    const text = await page.locator("#talk-proposals").innerText();
    if (!text.includes("Отнёс разговор к нити")) {
      throw new Error("связь сделана молча — владелец не узнает, что произошло");
    }
  });

  await check("вторая нить не заведена", async () => {
    const threads = await (await fetch(`${BASE}/api/v1/threads`)).json();
    const count = (threads.items || []).length;
    if (count !== 1) throw new Error(`нитей ${count}, ожидалась одна: узнавание не сработало`);
  });

  await check("связь можно снять", async () => {
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Не про эту нить" }).click();
    await waitFor("карточка нити исчезла", async () => await page.locator("#thread-state").isHidden());
  });

  await check("страница не ругалась в консоль", async () => {
    if (consoleErrors.length) throw new Error(consoleErrors.join("; "));
  });

  await browser.close();
  await rm(dataRoot, { recursive: true, force: true });
}

try {
  await main();
} catch (err) {
  failures++;
  console.error("E2E сорвался:", err.message);
} finally {
  for (const p of started) p.kill("SIGTERM");
}

if (failures) {
  console.error(`\nE2E: провалов ${failures}`);
  process.exit(1);
}
console.log("\nE2E: интерфейс проходит сценарий целиком");
process.exit(0);
