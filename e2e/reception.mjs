// E2E-проверка Приёмной: настоящий браузер, настоящий сервер, настоящая база.
//
// Проверяется главное обещание продукта: владелец начинает с разговора и
// получает нить и поручение, ни разу не открыв вкладок «Нити» и «Поручения».
// Unit-тесты API этого не показывают — они ходят тем же путём, но не глазами.
//
// И второе обещание, уже про сам экран: Приёмная — это разговор, а не пульт.
// Состояние дел лежит справа, в сайдбаре, и не заслоняет собеседника.
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

// daylightTZ выбирает часовой пояс, в котором у сервера сейчас середина дня.
//
// Тихие часы (23:00–08:00 по местному времени сервера) — настоящее продуктовое
// поведение: ночью Бэрримор придерживает несрочное до утра. Проверять сценарий
// с обращением, зная, что после одиннадцати вечера он честно не появится,
// значит получить тест, который «иногда падает». Поэтому серверу задаётся
// пояс, а не подкручивается политика.
function daylightTZ() {
  const offset = (12 - new Date().getUTCHours() + 24) % 24;
  const east = offset <= 12;
  // В POSIX-именах Etc/GMT знак обратный: Etc/GMT-5 — это UTC+5.
  return `Etc/GMT${east ? "-" + offset : "+" + (24 - offset)}`;
}

