// Явная оценка ответа — сильный, но необязательный сигнал владельца.
//
// Модуль не меняет монолитный app.js и не хранит собственную доменную правду.
// Он наблюдает уже существующий GET истории разговора, получает из него
// message.episode_id + текущий feedback и добавляет две тихие кнопки к тем же
// DOM-пузырям. Backend остаётся единственным источником истины.

const nativeFetch = window.fetch.bind(window);
let latestMessages = [];
let decorateQueued = false;

function injectStyles() {
  if (document.getElementById("barrymore-feedback-style")) return;
  const style = document.createElement("style");
  style.id = "barrymore-feedback-style";
  style.textContent = `
    .feedback-tools {
      display: flex; align-items: center; gap: 2px; margin-top: 5px;
      min-height: 24px; opacity: .42; transition: opacity .12s ease;
    }
    .bubble.barrymore:hover .feedback-tools,
    .feedback-tools:focus-within { opacity: 1; }
    .feedback-button {
      appearance: none; border: 0; background: transparent; color: var(--muted);
      padding: 2px 5px; border-radius: 5px; cursor: pointer; font: inherit;
      font-size: 14px; line-height: 1.2;
    }
    .feedback-button:hover { color: var(--ink); background: var(--bg); }
    .feedback-button[aria-pressed="true"] { color: var(--accent); opacity: 1; }
    .feedback-button:disabled { cursor: default; opacity: .55; }
    .feedback-error { color: var(--bad); font-size: 11px; margin-left: 4px; }
    @media (prefers-reduced-motion: reduce) { .feedback-tools { transition: none; } }
  `;
  document.head.appendChild(style);
}

function scheduleDecorate() {
  if (decorateQueued) return;
  decorateQueued = true;
  requestAnimationFrame(() => {
    decorateQueued = false;
    decorate();
  });
}

function setCurrent(box, value) {
  box.dataset.feedbackCurrent = value || "";
  for (const button of box.querySelectorAll(".feedback-button")) {
    button.setAttribute("aria-pressed", String(button.dataset.feedbackValue === value));
  }
}

async function recordFeedback(box, value) {
  const episodeID = box.dataset.episodeId;
  if (!episodeID) return;
  const buttons = [...box.querySelectorAll(".feedback-button")];
  buttons.forEach((b) => { b.disabled = true; });
  box.querySelector(".feedback-error")?.remove();

  try {
    const res = await nativeFetch(`/api/v1/episodes/${encodeURIComponent(episodeID)}/feedback`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ value, note: "" }),
    });
    const text = await res.text();
    let body = null;
    try { body = text ? JSON.parse(text) : null; } catch { body = null; }
    if (!res.ok) {
      throw new Error(body?.detail || body?.title || res.statusText || "оценка не записана");
    }
    const current = body?.current?.value || value;
    setCurrent(box, current);
    const messageID = box.closest(".bubble")?.dataset?.messageId;
    if (messageID) {
      const m = latestMessages.find((item) => item.id === messageID);
      if (m) m.feedback = current;
    }
  } catch (err) {
    const note = document.createElement("span");
    note.className = "feedback-error";
    note.textContent = "не записано";
    note.title = String(err?.message || err);
    box.appendChild(note);
  } finally {
    buttons.forEach((b) => { b.disabled = false; });
  }
}

function makeTools(message) {
  const box = document.createElement("div");
  box.className = "feedback-tools";
  box.dataset.episodeId = message.episode_id;

  const choices = [
    ["like", "👍", "Отличный ответ"],
    ["dislike", "👎", "Плохой ответ"],
  ];
  for (const [value, glyph, label] of choices) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "feedback-button";
    button.dataset.feedbackValue = value;
    button.textContent = glyph;
    button.title = label;
    button.setAttribute("aria-label", label);
    button.setAttribute("aria-pressed", "false");
    button.addEventListener("click", () => recordFeedback(box, value));
    box.appendChild(button);
  }
  setCurrent(box, message.feedback || "");
  return box;
}

function decorate() {
  injectStyles();
  const messages = latestMessages.filter((m) => m.role === "barrymore");
  const bubbles = [...document.querySelectorAll("#chat .bubble.barrymore")];
  const count = Math.min(messages.length, bubbles.length);
  if (!count) return;

  // Совмещаем с конца: если API ограничит историю или на экране есть временный
  // служебный пузырь, новые реальные ответы всё равно получат свой Episode.
  for (let i = 0; i < count; i++) {
    const message = messages[messages.length - count + i];
    const bubble = bubbles[bubbles.length - count + i];
    bubble.dataset.messageId = message.id || "";

    const existing = bubble.querySelector(":scope > .feedback-tools");
    if (!message.episode_id) {
      existing?.remove();
      continue;
    }
    if (existing && existing.dataset.episodeId === message.episode_id) {
      setCurrent(existing, message.feedback || "");
      continue;
    }
    existing?.remove();
    bubble.appendChild(makeTools(message));
  }
}

// app.js получает историю через fetch и затем рендерит её. Мы только читаем
// копию того же ответа. Response, который получает app.js, не меняется.
window.fetch = async (...args) => {
  const response = await nativeFetch(...args);
  try {
    const input = args[0];
    const options = args[1] || {};
    const rawURL = typeof input === "string" ? input : input?.url || "";
    const method = String(options.method || (typeof input !== "string" ? input?.method : "") || "GET").toUpperCase();
    const url = new URL(rawURL, window.location.origin);
    if (method === "GET" && /^\/api\/v1\/conversations\/[^/]+\/messages$/.test(url.pathname) && response.ok) {
      response.clone().json().then((body) => {
        latestMessages = Array.isArray(body?.items) ? body.items : [];
        scheduleDecorate();
      }).catch(() => {});
    }
  } catch {
    // Оценка — необязательная поверхность. Ошибка наблюдения сети не должна
    // ломать сам разговор.
  }
  return response;
};

const observer = new MutationObserver(() => scheduleDecorate());
const chat = document.getElementById("chat");
if (chat) observer.observe(chat, { childList: true, subtree: false });
injectStyles();
