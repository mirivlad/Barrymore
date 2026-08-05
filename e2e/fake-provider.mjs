// Поддельный провайдер модели для E2E-проверки интерфейса.
//
// Подделывается ровно один слой — тот, который в проверке интерфейса не
// участвует и стоит минуты машинного времени. Всё остальное настоящее:
// тот же двоичный файл, та же база, тот же браузер.
//
// Ответ намеренно отдаётся сразу и по контракту: E2E проверяет поведение
// экрана, а не способность локальной модели попасть в схему.
import http from "node:http";

const reply = {
  reply: "Похоже, дело в незакрытом worktree. Предлагаю посмотреть, что там.",
  thread_match: {
    thread_id: "",
    new_thread_title: "Rollboard: завис в worktree",
    new_thread_kind: "problem",
    why: "речь о конкретной длящейся проблеме с конкретным репозиторием",
  },
  thread_state: {
    goal: "разобраться, почему Rollboard не отпускает worktree",
    situation: "владелец не может продолжить работу: каталог занят",
    next_step: "выяснить, какой процесс держит каталог",
    obstacles: ["неизвестно, чем именно занят каталог"],
    waiting: [],
  },
  memory_candidates: [],
  work_order_proposals: [
    {
      title: "Аудит Rollboard",
      goal: "выяснить состояние worktree и назвать причину",
      why: "владелец не может продолжить работу",
      workspace_hint: process.env.E2E_WORKSPACE || "",
      acceptance_criteria: ["названо, чем занят каталог"],
      needs_write: false,
    },
  ],
  own_actions: [],
  open_questions: ["что именно означает «завис»?"],
};

// Ответ, в котором Бэрримор берётся посмотреть сам.
//
// Умение берётся из того же списка, который runtime показал в промпте, —
// иначе оно было бы отвергнуто, и это тоже часть проверяемого контракта.
const ownReply = {
  reply: "Это я посмотрю сам, звать никого не нужно.",
  thread_match: { thread_id: "", new_thread_title: "", new_thread_kind: "", why: "" },
  thread_state: { goal: "", situation: "", next_step: "", obstacles: [], waiting: [] },
  memory_candidates: [],
  own_actions: [
    {
      skill_id: "git.worktree.diagnose",
      target: process.env.E2E_WORKSPACE || "",
      why: "вопрос ровно о том, что я вижу своими средствами",
    },
  ],
  work_order_proposals: [],
  open_questions: [],
};

// Идентификатор нити берётся из того же списка, который Бэрримор показал
// в промпте. Так и работает настоящая модель, и так проверяется вторая ветка
// сопоставления: не «предложить новую», а «узнать уже существующую».
// Предложение способа запуска для незнакомого инструмента.
//
// Флаги берутся из справки, которую Бэрримор показал в этом же запросе:
// придуманный флаг runtime отвергнет, и это отдельная проверка.
const harnessDraft = {
  display_name: "Crush",
  version_args: ["--version"],
  run_args: ["run", "--quiet", "{prompt}"],
  prompt_via: "argv",
  audit_args: ["--read-only"],
  model_flag: "--model",
  auth_paths: [],
  capabilities: ["repository-audit"],
  why: "в справке описан неинтерактивный запуск подкомандой run",
  evidence: ["crush run [флаги] <задание>", "--read-only ничего не менять на диске"],
};

function replyFor(prompt) {
  // Подключение незнакомого инструмента идёт другим контрактом.
  if (/справку незнакомой программы/.test(prompt || "")) return harnessDraft;
  // Реплика «посмотри сам» проверяет вторую ветку выбора способа: не звать
  // исполнителя, когда на вопрос отвечает собственное умение.
  if (/посмотри сам/i.test(prompt || "")) {
    const offered = /git\.worktree\.diagnose/.test(prompt || "");
    return offered
      ? ownReply
      : { ...ownReply, reply: "Умения мне не показали.", own_actions: [] };
  }
  const known = /\bthr_[a-z0-9]+/.exec(prompt || "");
  if (!known) return reply;
  return {
    ...reply,
    reply: "Это та же история с Rollboard, продолжаем.",
    thread_match: {
      thread_id: known[0],
      new_thread_title: "",
      new_thread_kind: "",
      why: "разговор о той же нити Rollboard",
    },
  };
}

const server = http.createServer((req, res) => {
  if (req.url.startsWith("/v1/models")) {
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ data: [{ id: "e2e" }] }));
    return;
  }
  if (req.url.startsWith("/v1/chat/completions")) {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      let prompt = "";
      try {
        prompt = (JSON.parse(body).messages || [])
          .map((m) => m.content).join("\n");
      } catch {
        // Неразбираемый запрос — не повод молчать: отдаём общий ответ.
      }
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({
        model: "e2e",
        choices: [{ message: { role: "assistant", content: JSON.stringify(replyFor(prompt)) } }],
        usage: { prompt_tokens: 10, completion_tokens: 20 },
      }));
    });
    return;
  }
  res.writeHead(404);
  res.end();
});

const port = Number(process.env.E2E_PROVIDER_PORT || 18099);
server.listen(port, "127.0.0.1", () => {
  process.stdout.write(`провайдер слушает ${port}\n`);
});
