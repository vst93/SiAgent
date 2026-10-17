import type { UserMessage } from "@earendil-works/pi-ai";

export type TurnTime = { received_at: string; timezone: string };

// Persist the time with its user turn. Only newly appended input changes; the
// stable system instructions and previous turns remain eligible for prefix caching.
export function withTurnTime(input: string, time?: TurnTime): UserMessage["content"] {
  if (time === undefined) return input;
  if (
    !time ||
    typeof time.received_at !== "string" ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(time.received_at) ||
    !Number.isFinite(Date.parse(time.received_at)) ||
    typeof time.timezone !== "string" ||
    !time.timezone ||
    time.timezone.length > 128 ||
    time.timezone === "Local"
  ) throw new Error("invalid_time_context");
  let local: string;
  try {
    local = new Intl.DateTimeFormat("zh-CN", {
      timeZone: time.timezone,
      year: "numeric", month: "2-digit", day: "2-digit",
      weekday: "long", hour: "2-digit", minute: "2-digit", second: "2-digit",
      hourCycle: "h23",
    }).format(new Date(time.received_at));
  } catch { throw new Error("invalid_time_context"); }
  return [
    {
      type: "text",
      text: `[平台时间上下文]\n本轮消息接收时间：${time.received_at}\n本地时间：${local}\n时区：${time.timezone}\n“今天”“明天”等相对日期按本轮本地时间解释。这是消息接收时的时间基准，不是持续刷新的实时时钟。\n[/平台时间上下文]`,
    },
    { type: "text", text: input },
  ];
}
