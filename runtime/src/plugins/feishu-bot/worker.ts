import * as lark from "@larksuiteoapi/node-sdk";
import { randomBytes } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { Readable } from "node:stream";
import { pathToFileURL } from "node:url";
import { serve, type Session } from "../frames.js";

// The Feishu (Lark) worker is a thin wrapper over the official Node SDK's
// long-connection mode: `WSClient` performs endpoint discovery, authentication,
// fragmentation and reconnect, and `Client` performs typed API calls with a
// cached tenant access token. Everything durable stays in Go: this worker only
// translates events into protocol frames and performs one SDK request per
// platform delivery.

const maxInboundImages = 8;
const maxInboundImageBytes = 16 * 1024 * 1024;
const maxOutstanding = 128;
const maxTextBytes = 20000;
// One question stays one Feishu message: the platform sends acknowledgement,
// drafts and the final answer as separate frames, and each one updates the same
// interactive card. The SDK and Axios log through the vendor logger, which would
// write credentials and payloads to stderr, so it is silenced.
const quiet = {
  debug() {},
  info() {},
  warn() {},
  error() {},
  trace() {},
} as lark.Logger;
const appIDPattern = /^cli_[0-9a-fA-F]{16}$/;

interface Configured {
  appId: string;
  appSecret: string;
  domain: string;
  mediaDir: string;
}

