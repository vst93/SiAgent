import { exact, terms } from "./i18n-dict";

export type Language = "zh" | "en";

const storageKey = "siagent.language";

export function storedLanguage(): Language {
  try {
    return localStorage.getItem(storageKey) === "en" ? "en" : "zh";
  } catch {
    return "zh";
  }
}

export function rememberLanguage(language: Language) {
  try {
    localStorage.setItem(storageKey, language);
  } catch {
    // Language still applies for the current page.
  }
}

export function translate(value: string): string {
  const lead = value.match(/^\s*/)?.[0] ?? "";
  const trail = value.match(/\s*$/)?.[0] ?? "";
  const core = value.trim().replace(/\s+/g, " ");
  if (!core) return value;
  const direct = exact[core];
  if (direct !== undefined) return lead + direct + trail;
  let text = core;
  for (const [source, target] of terms) {
    if (text.includes(source)) text = text.split(source).join(` ${target} `);
  }
  if (text === core) return value;
  return lead + polish(text) + trail;
}

function polish(value: string): string {
  return value
    .replace(/，/g, ", ")
    .replace(/。/g, ". ")
    .replace(/：/g, ": ")
    .replace(/；/g, "; ")
    .replace(/！/g, "! ")
    .replace(/？/g, "? ")
    .replace(/（/g, " (")
    .replace(/）/g, ") ")
    .replace(/、/g, ", ")
    .replace(/[“”]/g, '"')
    .replace(/[‘’]/g, "'")
    .replace(/\s+([,.;:!?)])/g, "$1")
    .replace(/\(\s+/g, "(")
    .replace(/\s{2,}/g, " ")
    .trim();
}

const attributeNames = ["placeholder", "title", "aria-label", "alt"] as const;
const skipSelector =
  "script,style,noscript,textarea,[data-i18n-skip],.message-text,.markdown,.conversation,.tree-label,.agent-card-select strong,.kb-document-title,.kb-source-title";

let language: Language = "zh";
let observer: MutationObserver | null = null;
let applying = false;
const textOrigins = new WeakMap<Text, string>();
const attributeOrigins = new WeakMap<Element, Map<string, string>>();
const trackedText = new Set<Text>();
const trackedAttributes = new Set<Element>();

function skipped(element: Element | null): boolean {
  return !element || element.closest(skipSelector) !== null;
}

function localizeText(node: Text) {
  if (language !== "en" || !node.isConnected || skipped(node.parentElement)) return;
  const current = node.data;
  let origin = textOrigins.get(node);
  if (origin === undefined || (current !== translate(origin) && current !== origin)) {
    origin = current;
    textOrigins.set(node, origin);
    trackedText.add(node);
  }
  const next = translate(origin);
  if (next !== current) {
    applying = true;
    node.data = next;
    applying = false;
  }
}

function localizeAttribute(element: Element, name: string) {
  if (language !== "en" || !element.isConnected || skipped(element)) return;
  const current = element.getAttribute(name);
  if (current === null) return;
  let origins = attributeOrigins.get(element);
  if (!origins) {
    origins = new Map();
    attributeOrigins.set(element, origins);
    trackedAttributes.add(element);
  }
  let origin = origins.get(name);
  if (origin === undefined || (current !== translate(origin) && current !== origin)) {
    origin = current;
    origins.set(name, origin);
  }
  const next = translate(origin);
  if (next !== current) {
    applying = true;
    element.setAttribute(name, next);
    applying = false;
  }
}

function localizeElement(element: Element) {
  for (const name of attributeNames) {
    if (element.hasAttribute(name)) localizeAttribute(element, name);
  }
}

function localizeTree(root: Node) {
  if (root.nodeType === Node.TEXT_NODE) {
    localizeText(root as Text);
    return;
  }
  if (!(root instanceof Element) && !(root instanceof DocumentFragment)) return;
  if (root instanceof Element) localizeElement(root);
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
  let node: Node | null = walker.nextNode();
  while (node) {
    if (node.nodeType === Node.TEXT_NODE) localizeText(node as Text);
    else if (node instanceof Element) localizeElement(node);
    node = walker.nextNode();
  }
}

function restore() {
  applying = true;
  for (const node of trackedText) {
    if (!node.isConnected) continue;
    const origin = textOrigins.get(node);
    if (origin !== undefined) node.data = origin;
  }
  for (const element of trackedAttributes) {
    if (!element.isConnected) continue;
    const origins = attributeOrigins.get(element);
    if (!origins) continue;
    for (const [name, origin] of origins) element.setAttribute(name, origin);
  }
  applying = false;
  trackedText.clear();
  trackedAttributes.clear();
}

function handleMutations(mutations: MutationRecord[]) {
  if (applying) return;
  for (const mutation of mutations) {
    if (mutation.type === "characterData") {
      localizeText(mutation.target as Text);
    } else if (mutation.type === "attributes") {
      if (mutation.attributeName) localizeAttribute(mutation.target as Element, mutation.attributeName);
    } else {
      mutation.addedNodes.forEach((node) => localizeTree(node));
    }
  }
}

export function applyLanguage(next: Language): () => void {
  language = next;
  document.documentElement.lang = next === "zh" ? "zh-CN" : "en";
  observer?.disconnect();
  observer = null;
  restore();
  if (next === "zh" || !document.body) return () => {};
  localizeTree(document.body);
  observer = new MutationObserver(handleMutations);
  observer.observe(document.body, {
    subtree: true,
    childList: true,
    characterData: true,
    attributes: true,
    attributeFilter: [...attributeNames],
  });
  return () => {
    observer?.disconnect();
    observer = null;
    restore();
  };
}
