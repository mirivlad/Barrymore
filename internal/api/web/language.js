// Перевод внутренних терминов Barrymore на язык обычной поверхности.
//
// Thread остаётся доменной сущностью runtime и виден в техническом режиме.
// Но разговор и Стол не должны заставлять владельца знать, что такое thread,
// выбирать его или думать в терминах внутренней модели непрерывности.
//
// Старый app.js пока рисует несколько таких фраз напрямую. Этот маленький
// presentation-layer модуль намеренно меняет только публичную поверхность:
// чат и Стол. Технические вкладки, журнал и API остаются без подмены терминов.

const publicRoots = [
  document.getElementById("chat"),
  document.getElementById("affairs-groups"),
  document.getElementById("affairs-detail"),
].filter(Boolean);

const exactText = new Map([
  ["Разговор не удалось отнести к нити", "Не понял, к какому делу относится разговор"],
  ["нити", "контекст"],
  ["новая нить", "новое дело"],
  ["Завести нить", "Сохранить как дело"],
  ["Отнёс разговор к нити", "Понял, к какому делу это относится"],
  ["Не про эту нить", "Не про это"],
  ["нить заведена по вашему решению", "дело сохранено по вашему решению"],
]);

function translateTextNode(node) {
  const raw = node.nodeValue;
  if (!raw) return;
  const trimmed = raw.trim();
  if (!trimmed) return;

  if (exactText.has(trimmed)) {
    const replacement = exactText.get(trimmed);
    const left = raw.slice(0, raw.indexOf(trimmed));
    const right = raw.slice(raw.indexOf(trimmed) + trimmed.length);
    node.nodeValue = left + replacement + right;
    return;
  }

  // Приветствие — единственная длинная строка, в которой внутренний термин
  // встроен в обычную фразу. Меняем только этот точный фрагмент, не трогая
  // слова владельца или ответы модели, где «нить» может быть предметом беседы.
  if (raw.includes("Я помню ваши нити и разговоры")) {
    node.nodeValue = raw.replace(
      "Я помню ваши нити и разговоры",
      "Я помню наши разговоры и дела",
    );
  }
}

function translateElement(root) {
  if (!root) return;

  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes = [];
  while (walker.nextNode()) nodes.push(walker.currentNode);
  for (const node of nodes) translateTextNode(node);

  for (const field of root.querySelectorAll("input[placeholder]")) {
    if (field.placeholder === "как назвать нить") {
      field.placeholder = "как назвать это дело";
    }
  }
}

for (const root of publicRoots) {
  translateElement(root);
  const observer = new MutationObserver(() => translateElement(root));
  observer.observe(root, { childList: true, subtree: true, characterData: true });
}
