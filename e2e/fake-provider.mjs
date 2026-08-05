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
  open_questions: ["что именно означает «завис»?"],
};

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
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({
        model: "e2e",
        choices: [{ message: { role: "assistant", content: JSON.stringify(reply) } }],
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
