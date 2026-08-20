// Поведение продуктовой поверхности, не относящееся к доменной модели.
//
// Стол может раскрыться сам как непосредственный ответ на только что сказанное
// владельцем. Но восстановление предыдущего хода после входа/reload не должно
// внезапно распахивать боковую панель: старые решения уже видны счётчиком.

// Старый интерфейс запоминал технический режим и последнюю внутреннюю вкладку.
// После перехода к conversation-first поверхности это становится ловушкой:
// человек обновляет страницу и снова попадает прямо в «Нити»/«Поручения»,
// хотя эти сущности теперь считаются внутренностями Barrymore.
//
// Миграция выполняется ровно один раз в браузерном профиле. Дальше выбор
// владельца снова уважается: включённый им позднее технический режим не
// сбрасывается при каждом входе.
const surfaceMigration = "barrymore.surface.conversation-first.v1";
if (localStorage.getItem(surfaceMigration) !== "done") {
  localStorage.removeItem("barrymore.tech");
  localStorage.setItem("barrymore.tab", "talk");
  localStorage.removeItem("barrymore.thread");
  localStorage.setItem(surfaceMigration, "done");
} else if (localStorage.getItem("barrymore.tech") !== "1") {
  // Даже после миграции техническая вкладка не должна восстанавливаться при
  // обычном режиме. Она могла остаться последней после того, как владелец
  // выключил tech-mode и закрыл вкладку до того, как app.js успел сохранить
  // возвращение в разговор.
  const savedTab = localStorage.getItem("barrymore.tab");
  const publicTabs = new Set(["talk", "settings"]);
  if (savedTab && !publicTabs.has(savedTab)) {
    localStorage.setItem("barrymore.tab", "talk");
  }
}

const desk = document.getElementById("affairs");
const toggle = document.getElementById("affairs-toggle");
const send = document.getElementById("talk-send");
const input = document.getElementById("talk-input");

if (desk && toggle) {
  let restoring = true;

  function keepClosed() {
    if (!restoring) return;
    // MutationObserver наблюдает ровно эти атрибуты. Повторно записывать уже
    // установленное значение нельзя: браузер всё равно создаст mutation record
    // и получится бесконечная микрозадачная петля после reload.
    if (!desk.hidden) desk.hidden = true;
    if (document.body.classList.contains("affairs-open")) {
      document.body.classList.remove("affairs-open");
    }
    if (toggle.getAttribute("aria-expanded") !== "false") {
      toggle.setAttribute("aria-expanded", "false");
    }
  }

  keepClosed();

  // loadTalk() асинхронен и может восстановить последнюю Proposal уже после
  // загрузки JS. Поэтому одного keepClosed() недостаточно: до первого
  // осознанного действия владельца не позволяем гидратации открыть Стол.
  const observer = new MutationObserver(keepClosed);
  observer.observe(desk, { attributes: true, attributeFilter: ["hidden"] });
  observer.observe(document.body, { attributes: true, attributeFilter: ["class"] });

  function release() {
    if (!restoring) return;
    restoring = false;
    observer.disconnect();
  }

  // Нажал сам на Стол — дальше его состояние полностью принадлежит app.js.
  // Capture нужен, чтобы снять gate до штатного click-handler toggle.
  toggle.addEventListener("click", release, { capture: true, once: true });

  // После новой реплики Бэрримор вправе раскрыть Стол, если его ответ требует
  // решения. Это уже не восстановление старого состояния, а текущий диалог.
  send?.addEventListener("click", release, { capture: true, once: true });
  input?.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) release();
  }, { capture: true });
}
