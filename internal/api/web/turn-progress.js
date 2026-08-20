const STAGE_LABELS = {
  queued: "Готовлю ход",
  recall: "Вспоминаю похожий опыт",
  context: "Собираю контекст разговора",
  research: "Выбираю способ проверки",
  capability: "Проверяю состояние",
  provider_prompt: "Готовлю запрос к модели",
  provider_generation: "Формирую ответ",
  verification: "Проверяю ответ модели",
  finalization: "Сохраняю ответ",
};

function tokenWord(value) {
  const n = Math.abs(Math.trunc(value));
  const lastTwo = n % 100;
  if (lastTwo >= 11 && lastTwo <= 14) return "токенов";
  switch (n % 10) {
    case 1: return "токен";
    case 2:
    case 3:
    case 4: return "токена";
    default: return "токенов";
  }
}

export function formatTurnProgress(progress = {}) {
  const parts = [progress.label || STAGE_LABELS[progress.stage] || "Работаю над ответом"];
  const approximate = progress.approximate ? "≈" : "";
  if (Number.isFinite(progress.output_tokens) && progress.output_tokens > 0) {
    const count = Math.max(0, Math.round(progress.output_tokens));
    parts.push(`${approximate}${count} ${tokenWord(count)}`);
  }
  if (Number.isFinite(progress.generation_tokens_per_second) &&
      progress.generation_tokens_per_second > 0) {
    parts.push(`${approximate}${progress.generation_tokens_per_second.toFixed(1)} ток/с`);
  }
  if (Number.isFinite(progress.elapsed_ms) && progress.elapsed_ms >= 0) {
    parts.push(`${Math.floor(progress.elapsed_ms / 1000)} с`);
  }
  return parts.join(" · ");
}

export function progressFromTurn(turn, now = Date.now()) {
  const started = Date.parse(turn.started_at || turn.created_at || "");
  const elapsed = Number.isFinite(started) ? Math.max(0, now - started) : 0;
  return {
    turn_id: turn.id,
    conversation_id: turn.conversation_id,
    stage: turn.stage,
    label: turn.stage_label,
    elapsed_ms: elapsed,
  };
}

export function restoreTurnProgress(turn, snapshot = null, now = Date.now()) {
  const durable = progressFromTurn(turn, now);
  if (!snapshot) return durable;
  return {
    ...durable,
    ...snapshot,
    turn_id: durable.turn_id,
    conversation_id: durable.conversation_id,
    elapsed_ms: Math.max(durable.elapsed_ms, Number(snapshot.elapsed_ms) || 0),
  };
}

export function matchesTurn(progress, turn) {
  return Boolean(progress && turn &&
    progress.turn_id === turn.id &&
    progress.conversation_id === turn.conversation_id);
}
