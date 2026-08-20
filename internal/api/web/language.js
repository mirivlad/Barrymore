// Перевод внутренних терминов Barrymore на язык обычной поверхности.
//
// Thread остаётся доменной сущностью runtime и виден в техническом режиме.
// Но разговор и Стол не должны заставлять владельца знать, что такое thread,
// выбирать его или думать в терминах внутренней модели непрерывности.
//
// Старый app.js пока рисует несколько таких фраз напрямую. Этот маленький
// presentation-layer модуль намеренно меняет только системное приветствие и
// сгенерированные элементы Стола. Реплики владельца и модели не переписываются.

const chat = document.getElementById("chat");
const affairRoots = [
  document.getElementById("affairs-groups"),
  document.getElementById("affairs-detail"),
].filter(Boolean);

const exactAffairText = new Map([
  ["Разговор не удалось отнести к нити", "Не понял, к какому делу относится разговор"],
  ["нити", "контекст"],
  ["новая нить", "новое дело"],
  ["Завести нить", "Сохранить как дело"],
  ["Отнёс разговор к нити", "Понял, к какому делу это относится"],
  ["Не про эту нить", "Не про это"],
  ["нить заведена по вашему решению", "дело сохранено по вашему решению"],
]);

function replaceExactText(node, replacements) {
  const raw = node.nodeValue;
  if (!raw) return;
  const trimmed = raw.trim();
  if (!replacements.has(trimmed)) return;
  const replacement = replacements.get(trimmed);
  const at = raw.indexOf(trimmed);
  node.nodeValue = raw.slice(0, at) + replacement + raw.slice(at + trimmed.length);
}

function translateAffairs(root) {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes = [];
  while (walker.nextNode()) nodes.push(walker.currentNode);
  for (const node of nodes) replaceExactText(node, exactAffairText);

  for (const field of root.querySelectorAll("input[placeholder]")) {
    if (field.placeholder === "как назвать нить") {
      field.placeholder = "как назвать это дело";
    }
  }
}

function translateGreeting() {
  if (!chat) return;
  for (const bubble of chat.querySelectorAll(".bubble.barrymore")) {
    // Меняем только встроенное onboarding-приветствие. Любой настоящий ответ
    // модели, даже содержащий это слово, является содержанием разговора и не
    // должен проходить через словарь интерфейса.
    if (!bubble.textContent.startsWith("Здравствуйте. Я Бэрримор.")) continue;
    const walker = document.createTreeWalker(bubble, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode;
      if (node.nodeValue?.includes("Я помню ваши нити и разговоры")) {
        node.nodeValue = node.nodeValue.replace(
          "Я помню ваши нити и разговоры",
          "Я помню наши разговоры и дела",
        );
      }
    }
  }
}

if (chat) {
  translateGreeting();
  new MutationObserver(translateGreeting).observe(chat, { childList: true, subtree: true });
}

for (const root of affairRoots) {
  translateAffairs(root);
  new MutationObserver(() => translateAffairs(root)).observe(root, {
    childList: true,
    subtree: true,
    characterData: true,
  });
}
