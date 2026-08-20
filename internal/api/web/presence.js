// Короткий статус присутствия Бэрримора рядом с разговором.
//
// Это не технический мониторинг модели. Владелец должен понимать только одно:
// можно ли уже разговаривать, или постоянная локальная модель ещё поднимается.
// Полные endpoint/model/latency остаются в техническом режиме и настройках.

const talkTop = document.querySelector("#tab-talk .talk-top");
const historyButton = document.getElementById("talk-history");

if (talkTop && historyButton) {
  const presence = document.createElement("span");
  presence.id = "barrymore-presence";
  presence.className = "tag";
  presence.setAttribute("role", "status");
  presence.setAttribute("aria-live", "polite");
  presence.textContent = "просыпается…";
  talkTop.insertBefore(presence, historyButton);

  let loading = false;
  let timer = null;

  function show(text, tone = "") {
    presence.textContent = text;
    presence.className = `tag ${tone}`.trim();
    presence.title = text;
  }

  function modelDefinitelyBroken(model) {
    const reason = String(model?.reason || "").toLowerCase();
    return reason.includes("поднять нечем") ||
      reason.includes("не работает") ||
      reason.includes("не найден") ||
      reason.includes("недоступен");
  }

  async function refresh() {
    if (loading || document.hidden) return;
    loading = true;
    try {
      const response = await fetch("/api/v1/system/state", { cache: "no-store" });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const state = await response.json();
      const conversation = state.conversation || {};
      const model = state.local_model || {};

      if (conversation.status === "ready") {
        show("Бэрримор готов", "ok");
      } else if (model.loading) {
        show("локальная модель загружается…", "warn");
      } else if (model.configured && !model.serving) {
        if (modelDefinitelyBroken(model)) show("модель не отвечает", "bad");
        else show("локальная модель запускается…", "warn");
      } else if (conversation.status === "not_configured") {
        show("разговор не настроен", "warn");
      } else if (conversation.status === "unreachable" || conversation.status === "broken") {
        show("разговор недоступен", "bad");
      } else {
        show("просыпается…");
      }
    } catch {
      show("связь потеряна", "bad");
    } finally {
      loading = false;
    }
  }

  function schedule() {
    if (document.hidden) {
      if (timer) clearInterval(timer);
      timer = null;
      return;
    }
    refresh();
    if (!timer) timer = setInterval(refresh, 3000);
  }

  document.addEventListener("visibilitychange", schedule);
  schedule();
}
