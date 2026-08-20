// E2E-проверка продуктовой поверхности: настоящий браузер, настоящий сервер,
// настоящая база.
//
// Главное обещание теперь сильнее прежнего: владелец начинает с разговора,
// Бэрримор сам ведёт нити и поручения, а пользователь не обязан даже видеть
// эти внутренние сущности. В обычном режиме видны Разговор, Настройки и Стол.
//
// Техническая глубина не удалена: отдельная часть сценария включает
// технический режим и убеждается, что инспекторы по-прежнему доступны.
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
// Тихие часы — настоящее продуктовое поведение, поэтому тест меняет пояс,
// а не политику инициативы.
function daylightTZ() {
  const offset = (12 - new Date().getUTCHours() + 24) % 24;
  const east = offset <= 12;
  return `Etc/GMT${east ? "-" + offset : "+" + (24 - offset)}`;
}

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

  const toolBin = path.join(dataRoot, "bin");
  await mkdir(toolBin, { recursive: true });
  await writeFile(path.join(toolBin, "crush"), `#!/bin/sh
case "$1" in
  --version) echo 'crush 1.4.2' ;;
  *) cat <<'EOF'
crush — агент для работы с кодом

Использование:
  crush run [флаги] <задание>

Флаги:
  --version        показать версию
  -h, --help       показать эту справку
  -q, --quiet      не печатать ничего лишнего
  --read-only      ничего не менять на диске
  --model строка   какую модель использовать
EOF
  ;;
esac
`, { mode: 0o755 });

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
  ], { TZ: daylightTZ(), PATH: `${toolBin}:${process.env.PATH}` });

  await waitFor("сервер поднялся", async () => {
    try {
      return (await fetch(`${BASE}/healthz`)).ok;
    } catch {
      return false;
    }
  });

  // Обнаружение исполнителей — установка/служебная работа, а не ежедневная
  // навигация владельца. Делается запросом, чтобы тест продукта не открывал Штат.
  await fetch(`${BASE}/api/v1/workers/discover`, {
    method: "POST", headers: { "content-type": "application/json" }, body: "{}",
  }).catch(() => {});

  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const consoleErrors = [];
  page.on("pageerror", (e) => consoleErrors.push(String(e)));
  await page.goto(BASE);

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

  async function currentTab() {
    return (await page.locator("nav button[aria-current='true']").innerText()).trim();
  }

  console.log("Первый взгляд на Бэрримора:");

  await check("в центре разговор и поле ввода, и ничего больше", async () => {
    await chatIsCentral("на входе");
    const cards = await page.locator(".talk .card").count();
    if (cards) throw new Error(`в центре ${cards} карточек`);
  });

  await check("обычная навигация не показывает кишки runtime", async () => {
    const visible = [];
    for (const button of await page.locator("nav button").all()) {
      if (await button.isVisible()) visible.push((await button.innerText()).trim());
    }
    const want = ["Разговор", "Настройки"];
    if (JSON.stringify(visible) !== JSON.stringify(want)) {
      throw new Error(`видимые вкладки: ${visible.join(", ")}`);
    }
    for (const internal of ["Нити", "Штат", "Поручения", "Память", "Состояние", "Журнал"]) {
      if (visible.includes(internal)) throw new Error(`в обычном режиме видна вкладка «${internal}»`);
    }
  });

  await check("Стол закрыт, видна только кнопка со счётчиком", async () => {
    if (await page.locator("#affairs").isVisible()) {
      throw new Error("Стол раскрыт с порога");
    }
    const toggle = page.locator("#affairs-toggle");
    if (!(await toggle.isVisible())) throw new Error("кнопки Стола не видно");
    if (!(await toggle.innerText()).includes("Стол")) throw new Error("Стол всё ещё называется внутренним термином");
    const count = await page.locator("#affairs-count").innerText();
    if (!/^\d+$/.test(count.trim())) throw new Error(`счётчик показывает «${count}»`);
  });

  await check("технический переключатель живёт в Настройках", async () => {
    if (await page.locator("header #tech-mode").count()) {
      throw new Error("переключатель по-прежнему в основной шапке");
    }
    if (!(await page.locator("#tab-settings #tech-mode").count())) {
      throw new Error("переключатель исчез совсем: техническую глубину не вернуть");
    }
  });

  await check("нить не выбирают и не показывают до разговора", async () => {
    if (await page.locator("#tab-talk select").count()) {
      throw new Error("в разговоре появился выбор внутренней сущности");
    }
    if (await page.locator("#thread-line").isVisible()) {
      throw new Error("в обычном режиме показана внутренняя нить");
    }
  });

  console.log("Разговор заводит внутренний контекст:");

  await check("разговор начинается с одной реплики", async () => {
    await page.locator("#talk-input").fill("У меня Rollboard завис в worktree");
    await page.locator("#talk-send").click();
    await page.locator(".bubble.barrymore").first().waitFor({ timeout: 30000 });
  });

  await check("предложение нового дела пришло на Стол с готовым состоянием", async () => {
    const box = page.locator("#affairs");
    await box.getByText("Похоже, это новое дело").waitFor({ timeout: 10000 });
    for (const field of ["Чего хотим", "Где остановились", "Следующий шаг"]) {
      if (!(await box.getByText(field).count())) {
        throw new Error(`в предложении нет поля «${field}»`);
      }
    }
  });

  await check("решение лежит на Столе под «требует вашего решения»", async () => {
    const group = await page.locator("#affairs-groups h3").first().innerText();
    if (!group.startsWith("Требует вашего решения")) {
      throw new Error(`первый раздел Стола — «${group}»`);
    }
  });

  await check("внутренняя нить создаётся, но не появляется в обычном UI", async () => {
    await page.getByRole("button", { name: "Сохранить как дело" }).click();
    await waitFor("нить записана", async () => {
      const threads = await (await fetch(`${BASE}/api/v1/threads`)).json();
      return (threads.items || []).length === 1;
    });
    if (await page.locator("#thread-line").isVisible()) {
      throw new Error("после создания наружу вылезло имя внутренней нити");
    }
    if (await page.locator("#thread-state").count()) {
      throw new Error("каноническое состояние нити вернулось в поток разговора");
    }
  });

  await check("разговор остался центральным", async () => {
    await chatIsCentral("после заведения внутреннего контекста");
    if (await currentTab() !== "Разговор") throw new Error(`активна вкладка «${await currentTab()}»`);
  });

  const workers = await (await fetch(`${BASE}/api/v1/workers`)).json();
  const runnable = (workers.items || []).some(
    (w) => (w.worker ?? w).auth_state === "configured" && (w.worker ?? w).enabled);

  let ordered = false;
  if (!runnable) {
    console.log("  ~ поручение не проверено: на машине нет настроенного исполнителя");
  } else {
    console.log("Персонал из разговора:");

    await check("Бэрримор объясняет существенное, а не показывает WorkOrder", async () => {
      const box = page.locator("#affairs");
      await box.getByText("Предлагаю поручить").waitFor({ timeout: 10000 });
      const text = await box.innerText();
      for (const field of ["каталог", "что считается сделанным", "доступ"]) {
        if (!text.includes(field)) throw new Error(`нет существенного поля «${field}»`);
      }
      if (/workorder|contextpack|heartbeat|sandbox_profile/i.test(text)) {
        throw new Error("на Стол вылез внутренний протокол поручения");
      }
    });

    await check("подтверждение появляется на Столе", async () => {
      await page.getByRole("button", { name: "Поручить", exact: true }).click();
      await page.locator("#affairs").getByText("Запустить исполнителя?")
        .waitFor({ timeout: 20000 });
      const text = await page.locator("#affairs").innerText();
      if (!text.includes(workspace)) throw new Error("в подтверждении не назван каталог");
      if (!text.includes("только чтение")) throw new Error("не сказано, что запуск без записи");
    });

    await check("решение принимается без ухода из разговора", async () => {
      const before = await currentTab();
      await page.getByRole("button", { name: "Подтвердить и запустить" }).click();
      await waitFor("поручение пошло", async () => {
        const d = await (await fetch(`${BASE}/api/v1/work-orders`)).json();
        return (d.items || []).some((o) => o.state !== "proposed" && o.state !== "draft");
      });
      const after = await currentTab();
      if (before !== "Разговор" || after !== "Разговор") {
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

    await check("разговор остался центральным и после запуска персонала", async () => {
      await chatIsCentral("после запуска исполнителя");
    });
  }

  await check("при выключенном техническом режиме в центре нет служебных данных", async () => {
    await centreIsClean("в разговоре");
  });

  if (ordered) {
    console.log("Пока владелец разговаривал:");
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
      await check("обращение о завершении появилось на Столе", async () => {
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
        if (!which.includes("Rollboard")) throw new Error(`дело не названо: «${which}»`);
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

  console.log("Пока Бэрримор думает:");

  await check("вопрос и секундомер переживают уход в Настройки", async () => {
    await page.locator("#talk-input").fill("Не спеши, подумай как следует");
    await page.locator("#talk-send").click();
    await page.locator("#thinking").waitFor({ timeout: 5000 });

    // Пользователь уходит по единственной обычной служебной вкладке. Нити для
    // проверки устойчивости чата больше не нужны и в обычном режиме не видны.
    await page.locator("nav button[data-tab='settings']").click();
    await page.waitForTimeout(800);
    await page.locator("nav button[data-tab='talk']").click();

    const chat = await page.locator("#chat").innerText();
    if (!chat.includes("Не спеши")) throw new Error("реплика владельца стёрта");
    if (!(await page.locator("#thinking").count())) {
      throw new Error("ожидание стёрто: экран молчит, будто ничего не спрашивали");
    }
  });

  await check("ответ всё равно приходит на место", async () => {
    await page.locator("#thinking").waitFor({ state: "detached", timeout: 30000 });
    const bubbles = await page.locator("#chat .bubble.barrymore").count();
    if (bubbles < 2) throw new Error("ответ не появился");
  });

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
    if (await currentTab() !== "Разговор") throw new Error(`ушли на вкладку «${await currentTab()}»`);
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

  console.log("Бэрримор учится:");

  const applySkill = (id) =>
    fetch(`${BASE}/api/v1/skills/${id}/apply`, {
      method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ target: workspace }),
    });

  await check("повторяющийся порядок действий замечен", async () => {
    for (let round = 0; round < 3; round++) {
      for (const id of ["workspace.survey", "workspace.who"]) {
        const res = await applySkill(id);
        if (!res.ok) throw new Error(`умение ${id} не применилось: ${res.status}`);
      }
    }
    const d = await (await fetch(`${BASE}/api/v1/skills`)).json();
    const seq = (d.suggestions || [])[0];
    if (!seq) throw new Error("повтор не замечен: предложений нет");
    if (seq.seen_times < 2) throw new Error(`повторов насчитано ${seq.seen_times}`);
  });

  await check("Бэрримор предлагает новый способ на Столе", async () => {
    await page.reload();
    await page.locator("#affairs-toggle").click();
    await page.locator("#affairs").getByText("Могу освоить новый способ")
      .waitFor({ timeout: 15000 });
  });

  await check("освоенное умение появляется и применимо", async () => {
    await page.locator("#affairs .affair", { hasText: "Могу освоить новый способ" })
      .locator(".affair-head").click();
    await page.getByRole("button", { name: "Осваивайте" }).click();
    await waitFor("умение освоено", async () => {
      const d = await (await fetch(`${BASE}/api/v1/skills`)).json();
      return (d.items || []).some((sk) => sk.origin === "learned");
    });
    const d = await (await fetch(`${BASE}/api/v1/skills`)).json();
    const learned = (d.items || []).find((sk) => sk.origin === "learned");
    if ((learned.steps || []).length < 2) {
      throw new Error("освоенное умение собрано не из шагов прежних");
    }
    const res = await fetch(`${BASE}/api/v1/skills/${learned.id}/apply`, {
      method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ target: workspace }),
    });
    if (!res.ok) throw new Error(`освоенное умение не применяется: ${res.status}`);
    const run = await res.json();
    if (run.status !== "done") throw new Error(`освоенное умение не сработало: ${run.failure}`);
  });

  await check("опыт виден рядом со способом, а не только в цифрах", async () => {
    const d = await (await fetch(`${BASE}/api/v1/skills`)).json();
    const p = (d.practices || []).find((x) => x.ref === "workspace.survey");
    if (!p) throw new Error("опыт по умению не записан");
    if (p.applied < 2) throw new Error(`применений насчитано ${p.applied}`);
    if (p.avg_ms <= 0) throw new Error("цена способа не измерена: сравнить не с чем");
  });

  console.log("Узнавание существующего контекста:");

  await check("новый разговор сам находит прежнюю нить, не показывая её", async () => {
    await page.getByRole("button", { name: "Новый разговор" }).click();
    await page.locator("#talk-input").fill("Вернёмся к Rollboard: что там с worktree?");
    await page.locator("#talk-send").click();

    // Одна нить существовала ещё до этой реплики, поэтому ждать просто
    // `threads.length === 1` было гонкой: условие становилось истинным раньше,
    // чем второй ход вообще успевал закончиться. Синхронизируемся по видимому
    // результату самого действия — Бэрримор сообщил, что узнал прежний контекст.
    await waitFor("прежний контекст узнан и показан на Столе", async () => {
      const text = await page.locator("#affairs").innerText();
      return text.includes("Понял, к какому делу это относится");
    }, 30000);

    const threads = await (await fetch(`${BASE}/api/v1/threads`)).json();
    if ((threads.items || []).length !== 1) {
      throw new Error(`нитей ${(threads.items || []).length}, ожидалась одна`);
    }
    if (await page.locator("#thread-line").isVisible()) {
      throw new Error("узнанная внутренняя нить появилась в обычном интерфейсе");
    }
  });

  await check("владельцу сказано человеческим языком, что контекст узнан", async () => {
    if (!(await page.locator("#affairs").isVisible())) {
      await page.locator("#affairs-toggle").click();
    }
    const text = await page.locator("#affairs").innerText();
    if (!text.includes("Понял, к какому делу это относится")) {
      throw new Error(`связь сделана молча: ${text.slice(0, 200)}`);
    }
  });

  await check("к прошлому разговору можно вернуться, не уходя с продуктовой поверхности", async () => {
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
    if (await currentTab() !== "Разговор") throw new Error(`ушли на вкладку «${await currentTab()}»`);
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

  console.log("После перезагрузки:");

  await check("Стол снова закрыт, а разговор открыт", async () => {
    await page.reload();
    await page.locator("#affairs-toggle").waitFor({ state: "visible", timeout: 10000 });
    if (await page.locator("#affairs").isVisible()) throw new Error("после перезагрузки Стол раскрыт");
    if (await currentTab() !== "Разговор") throw new Error(`после reload активна вкладка «${await currentTab()}»`);
  });

  await check("разговор по-прежнему в центре", async () => {
    await chatIsCentral("после перезагрузки");
    await centreIsClean("после перезагрузки");
  });

  console.log("Технический режим:");

  await check("внутренние инспекторы возвращаются только по явному переключателю", async () => {
    await page.locator("nav button[data-tab='settings']").click();
    await page.locator("#tech-mode").check();
    for (const tab of ["threads", "staff", "orders", "memory", "state", "journal"]) {
      if (!(await page.locator(`nav button[data-tab='${tab}']`).isVisible())) {
        throw new Error(`техническая вкладка ${tab} не вернулась`);
      }
    }
    await page.locator("nav button[data-tab='talk']").click();
    if (!(await page.locator("#thread-line").isVisible())) {
      throw new Error("инспектор текущей нити не вернулся в техническом режиме");
    }
  });

  console.log("Незнакомый инструмент:");

  await check("Бэрримор изучает его сам, по одному имени команды", async () => {
    await page.locator("nav button[data-tab='staff']").click();
    await page.locator("#harness-name").fill("crush");
    await page.getByRole("button", { name: "Изучить" }).click();
    await page.locator("#harness-result").getByText("Принять в штат")
      .waitFor({ timeout: 30000 });
    const text = await page.locator("#harness-result").innerText();
    if (!text.includes("crush run")) throw new Error(`способ запуска не выведен: ${text}`);
    if (!text.includes("--read-only")) throw new Error("режим только чтения не найден в справке");
    if (!text.includes("только чтение рабочего каталога")) {
      throw new Error("новичку выдано доверие больше наименьшего");
    }
  });

  await check("принятый в штат появляется среди исполнителей", async () => {
    await page.getByRole("button", { name: "Принять в штат" }).click();
    await waitFor("исполнитель в штате", async () => {
      const d = await (await fetch(`${BASE}/api/v1/workers`)).json();
      return (d.items || []).some((v) => (v.worker ?? v).adapter_id === "crush");
    });
    const d = await (await fetch(`${BASE}/api/v1/workers`)).json();
    const w = (d.items || []).map((v) => v.worker ?? v).find((x) => x.adapter_id === "crush");
    if (w.trust_level !== "workspace_read") {
      throw new Error(`новичку выдано доверие «${w.trust_level}»`);
    }
    if (!w.version) throw new Error("версия не опрошена");
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
    console.log("\nE2E: продуктовая поверхность проходит сценарий целиком");
  });