// Служебное, чему не место в центре экрана при выключенном техническом режиме.
// Список взят из того, что интерфейс действительно умеет показать: статус
// провайдера, задержка, токены, путь к весам, идентификаторы, ревизии.
const SERVICE_DATA = [
  [/\bready\b/i, "статус провайдера"],
  [/not_configured|unreachable/i, "внутренний статус runtime"],
  [/\bпровайдер/i, "провайдер"],
  [/токен/i, "счёт токенов"],
  [/\.gguf/i, "путь к весам модели"],
  [/\bмс\b/, "задержка в миллисекундах"],
  [/\b(thr|wo|conv|msg|apr|ntc|run|wrk)_[a-z0-9]{8,}/i, "идентификатор"],
  [/рев\.\s*\d/i, "ревизия"],
  [/work_order|thread\.canon|conversation\./i, "вид события"],
];

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
  ], { TZ: daylightTZ() });

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
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const consoleErrors = [];
  page.on("pageerror", (e) => consoleErrors.push(String(e)));
  await page.goto(BASE);

  // ---------- общие проверки, которые должны держаться на всём пути ----------

  // chatIsCentral следит за тем, чтобы разговор не съёжился до виджета.
  // Это и есть та регрессия, которую словами не поймать: карточки прибывают
  // по одной, каждая «маленькая», и в какой-то момент собеседник оказывается
  // полоской посреди дашборда.
  async function chatIsCentral(where) {
    const chat = await page.locator("#chat").boundingBox();
    const talk = await page.locator(".talk").boundingBox();
    const view = page.viewportSize();
    if (!chat) throw new Error(`${where}: разговора нет на экране`);
    if (!(await page.locator("#talk-input").isVisible())) {
      throw new Error(`${where}: поля ввода не видно`);
    }
    if (chat.width < view.width * 0.5) {
      throw new Error(`${where}: разговор занимает ${Math.round(chat.width)}px из ${view.width}`);
    }
    if (chat.height < view.height * 0.4) {
      throw new Error(`${where}: разговору отдано ${Math.round(chat.height)}px высоты из ${view.height}`);
    }
    // Разговор должен быть выше всего остального в центральной колонке:
    // если карточка снова вылезет над ним, эта проверка её и поймает.
    const others = await page.locator(".talk > *:not(#chat)").all();
    for (const el of others) {
      const box = await el.boundingBox();
      if (box && box.height > chat.height) {
        throw new Error(`${where}: в центре есть блок выше разговора`);
      }
    }
    if (talk.width < view.width * 0.5) {
      throw new Error(`${where}: центральная колонка сжата до ${Math.round(talk.width)}px`);
    }
  }

  async function centreIsClean(where) {
    const text = await page.locator(".talk").innerText();
    for (const [re, what] of SERVICE_DATA) {
      const m = re.exec(text);
      if (m) throw new Error(`${where}: в центре видно ${what} — «${m[0]}»`);
    }
  }

  console.log("Первый взгляд на Приёмную:");

  await check("в центре разговор и поле ввода, и ничего больше", async () => {
    await chatIsCentral("на входе");
    const cards = await page.locator(".talk .card").count();
    if (cards) throw new Error(`в центре ${cards} карточек`);
  });

  await check("сайдбар закрыт, видна только кнопка со счётчиком", async () => {
    if (await page.locator("#affairs").isVisible()) {
      throw new Error("сайдбар раскрыт с порога");
    }
    const toggle = page.locator("#affairs-toggle");
    if (!(await toggle.isVisible())) throw new Error("кнопки дел не видно");
    const count = await page.locator("#affairs-count").innerText();
    if (!/^\d+$/.test(count.trim())) throw new Error(`счётчик показывает «${count}»`);
  });

  await check("технический переключатель убран из шапки", async () => {
    if (await page.locator("header #tech-mode").count()) {
      throw new Error("переключатель по-прежнему в основной шапке");
    }
    if (!(await page.locator("#tab-settings #tech-mode").count())) {
      throw new Error("переключатель исчез совсем: техническую глубину не вернуть");
    }
  });

  await check("нить не выбирают до разговора", async () => {
    const selects = await page.locator("#tab-talk select").count();
    if (selects > 0) throw new Error(`в Приёмной ${selects} выпадающих списков`);
  });

  console.log("Разговор заводит нить:");

  await check("разговор начинается с одной реплики", async () => {
    await page.locator("#talk-input").fill("У меня Rollboard завис в worktree");
    await page.locator("#talk-send").click();
    await page.locator(".bubble.barrymore").first().waitFor({ timeout: 30000 });
  });

  await check("предложение нити пришло в сайдбар, с готовым состоянием", async () => {
    const box = page.locator("#affairs");
    await box.getByText("Похоже, это новое дело").waitFor({ timeout: 10000 });
    for (const field of ["Чего хотим", "Где остановились", "Следующий шаг"]) {
      if (!(await box.getByText(field).count())) {
        throw new Error(`в предложении нет поля «${field}»`);
      }
    }
  });

  await check("предложение лежит под «требует вашего решения»", async () => {
    const group = await page.locator("#affairs-groups h3").first().innerText();
    if (!group.startsWith("Требует вашего решения")) {
      throw new Error(`первый раздел сайдбара — «${group}»`);
    }
  });

  await check("нить заводится одним нажатием", async () => {
    await page.getByRole("button", { name: "Завести нить" }).click();
    await page.locator("#thread-line").waitFor({ state: "visible", timeout: 10000 });
    const text = await page.locator("#thread-line").innerText();
    if (!text.includes("Rollboard")) throw new Error(`в строке нити: ${text.slice(0, 120)}`);
  });

  await check("нить обозначена строкой, а не карточкой над разговором", async () => {
    const box = await page.locator("#thread-line").boundingBox();
    if (box.height > 34) throw new Error(`строка нити высотой ${Math.round(box.height)}px`);
    if (await page.locator("#thread-state").count()) {
      throw new Error("карточка канонического состояния вернулась в поток");
    }
  });

  await check("полное состояние нити открывается тут же, в сайдбаре", async () => {
    await page.locator("#thread-line").click();
    const detail = page.locator("#affairs-detail");
    await detail.getByText("Чего хотим").waitFor({ timeout: 5000 });
    const text = await detail.innerText();
    if (!text.includes("разобраться")) throw new Error("состояние не перенесено в нить");
    if (!text.includes("вернуть прежнее")) throw new Error("автоматическая правка необратима");
    const current = await page.locator("nav button[aria-current='true']").innerText();
    if (current !== "Приёмная") throw new Error(`ушли на вкладку «${current}»`);
    await page.locator("#thread-line").click();
  });

  await check("разговор остался центральным", async () => {
    await chatIsCentral("после заведения нити");
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

  let ordered = false;
  if (!runnable) {
    console.log("  ~ поручение не проверено: на машине нет настроенного исполнителя");
  } else {
    console.log("Поручение из разговора:");

    await check("предложение содержит всё, что нужно для решения", async () => {
      const box = page.locator("#affairs");
      await box.getByText("Предлагаю поручить").waitFor({ timeout: 10000 });
      const text = await box.innerText();
      for (const field of ["каталог", "что считается сделанным", "доступ"]) {
        if (!text.includes(field)) throw new Error(`нет поля «${field}»`);
      }
    });

    await check("подтверждение появляется здесь же, в сайдбаре", async () => {
      await page.getByRole("button", { name: "Поручить", exact: true }).click();
      await page.locator("#affairs").getByText("Запустить исполнителя?")
        .waitFor({ timeout: 20000 });
      const text = await page.locator("#affairs").innerText();
      if (!text.includes(workspace)) throw new Error("в подтверждении не назван каталог");
      if (!text.includes("только чтение")) throw new Error("не сказано, что запуск без записи");
    });

    await check("решение принимается без перехода на другую вкладку", async () => {
      const before = await page.locator("nav button[aria-current='true']").innerText();
      await page.getByRole("button", { name: "Подтвердить и запустить" }).click();
      await waitFor("поручение пошло", async () => {
        const d = await (await fetch(`${BASE}/api/v1/work-orders`)).json();
        return (d.items || []).some((o) => o.state !== "proposed" && o.state !== "draft");
      });
      const after = await page.locator("nav button[aria-current='true']").innerText();
      if (before !== "Приёмная" || after !== "Приёмная") {
        throw new Error(`вкладка сменилась: было «${before}», стало «${after}»`);
      }
      ordered = true;
    });

    await check("идущая работа видна отдельно от того, что требует решения", async () => {
      const heads = await page.locator("#affairs-groups h3").allInnerTexts();
      if (heads.length !== 3) throw new Error(`разделов ${heads.length}, ожидалось три`);
      if (!heads[2].startsWith("Сейчас выполняется")) {
        throw new Error(`третий раздел — «${heads[2]}»`);
      }
    });

    await check("разговор остался центральным и после поручения", async () => {
      await chatIsCentral("после запуска поручения");
    });
  }

  await check("при выключенном техническом режиме в центре нет служебных данных", async () => {
    await centreIsClean("в разговоре");
  });

  if (ordered) {
    console.log("Пока владелец разговаривал:");
    // Настоящий прогон исполнителя занимает минуты и зависит от сети. Если он
    // не успел, это не провал интерфейса — но и не повод считать проверку
    // пройденной.
    let finished = false;
    try {
      await waitFor("поручение завершилось", async () => {
        const d = await (await fetch(`${BASE}/api/v1/work-orders`)).json();
        return (d.items || []).some((o) => ["completed", "failed"].includes(o.state));
      }, 240000);
      finished = true;
    } catch {
      console.log("  ~ обращение о завершении не проверено: исполнитель не успел за 4 минуты");
    }

    if (finished) {
      await check("обращение о завершении появилось в сайдбаре", async () => {
        await waitFor("обращение дошло до экрана", async () => {
          const text = await page.locator("#affairs").innerText();
          return text.includes("Поручение выполнено") || text.includes("Поручение не вышло");
        }, 30000);
      });

      await check("обращение говорит, к какому делу относится и почему сейчас", async () => {
        const item = page.locator("#affairs .affair", {
          hasText: /Поручение (выполнено|не вышло)/,
        }).first();
        const which = await item.locator(".affair-which").innerText();
        if (!which.includes("Rollboard")) {
          throw new Error(`дело не названо: «${which}»`);
        }
        await item.locator(".affair-head").click();
        const body = await item.locator(".affair-body").innerText();
        if (!body.includes("Почему сейчас")) throw new Error("нет ответа на «почему сейчас»");
      });

      await check("центр экрана всё это время не показывал служебного", async () => {
        await centreIsClean("после завершения поручения");
        await chatIsCentral("после завершения поручения");
      });
    }
  }

  console.log("Бэрримор делает сам:");

  await check("вместо поручения предложено собственное умение", async () => {
    await page.locator("#talk-input").fill("Посмотри сам, что там с worktree");
    await page.locator("#talk-send").click();
    await page.locator("#affairs").getByText("Могу посмотреть сам")
      .waitFor({ timeout: 30000 });
    const text = await page.locator("#affairs").innerText();
    if (text.includes("Предлагаю поручить: Аудит")) {
      throw new Error("рядом с собственным умением всё равно предложено поручение");
    }
    if (!text.includes("бесплатно")) {
      throw new Error("не сказано, чего это стоит: сравнить с поручением нечем");
    }
  });

  await check("умение применяется без подтверждения и отвечает в разговоре", async () => {
    const before = await (await fetch(`${BASE}/api/v1/work-orders`)).json();
    await page.getByRole("button", { name: "Посмотрите" }).click();
    await waitFor("ответ пришёл в разговор", async () =>
      (await page.locator("#chat").innerText()).includes("Посмотрел сам"));

    const after = await (await fetch(`${BASE}/api/v1/work-orders`)).json();
    if ((after.items || []).length !== (before.items || []).length) {
      throw new Error("собственное умение всё-таки создало поручение");
    }
    const current = await page.locator("nav button[aria-current='true']").innerText();
    if (current !== "Приёмная") throw new Error(`ушли на вкладку «${current}»`);
  });

  await check("посмотрел быстрее, чем успел бы позвать исполнителя", async () => {
    const d = await (await fetch(`${BASE}/api/v1/skills`)).json();
    const runs = d.runs || [];
    if (!runs.length) throw new Error("применение умения не записано");
    if (runs[0].status !== "done") throw new Error(`умение не сработало: ${runs[0].failure}`);
    if (runs[0].took_ms > 5000) {
      throw new Error(`умение шло ${runs[0].took_ms} мс — это уже не «сам и сразу»`);
    }
  });

  await check("в центре по-прежнему нет служебных данных", async () => {
    await centreIsClean("после собственного умения");
    await chatIsCentral("после собственного умения");
  });

  console.log("Узнавание уже существующей нити:");

  await check("новый разговор сам находит прежнюю нить", async () => {
    await page.getByRole("button", { name: "Новый разговор" }).click();
    await page.locator("#talk-input").fill("Вернёмся к Rollboard: что там с worktree?");
    await page.locator("#talk-send").click();

    // Связь Бэрримор делает сам — нажимать нечего, и это главное отличие
    // от прежнего порядка, где нить выбирали из списка до первой реплики.
    await page.locator("#thread-line").waitFor({ state: "visible", timeout: 30000 });
    const text = await page.locator("#thread-line").innerText();
    if (!text.includes("Rollboard")) throw new Error(`строка нити: ${text.slice(0, 120)}`);
  });

  await check("владельцу сказано, что разговор отнесён к нити", async () => {
    const text = await page.locator("#affairs").innerText();
    if (!text.includes("Отнёс разговор к нити")) {
      throw new Error("связь сделана молча — владелец не узнает, что произошло");
    }
  });

  await check("к прошлому разговору можно вернуться, не уходя из Приёмной", async () => {
    await page.getByRole("button", { name: "Прошлые разговоры" }).click();
    const list = page.locator("#affairs-detail");
    await list.getByText("Прошлые разговоры").waitFor({ timeout: 10000 });
    const rows = list.locator("li");
    if ((await rows.count()) < 2) {
      throw new Error(`в списке ${await rows.count()} разговоров: прежний пропал`);
    }
    await rows.last().click();
    await waitFor("открылся прежний разговор", async () =>
      (await page.locator("#chat").innerText()).includes("завис в worktree"));
    const current = await page.locator("nav button[aria-current='true']").innerText();
    if (current !== "Приёмная") throw new Error(`ушли на вкладку «${current}»`);
    // Возвращаемся к последнему разговору: дальше проверяется он.
    await page.getByRole("button", { name: "Прошлые разговоры" }).click();
    await page.locator("#affairs-detail li").first().click();
    await waitFor("вернулись к последнему разговору", async () =>
      (await page.locator("#chat").innerText()).includes("Вернёмся к Rollboard"));
  });

  await check("вторая нить не заведена", async () => {
    const threads = await (await fetch(`${BASE}/api/v1/threads`)).json();
    const count = (threads.items || []).length;
    if (count !== 1) throw new Error(`нитей ${count}, ожидалась одна: узнавание не сработало`);
  });

  await check("связь можно снять, не уходя из Приёмной", async () => {
    await page.locator("#thread-line").click();
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Не про эту нить" }).click();
    await waitFor("строка нити исчезла", async () => await page.locator("#thread-line").isHidden());
    const current = await page.locator("nav button[aria-current='true']").innerText();
    if (current !== "Приёмная") throw new Error(`ушли на вкладку «${current}»`);
  });

  console.log("После перезагрузки:");

  await check("сайдбар снова закрыт, а счётчик на месте", async () => {
    await page.reload();
    await page.locator("#affairs-toggle").waitFor({ state: "visible", timeout: 10000 });
    if (await page.locator("#affairs").isVisible()) {
      throw new Error("после перезагрузки сайдбар раскрыт");
    }
    await page.locator("#affairs-count").waitFor({ state: "visible" });
  });

  await check("разговор по-прежнему в центре", async () => {
    await chatIsCentral("после перезагрузки");
    await centreIsClean("после перезагрузки");
  });

  await check("страница не ругалась в консоль", async () => {
    if (consoleErrors.length) throw new Error(consoleErrors.join("; "));
  });

  await browser.close();
  await rm(dataRoot, { recursive: true, force: true });
}

main()
  .catch((err) => {
    console.error(err);
    failures++;
  })
  .finally(() => {
    for (const p of started) p.kill("SIGTERM");
    if (failures) {
      console.error(`\nE2E: провалов ${failures}`);
      process.exit(1);
    }
    console.log("\nE2E: интерфейс проходит сценарий целиком");
  });
