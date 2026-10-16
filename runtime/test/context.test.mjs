import { test } from "node:test";
import assert from "node:assert/strict";
import {
  contextPolicy,
  selectContext,
  compactionSettings,
} from "../dist/context.js";

const user = (content, timestamp = 1) => ({ role: "user", content, timestamp });
const assistant = (content) => ({
  role: "assistant",
  content: [{ type: "text", text: content }],
  timestamp: 1,
});
test("recent context drops whole turns without separating parallel tool calls and results", () => {
  const system = {
    role: "system",
    content: "Keep the role and permissions.",
    timestamp: 1,
  };
  const call = {
    role: "assistant",
    content: [
      { type: "toolCall", id: "a", name: "lookup", arguments: {} },
      { type: "toolCall", id: "b", name: "lookup", arguments: {} },
    ],
    timestamp: 1,
  };
  const results = ["a", "b"].map((toolCallId) => ({
    role: "toolResult",
    toolCallId,
    toolName: "lookup",
    content: [{ type: "text", text: "found" }],
    timestamp: 1,
  }));
  const messages = [
    system,
    user("old"),
    assistant("old answer"),
    user("kept"),
    call,
    ...results,
    assistant("kept answer"),
    user("current"),
  ];
  const context = { messages };
  const selected = selectContext(
    context,
    contextPolicy({ strategy: "recent", max_turns: 2 }),
    100000,
  );
  assert.deepEqual(selected.messages, [system, ...messages.slice(3)]);
  assert.equal(context.messages, messages);
  const one = selectContext(
    context,
    contextPolicy({ strategy: "recent", max_turns: 1 }),
    100000,
  );
  assert.deepEqual(one.messages, [system, messages.at(-1)]);
});
test("time window expires old turns but always retains the current user turn", () => {
  const now = 10_000_000;
  const messages = [
    user("expired", now - 120_000),
    assistant("old"),
    user("recent", now - 30_000),
    assistant("new"),
    user("current", now),
  ];
  const selected = selectContext(
    { messages },
    contextPolicy({ strategy: "time", max_age_minutes: 1 }),
    100000,
    now,
  );
  assert.deepEqual(selected.messages, messages.slice(2));
  const expired = selectContext(
    { messages: [user("expired", 1), user("current", 2)] },
    contextPolicy({ strategy: "time", max_age_minutes: 1 }),
    100000,
    now,
  );
  assert.deepEqual(expired.messages, [user("current", 2)]);
});
test("token budgeting drops complete old turns, keeps tools and cannot truncate current input", () => {
  const latest = user("current question");
  const context = {
    messages: [user("old ".repeat(5000)), assistant("old answer"), latest],
  };
  const selected = selectContext(
    context,
    contextPolicy({ strategy: "recent" }),
    200,
  );
  assert.deepEqual(selected.messages, [latest]);
  const oversized = { messages: [user("current ".repeat(1000))] };
  assert.deepEqual(
    selectContext(oversized, contextPolicy({ strategy: "recent" }), 1).messages,
    oversized.messages,
  );
  assert.equal(
    selectContext(context, contextPolicy({ strategy: "full" }), 1),
    context,
  );
  assert.equal(
    selectContext(context, contextPolicy({ strategy: "summary" }), 1),
    context,
  );
});
test("summary thresholds reserve output and adapt recent text to small model windows", () => {
  const settings = compactionSettings(
    contextPolicy({ strategy: "summary", summary_threshold: 70 }),
    4096,
    1024,
  );
  assert(settings.enabled);
  assert.equal(4096 - settings.reserveTokens, Math.floor((4096 - 1024) * 0.7));
  assert(settings.keepRecentTokens < 4096 - settings.reserveTokens);
  for (const value of [
    { strategy: "bad" },
    { max_turns: 0 },
    { max_age_minutes: 0 },
    { summary_threshold: 99 },
    { keep_recent_tokens: 1 },
    null,
  ])
    assert.throws(() => contextPolicy(value), /invalid_context_policy/);
});

test("stale provider usage cannot discard recent turns after a prefix is removed", () => {
  const answer = {
    ...assistant("kept answer"),
    stopReason: "stop",
    usage: {
      input: 100000,
      output: 1000,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 101000,
    },
  };
  const messages = [
    user("old ".repeat(5000)),
    assistant("old answer"),
    user("kept question"),
    answer,
    user("current"),
  ];
  const selected = selectContext(
    { messages },
    contextPolicy({ strategy: "recent", max_turns: 2 }),
    500,
  );
  assert.deepEqual(selected.messages, messages.slice(2));
});
