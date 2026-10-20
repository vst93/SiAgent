import type { Message, TranscriptContext } from "@earendil-works/pi-ai";
import { estimateMessageTokens } from "@earendil-works/pi-ai/utils/estimate";

export type ContextPolicy = {
  strategy: "full" | "recent" | "time" | "summary";
  max_turns: number;
  max_age_minutes: number;
  summary_threshold: number;
  keep_recent_tokens: number;
};
export type OutputPolicy = {
  thinking: boolean;
  tools: "hidden" | "summary" | "detailed";
  sources: boolean;
  progress: boolean;
  /** "strip" removes inline <think>-style reasoning from the answer; "keep" leaves it. */
  think_tags?: "strip" | "keep";
  thinking_max_chars?: number;
  acknowledge?: boolean;
  stream?: boolean;
};
export function contextPolicy(value?: Partial<ContextPolicy>): ContextPolicy {
  if (
    value !== undefined &&
    (value === null || typeof value !== "object" || Array.isArray(value))
  )
    throw new Error("invalid_context_policy");
  const p: ContextPolicy = {
    strategy: "full",
    max_turns: 20,
    max_age_minutes: 1440,
    summary_threshold: 70,
    keep_recent_tokens: 4096,
    ...value,
  };
  if (
    value === null ||
    !["full", "recent", "time", "summary"].includes(p.strategy) ||
    !Number.isInteger(p.max_turns) ||
    p.max_turns < 1 ||
    p.max_turns > 200 ||
    !Number.isInteger(p.max_age_minutes) ||
    p.max_age_minutes < 1 ||
    p.max_age_minutes > 43200 ||
    !Number.isInteger(p.summary_threshold) ||
    p.summary_threshold < 50 ||
    p.summary_threshold > 90 ||
    !Number.isInteger(p.keep_recent_tokens) ||
    p.keep_recent_tokens < 128 ||
    p.keep_recent_tokens > 32768
  )
    throw new Error("invalid_context_policy");
  return p;
}
// Select whole user turns, including every assistant/tool exchange they own.
// System configuration remains available; persisted history is never edited.
// Cached provider usage describes the old prefix and is invalid after trimming.
export const estimateSelectedTokens = (context: TranscriptContext) =>
  context.messages.reduce(
    (total, message) => total + estimateMessageTokens(message) + 8,
    0,
  );
export function selectContext(
  context: TranscriptContext,
  policy: ContextPolicy,
  inputBudget: number,
  now = Date.now(),
): TranscriptContext {
  if (policy.strategy !== "recent" && policy.strategy !== "time")
    return context;
  const turns = context.messages.flatMap((m, index) =>
    m.role === "user" ? [index] : [],
  );
  if (turns.length === 0) return context;
  let first = Math.max(0, turns.length - policy.max_turns);
  if (policy.strategy === "time") {
    const cutoff = now - policy.max_age_minutes * 60000;
    while (
      first < turns.length - 1 &&
      context.messages[turns[first]].timestamp < cutoff
    )
      first++;
  }
  const selected = (): Message[] =>
    context.messages.filter((m, i) => m.role === "system" || i >= turns[first]);
  let messages = selected();
  while (
    first < turns.length - 1 &&
    estimateSelectedTokens({ ...context, messages }) > inputBudget
  ) {
    first++;
    messages = selected();
  }
  return { ...context, messages };
}
export function compactionSettings(
  policy: ContextPolicy,
  window: number,
  output: number,
) {
  const input = window - output;
  const threshold = Math.floor((input * policy.summary_threshold) / 100);
  return {
    enabled: policy.strategy === "summary",
    reserveTokens: window - threshold,
    keepRecentTokens: Math.max(
      32,
      Math.min(policy.keep_recent_tokens, Math.floor(threshold / 2)),
    ),
    backgroundTokens: 0,
  };
}