export function runWorker(connectionHost?: string) {
  let client: lark.Client | undefined;
  let wsClient: lark.WSClient | undefined;
  let configured: Configured | undefined;
  let connected = false;
  // The bot's own open_id lets group messages be answered only when the bot was
  // mentioned. It is resolved once after the first handshake; when the lookup
  // fails, group messages are accepted as delivered by the subscription scope.
  let botOpenId = "";
  const outstanding = new Set<string>();
  // Cards this worker created, keyed by the platform's inbox/message part. The
  // platform retries a delivery with the same identity, so a retry after an
  // uncertain outcome must never post a second card.
  const cards = new Map<string, string>();
  serve((frame, session) => {
    if (frame.type === "configure" && !wsClient) {
      const values = frame.config?.config;
      const secrets = frame.config?.secrets;
      if (
        typeof values?.app_id !== "string" ||
        typeof secrets?.app_secret !== "string"
      ) {
        session.close();
        return;
      }
      configured = {
        appId: values.app_id,
        appSecret: secrets.app_secret,
        domain: typeof values?.domain === "string" ? values.domain.trim() : "",
        mediaDir:
          typeof frame.config?.media_dir === "string"
            ? frame.config.media_dir
            : "",
      };
      if (!appIDPattern.test(configured.appId)) {
        session.state("failed");
        session.close();
        return;
      }
      const shared = {
        appId: configured.appId,
        appSecret: configured.appSecret,
        logger: quiet,
        loggerLevel: lark.LoggerLevel.error,
        ...(configured.domain ? { domain: configured.domain } : {}),
      };
      client = new lark.Client(shared);
      wsClient = new lark.WSClient({
        ...shared,
        handshakeTimeoutMs: 10000,
        ...(connectionHost ? { connectionHost } : {}),
        onReady: () => {
          connected = true;
          session.state("connected");
          void resolveBotIdentity();
        },
        onReconnecting: () => {
          connected = false;
          session.state("reconnecting");
        },
        onReconnected: () => {
          connected = true;
          session.state("connected");
        },
        onError: () => {
          connected = false;
          session.state("failed");
        },
      });
      const dispatcher = new lark.EventDispatcher({
        logger: quiet,
        loggerLevel: lark.LoggerLevel.error,
      }).register({
        "im.message.receive_v1": (data) => {
          void inbound(data, session);
          // The gateway only needs to know the frame was handled; Go already
          // owns durable admission, so nothing is returned here.
          return undefined;
        },
      });
      session.onClose(() => wsClient?.close({ force: true }));
      session.state("connecting");
      void wsClient
        .start({ eventDispatcher: dispatcher })
        .then(() => {
          // start() resolves once the endpoint credentials were pulled; the
          // handshake itself is reported through the onReady callback.
          if (connected) session.state("connected");
        })
        .catch(() => {
          connected = false;
          session.state("failed");
        });
      return;
    }
    if (frame.type === "receipt") {
      outstanding.delete(frame.id);
      if (!frame.accepted) {
        connected = false;
        wsClient?.close({ force: true });
        session.state("failed");
      }
      return;
    }
    if (frame.type === "send") {
      void send(frame.id, frame.delivery, session);
      return;
    }
    session.close();
  });

  async function resolveBotIdentity(): Promise<void> {
    const clientValue = client;
    if (!clientValue || botOpenId) return;
    try {
      const info: any = await clientValue.request({
        url: "/open-apis/bot/v3/info",
        method: "GET",
      });
      const openId = info?.bot?.open_id;
      if (typeof openId === "string" && openId) botOpenId = openId;
    } catch {
      // Group messages still reach the agent; the subscription scope decides
      // which ones the vendor delivers.
    }
  }

  // inbound normalizes one Feishu event into a platform frame. Text and images
  // are supported; pictures are downloaded here because only this worker holds
  // the tenant credential, and they are staged as files so image bytes never
  // travel inside a protocol frame.
  async function inbound(data: any, session: Session): Promise<void> {
    const values = configured;
    const clientValue = client;
    if (!values || !clientValue) return;
    const message = data?.message;
    const sender = data?.sender?.sender_id;
    const messageId = message?.message_id;
    const senderId = sender?.open_id ?? sender?.user_id ?? sender?.union_id;
    const chatId = message?.chat_id;
    const chatType = message?.chat_type;
    if (
      typeof messageId !== "string" ||
      !messageId ||
      messageId.length > 256 ||
      typeof senderId !== "string" ||
      !senderId ||
      senderId.length > 256 ||
      typeof chatId !== "string" ||
      !chatId ||
      chatId.length > 256 ||
      typeof chatType !== "string"
    )
      return;
    // `group` and `topic` are shared chats; `p2p` is private. An unknown value is
    // ignored instead of being guessed into a conversation scope.
    const kind =
      chatType === "group" || chatType === "topic"
        ? "group"
        : chatType === "p2p"
          ? "single"
          : "";
    if (!kind) return;
    // Bot-authored messages are not questions; reacting to them would loop.
    if (data?.sender?.sender_type && data.sender.sender_type !== "user") return;
    // In shared chats the bot answers only messages that mention it, so it never
    // replies to every message in a group it was added to.
    if (kind === "group" && botOpenId) {
      const mentions = Array.isArray(message?.mentions) ? message.mentions : [];
      const mentioned = mentions.some(
        (mention: any) => mention?.id?.open_id === botOpenId,
      );
      if (!mentioned) return;
    }
    if (outstanding.size >= maxOutstanding) {
      connected = false;
      wsClient?.close({ force: true });
      session.state("failed");
      return;
    }
    outstanding.add(messageId);
    const { text, imageKeys, kind: vendorKind } = describeInbound(message);
    const replyContext = {
      message_id: messageId,
      chat_id: chatId,
      received_at: String(Date.now()),
    };
    try {
      const images = await stageImages(
        clientValue,
        messageId,
        imageKeys.slice(0, maxInboundImages),
      );
      session.output({
        type: "incoming",
        incoming: {
          id: messageId,
          chat_type: kind,
          sender_id: senderId,
          group_id: kind === "group" ? chatId : "",
          session: JSON.stringify([
            kind,
            kind === "group" ? chatId : "",
            senderId,
          ]),
          kind: images.length > 0 ? "image" : vendorKind,
          text,
          images,
          reply_context: replyContext,
        },
      });
    } catch {
      // A failed download must not lose the message: report the text it carried
      // so the platform still answers, or records the empty-content notice.
      session.output({
        type: "incoming",
        incoming: {
          id: messageId,
          chat_type: kind,
          sender_id: senderId,
          group_id: kind === "group" ? chatId : "",
          session: JSON.stringify([
            kind,
            kind === "group" ? chatId : "",
            senderId,
          ]),
          kind: "text",
          text,
          reply_context: replyContext,
        },
      });
    }
  }

  // describeInbound flattens the text of text, image and rich-text messages and
  // lists the image keys they carry.
  function describeInbound(message: any): {
    text: string;
    imageKeys: string[];
    kind: string;
  } {
    const content = parseContent(message?.content);
    const imageKeys: string[] = [];
    const type = typeof message?.message_type === "string" ? message.message_type : "text";
    switch (type) {
      case "text":
        return {
          text: stripMentions(
            typeof content?.text === "string" ? content.text : "",
            message?.mentions,
          ),
          imageKeys,
          kind: "text",
        };
      case "image":
        if (typeof content?.image_key === "string" && content.image_key)
          imageKeys.push(content.image_key);
        return { text: "", imageKeys, kind: "image" };
      case "post": {
        // Rich text is a locale map of one title plus paragraphs of tagged
        // elements; the first inline picture is downloaded, the rest ignored.
        const parts: string[] = [];
        const posts = Array.isArray(content)
          ? content
          : Object.values(content ?? {});
        for (const post of posts as any[]) {
          if (typeof post?.title === "string" && post.title) parts.push(post.title);
          for (const paragraph of post?.content ?? []) {
            const line: string[] = [];
            for (const element of paragraph ?? []) {
              if (
                (element?.tag === "text" || element?.tag === "a") &&
                typeof element.text === "string"
              )
                line.push(element.text);
              else if (
                element?.tag === "img" &&
                typeof element.image_key === "string" &&
                element.image_key
              )
                imageKeys.push(element.image_key);
            }
            parts.push(line.join(""));
          }
        }
        return { text: parts.filter(Boolean).join("\n"), imageKeys, kind: "text" };
      }
      case "audio":
        // Voice messages carry an optional transcript, which is the useful part.
        return {
          text: typeof content?.text === "string" ? content.text : "",
          imageKeys,
          kind: "text",
        };
      default:
        // Unsupported types keep their vendor name so the platform records an
        // explicit scope notice rather than an empty question.
        return { text: "", imageKeys, kind: type };
    }
  }

  // stripMentions removes the "@_user_N" placeholders Feishu substitutes for
  // mentions, so the model receives the question rather than transport tokens.
  function stripMentions(text: string, mentions: unknown): string {
    if (!Array.isArray(mentions)) return text.trim();
    let value = text;
    for (const mention of mentions) {
      const key = (mention as { key?: unknown })?.key;
      if (typeof key === "string" && key) value = value.split(key).join("");
    }
    return value.trim();
  }

  function parseContent(raw: unknown): any {
    if (typeof raw !== "string" || raw.length > 262144) return undefined;
    try {
      return JSON.parse(raw);
    } catch {
      return undefined;
    }
  }

  // stageImages downloads message resources into the platform staging directory
  // and returns their base names. The platform re-validates content and applies
  // its own limits, so a vendor-declared type is never trusted.
  async function stageImages(
    clientValue: lark.Client,
    messageId: string,
    keys: string[],
  ): Promise<{ name: string; file: string }[]> {
    if (!keys.length || !configured?.mediaDir) return [];
    const staged: { name: string; file: string }[] = [];
    for (const [index, key] of keys.entries()) {
      const response = await clientValue.im.v1.messageResource.get({
        params: { type: "image" },
        path: { message_id: messageId, file_key: key },
      });
      const buffer = await readStream(response.getReadableStream());
      if (buffer.length === 0 || buffer.length > maxInboundImageBytes) continue;
      const file = `${messageId.replace(/[^A-Za-z0-9_-]/g, "_")}-${index}-${randomBytes(6).toString("hex")}.bin`;
      await writeFile(join(configured.mediaDir, file), buffer, { mode: 0o600 });
      staged.push({ name: `图片${index + 1}`, file });
    }
    return staged;
  }

  function readStream(stream: Readable): Promise<Buffer> {
    return new Promise((resolve, reject) => {
      const chunks: Buffer[] = [];
      let size = 0;
      stream.on("data", (chunk: Buffer) => {
        size += chunk.length;
        if (size > maxInboundImageBytes) {
          stream.destroy();
          reject(new Error("resource exceeds the ingest limit"));
          return;
        }
        chunks.push(chunk);
      });
      stream.on("error", reject);
      stream.on("end", () => resolve(Buffer.concat(chunks)));
    });
  }

  // send performs exactly one SDK request per delivery. The platform retries only
  // when the vendor clearly rejected the request and never replays an uncertain
  // outcome, so this function classifies honestly instead of throwing.
  async function send(
    id: string,
    delivery: any,
    session: Session,
  ): Promise<void> {
    const result = (state: string, code = 0) =>
      session.output({
        type: "result",
        id,
        result: {
          state,
          code,
          message:
            state === "sent"
              ? "渠道已接受。"
              : state === "retry"
                ? "渠道尚未连接，未发送消息。"
                : state === "failed"
                  ? "渠道明确拒绝了消息，请检查配置和发送限制。"
                  : "未收到投递确认，请人工核对，不自动重发。",
        },
      });
    const clientValue = client;
    if (!clientValue || !connected) {
      result("retry");
      return;
    }
    if (
      typeof delivery?.text !== "string" ||
      delivery.text.length === 0 ||
      Buffer.byteLength(delivery.text) > maxTextBytes ||
      typeof delivery?.reply_context?.message_id !== "string" ||
      typeof delivery?.reply_context?.chat_id !== "string" ||
      typeof delivery?.inbox_id !== "string"
    ) {
      result("failed");
      return;
    }
    const key = `${delivery.inbox_id}:${delivery.part ?? 0}`;
    const content = JSON.stringify(
      streamCard(delivery.text, delivery.kind === "final"),
    );
    try {
      const existing = cards.get(key);
      if (existing) {
        const patched = await clientValue.im.v1.message.patch({
          path: { message_id: existing },
          data: { content },
        });
        const code = responseCode(patched);
        if (code !== 0) {
          result(classify(code), code);
          return;
        }
      } else {
        const created = await clientValue.im.v1.message.reply({
          path: { message_id: delivery.reply_context.message_id },
          data: { msg_type: "interactive", content, reply_in_thread: false },
        });
        const code = responseCode(created);
        if (code !== 0) {
          result(classify(code), code);
          return;
        }
        const messageId = created?.data?.message_id;
        if (typeof messageId !== "string" || !messageId) {
          result("unknown");
          return;
        }
        // Bound the map so a long-running channel cannot grow without limit; an
        // evicted card simply starts a new message on the next update.
        if (cards.size >= 512) cards.clear();
        cards.set(key, messageId);
      }
      if (delivery.kind === "final") cards.delete(key);
      result("sent");
    } catch (error) {
      const code = larkErrorCode(error);
      if (code === undefined) result("unknown");
      else result(classify(code), code);
    }
  }

  // streamCard renders one markdown card. Interactive cards are the only message
  // type the bot can update in place through the message patch API.
  function streamCard(text: string, finished: boolean) {
    return {
      config: { wide_screen_mode: true, update_multi: true },
      elements: [
        { tag: "markdown", content: text },
        ...(finished
          ? []
          : [
              {
                tag: "note",
                elements: [{ tag: "plain_text", content: "正在作答…" }],
              },
            ]),
      ],
    };
  }

  // responseCode reads the vendor's business code. The SDK resolves a successful
  // HTTP exchange even when Feishu rejected the request, so the body carries the
  // only trustworthy outcome.
  function responseCode(response: unknown): number {
    const code = (response as { code?: unknown } | undefined)?.code;
    return typeof code === "number" && Number.isInteger(code) ? code : 0;
  }

  function larkErrorCode(error: unknown): number | undefined {
    if (!error || typeof error !== "object") return undefined;
    const response = (error as { response?: { data?: { code?: unknown } } })
      .response;
    const code = response?.data?.code;
    return typeof code === "number" && Number.isInteger(code) ? code : undefined;
  }

  // classify maps a vendor code to the platform's delivery states. 99991400 and
  // 99991401 are frequency limits and 99991663 a transient token problem: the
  // request was refused outright, so retrying is safe. Everything else is a
  // definite rejection the operator must act on.
  function classify(code: number): "retry" | "failed" {
    return [99991400, 99991401, 99991663].includes(code) ? "retry" : "failed";
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  runWorker();
