import test from "node:test";
import assert from "node:assert/strict";

import {
  formatTurnProgress,
  matchesTurn,
  progressFromTurn,
} from "./turn-progress.js";

test("marks live generation telemetry approximate", () => {
  const text = formatTurnProgress({
    stage: "provider_generation",
    elapsed_ms: 17000,
    approximate: true,
    output_tokens: 84,
    generation_tokens_per_second: 13.1,
  });
  assert.equal(text, "Формирую ответ · ≈84 токена · ≈13.1 ток/с · 17 с");
});

test("uses runtime capability label and omits unsupported telemetry", () => {
  const text = formatTurnProgress({
    stage: "capability",
    label: "Проверяю текущую модель",
    elapsed_ms: 8200,
  });
  assert.equal(text, "Проверяю текущую модель · 8 с");
});

test("restores durable stage and elapsed time", () => {
  const progress = progressFromTurn({
    id: "trn_1",
    conversation_id: "conv_1",
    stage: "research",
    stage_label: "Выбираю способ проверки",
    started_at: "2026-08-21T10:00:00Z",
  }, Date.parse("2026-08-21T10:00:05Z"));
  assert.deepEqual(progress, {
    turn_id: "trn_1",
    conversation_id: "conv_1",
    stage: "research",
    label: "Выбираю способ проверки",
    elapsed_ms: 5000,
  });
});

test("correlates both conversation and turn", () => {
  const current = { id: "trn_1", conversation_id: "conv_1" };
  assert.equal(matchesTurn({ turn_id: "trn_1", conversation_id: "conv_1" }, current), true);
  assert.equal(matchesTurn({ turn_id: "trn_2", conversation_id: "conv_1" }, current), false);
  assert.equal(matchesTurn({ turn_id: "trn_1", conversation_id: "conv_2" }, current), false);
});
