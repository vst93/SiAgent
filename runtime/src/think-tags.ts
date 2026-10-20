// Some OpenAI-compatible models inline their reasoning in the answer text
// instead of returning a separate reasoning block, for example
// "<think>...</think>answer". The platform supports those models without
// leaking the trace into a chat reply: the tags are removed from the visible
// answer, and when the Agent shows a reasoning summary the removed text becomes
// that summary. Supported names are the ones seen in practice.
const names = ["thinking", "think", "thought", "reasoning", "reflection"];
const openTag = /^<\s*(think|thinking|thought|reasoning|reflection)\b[^>]*>/i;
const closeTags = new Map(
  names.map((name) => [name, new RegExp(`</\\s*${name}\\s*>`, "i")]),
);

// A trailing partial tag is held back while streaming, so a tag split across
// deltas never renders as text. Ordinary text such as "a < b" is not held back.
function partialTag(text: string): boolean {
  if (!text.startsWith("<") || text.includes(">")) return false;
  let rest = text.slice(1).replace(/^[ \t]+/, "").toLowerCase();
  if (rest.startsWith("/")) rest = rest.slice(1).replace(/^[ \t]+/, "");
  if (!rest) return true;
  return names.some(
    (name) => name.startsWith(rest) || rest.startsWith(name),
  );
}

export type SplitThinking = { visible: string; thought: string };

// splitInlineThinking is monotonic while streaming: an unclosed tag hides
// everything after it, and visible text only grows as more text arrives.
export function splitInlineThinking(text: string): SplitThinking {
  let visible = "";
  let thought = "";
  for (;;) {
    const index = text.indexOf("<");
    if (index < 0) {
      visible += text;
      break;
    }
    visible += text.slice(0, index);
    const rest = text.slice(index);
    const open = openTag.exec(rest);
    if (!open) {
      if (partialTag(rest)) break;
      visible += "<";
      text = rest.slice(1);
      continue;
    }
    const name = open[1].toLowerCase();
    const body = rest.slice(open[0].length);
    const close = closeTags.get(name)!.exec(body);
    if (!close) {
      thought += body;
      break;
    }
    thought += body.slice(0, close.index);
    text = body.slice(close.index + close[0].length);
  }
  return { visible, thought };
}

export function sanitizeAnswer(text: string): string {
  return splitInlineThinking(text).visible;
}
