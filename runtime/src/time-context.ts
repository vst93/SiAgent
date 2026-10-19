import type { ImageContent, TextContent, UserMessage } from "@earendil-works/pi-ai";

export type TurnTime = { received_at: string; timezone: string };
export type TurnAttachment =
  | { kind: "image"; name: string; mime: string; data: string }
  | { kind: "file"; name: string; mime: string; text: string };

// Persist the time with its user turn. Only newly appended input changes; the
// stable system instructions and previous turns remain eligible for prefix caching.
export function withTurnTime(
  input: string,
  time?: TurnTime,
  attachments: TurnAttachment[] = [],
): UserMessage["content"] {
  if (time === undefined && attachments.length === 0) return input;
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
  let local = "";
  if (time !== undefined) {
    try {
      local = new Intl.DateTimeFormat("zh-CN", {
        timeZone: time.timezone,
        year: "numeric", month: "2-digit", day: "2-digit",
        weekday: "long", hour: "2-digit", minute: "2-digit", second: "2-digit",
        hourCycle: "h23",
      }).format(new Date(time.received_at));
    } catch { throw new Error("invalid_time_context"); }
  }
  const blocks: Array<TextContent | ImageContent> = [];
  if (time !== undefined) {
    blocks.push({
      type: "text",
      text: `[平台时间上下文]\n本轮消息接收时间：${time.received_at}\n本地时间：${local}\n时区：${time.timezone}\n“今天”“明天”等相对日期按本轮本地时间解释。这是消息接收时的时间基准，不是持续刷新的实时时钟。\n[/平台时间上下文]`,
    });
  }
  const fileText = attachments
    .filter((item): item is Extract<TurnAttachment, { kind: "file" }> => item.kind === "file")
    .map((item) => `\n\n[附件：${item.name}]\n${item.text}`)
    .join("");
  blocks.push({ type: "text", text: input + fileText });
  for (const item of attachments) {
    if (item.kind === "image") {
      blocks.push({ type: "image", data: item.data, mimeType: item.mime });
    }
  }
  return blocks;
}
