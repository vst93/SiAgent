import { WSClient, type WsFrame } from "@wecom/aibot-node-sdk";
import { randomBytes } from "node:crypto";
import { unlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { serve } from "../frames.js";

// The transport worker owns only the SDK connection. Go owns durable admission,
// authorization, sessions and delivery retries. No Agent/model code runs here.

// WeCom sends images as an encrypted URL that expires in five minutes; only this
// worker holds the per-message AES key, so it downloads and decrypts the bytes
// and stages them for the platform. The platform validates the content, applies
// its own size limits and turns accepted files into ordinary chat attachments.
const maxInboundImages = 8;
const maxInboundImageBytes = 16 * 1024 * 1024;
// Bounded in-flight messages: the platform must confirm durable admission, so an
// unbounded queue would only hide a stalled consumer.
const maxOutstanding = 128;

export function runWorker(wsUrl?: string) {
  let client: WSClient | undefined;
  let connected = false;
  let mediaDir = "";
  const outstanding = new Set<string>();
  serve((frame, session) => {
    if (frame.type === "configure" && !client) {
      const values = frame.config?.config;
      const secrets = frame.config?.secrets;
      if (
        typeof values?.bot_id !== "string" ||
        typeof secrets?.secret !== "string"
      ) {
        session.close();
        return;
      }
      // The platform creates this directory and owns it; the worker only stages
      // downloaded pictures there so images never travel inside a protocol frame.
      if (typeof frame.config?.media_dir === "string")
        mediaDir = frame.config.media_dir;
      const quiet = { debug() {}, info() {}, warn() {}, error() {} };
      client = new WSClient({
        botId: values.bot_id,
        secret: secrets.secret,
        logger: quiet,
        maxReconnectAttempts: -1,
        maxAuthFailureAttempts: 3,
        maxReplyQueueSize: 1,
        ...(wsUrl ? { wsUrl } : {}),
        wsOptions: { maxPayload: 262144, handshakeTimeout: 10000 },
      });
      client.on("authenticated", () => {
        connected = true;
        session.state("connected");
      });
      client.on("disconnected", () => {
        connected = false;
        session.state("disconnected");
      });
      client.on("reconnecting", () => {
        connected = false;
        session.state("reconnecting");
      });
      client.on("error", () => {
        connected = false;
        session.state("failed");
      });
      client.on("message", (message: WsFrame) => {
        const body = message.body as Record<string, any> | undefined;
        if (
          !body ||
          body.aibotid !== values.bot_id ||
          body.compat_info ||
          !["single", "group"].includes(body.chattype)
        )
          return;
        const sender = body.from?.userid;
        const chat = body.chattype === "group" ? body.chatid : "";
        const req = message.headers?.req_id;
        if (
          typeof body.msgid !== "string" ||
          body.msgid.length > 256 ||
          typeof sender !== "string" ||
          !sender ||
          sender.length > 256 ||
          typeof req !== "string" ||
          !req ||
          req.length > 256 ||
          (body.chattype === "group" &&
            (typeof chat !== "string" || !chat || chat.length > 256))
        )
          return;
        if (outstanding.size >= maxOutstanding) {
          client?.disconnect();
          connected = false;
          session.state("failed");
          return;
        }
        outstanding.add(body.msgid);
        // Resolve only the explicit quote carried by this callback. Do not
        // infer attachments from other messages or follow nested quotes.
        // Download all pictures before reporting one complete question.
        const direct = describeInbound(body);
        const quoted = describeInbound(body.quote ?? {});
        const pictures = [...direct.pictures, ...quoted.pictures];
        const text = [
          direct.text,
          quoted.text ? `[引用内容]\n${quoted.text}\n[/引用内容]` : "",
        ]
          .filter(Boolean)
          .join("\n\n");
        const imageCount = direct.imageCount + quoted.imageCount;
        const unsupported = direct.unsupported || quoted.unsupported;
        const typeName = (value: unknown) =>
          ["text", "image", "mixed", "voice", "file", "video"].includes(String(value))
            ? String(value)
            : "unknown";
        const report = (images: { name: string; file: string }[], failed: boolean) =>
          session.output({
            type: "incoming",
            incoming: {
              id: body.msgid,
              chat_type: body.chattype,
              sender_id: sender,
              group_id: chat,
              session: JSON.stringify([body.chattype, chat, sender]),
              kind: unsupported ? "file" : imageCount > 0 ? "image" : "text",
              text,
              images,
              reply_context: {
                req_id: req,
                chat_id: chat,
                sender,
                received_at: String(Date.now()),
                media_diagnostic: `正文=${typeName(body.msgtype)}；引用=${body.quote == null ? "none" : typeName(body.quote.msgtype)}；正文图片=${direct.imageCount}；引用图片=${quoted.imageCount}；可下载图片=${pictures.length}；已暂存=${images.length}；处理=${unsupported ? "unsupported" : failed ? "failed" : "ok"}`,
              },
            },
          });
        if (unsupported) {
          report([], false);
        } else if (imageCount !== pictures.length || imageCount > maxInboundImages) {
          report([], true);
        } else {
          void stageImages(body.msgid, pictures)
            .then((images) => report(images, imageCount > 0 && images.length === 0))
            .catch(() => report([], true));
        }
      });
      session.onClose(() => client?.disconnect());
      session.state("connecting");
      client.connect();
      return;
    }
    if (frame.type === "receipt") {
      outstanding.delete(frame.id);
      if (!frame.accepted) {
        connected = false;
        client?.disconnect();
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

  // describeInbound flattens the text of a text, image or mixed message and
  // collects the picture descriptors WeCom sent with it.
  function describeInbound(body: Record<string, any>): {
    text: string;
    pictures: { url: string; aeskey?: string }[];
    imageCount: number;
    unsupported: boolean;
  } {
    const pictures: { url: string; aeskey?: string }[] = [];
    let imageCount = 0;
    const unsupported = ["file", "video"].includes(body.msgtype);
    const pushPicture = (image: any) => {
      imageCount++;
      if (
        pictures.length < maxInboundImages &&
        image &&
        typeof image.url === "string" &&
        image.url.length > 0 &&
        image.url.length <= 4096
      )
        pictures.push({
          url: image.url,
          aeskey: typeof image.aeskey === "string" ? image.aeskey : undefined,
        });
    };
    if (body.msgtype === "text")
      return {
        text: typeof body.text?.content === "string" ? body.text.content : "",
        pictures,
        imageCount,
        unsupported,
      };
    if (body.msgtype === "image") {
      pushPicture(body.image);
      return { text: "", pictures, imageCount, unsupported };
    }
    if (body.msgtype === "mixed" && Array.isArray(body.mixed?.msg_item)) {
      const parts: string[] = [];
      for (const item of body.mixed.msg_item) {
        if (item?.msgtype === "text" && typeof item.text?.content === "string")
          parts.push(item.text.content);
        else if (item?.msgtype === "image") pushPicture(item.image);
      }
      return { text: parts.join("\n"), pictures, imageCount, unsupported };
    }
    // Voice messages carry their transcript, which is the useful content.
    if (body.msgtype === "voice")
      return {
        text:
          typeof body.voice?.content === "string" ? body.voice.content : "",
        pictures,
        imageCount,
        unsupported,
      };
    return { text: "", pictures, imageCount, unsupported };
  }

  // stageImages downloads and decrypts each picture into the platform staging
  // directory. The bytes never enter a protocol frame: one frame is capped at
  // 256 KiB while a picture may be several MiB.
  async function stageImages(
    msgid: string,
    pictures: { url: string; aeskey?: string }[],
  ): Promise<{ name: string; file: string }[]> {
    if (!pictures.length || !mediaDir || !client) return [];
    const staged: { name: string; file: string }[] = [];
    try {
      for (const [index, picture] of pictures.entries()) {
        const { buffer, filename } = await client.downloadFile(
          picture.url,
          picture.aeskey,
        );
        if (buffer.length === 0 || buffer.length > maxInboundImageBytes)
          throw new Error("Invalid image size");
        // A random name keeps the staging area inert even if two messages race.
        const file = `${msgid.replace(/[^A-Za-z0-9_-]/g, "_")}-${index}-${randomBytes(6).toString("hex")}.bin`;
        staged.push({
          name: typeof filename === "string" ? filename : `图片${index + 1}`,
          file,
        });
        await writeFile(join(mediaDir, file), buffer, { mode: 0o600 });
      }
      return staged;
    } catch {
      await Promise.all(
        staged.map((image) =>
          unlink(join(mediaDir, image.file)).catch(() => {}),
        ),
      );
      throw new Error("Image staging failed");
    }
  }

  async function send(id: string, delivery: any, session: any) {
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
    if (!client || !connected) {
      result("retry");
      return;
    }
    if (
      typeof delivery?.reply_context?.req_id !== "string" ||
      typeof delivery?.text !== "string" ||
      Buffer.byteLength(delivery.text) > 20000 ||
      typeof delivery?.inbox_id !== "string"
    ) {
      result("failed");
      return;
    }
    try {
      // WeCom ends a stream after ten minutes, so each nine-minute window uses a
      // different stream ID. Within one window the acknowledgement, drafts and
      // the final answer replace the same message; after that a new message is
      // started rather than updating an expired one.
      const received = Number(delivery.reply_context.received_at);
      const elapsed =
        Number.isFinite(received) && received > 0 ? Date.now() - received : 0;
      const window = Math.max(0, Math.floor(elapsed / (9 * 60 * 1000)));
      const streamId = `siagent-${delivery.inbox_id}-${delivery.part}${
        window > 0 ? `-r${window}` : ""
      }`;
      await client.replyStream(
        { headers: { req_id: delivery.reply_context.req_id } },
        streamId,
        delivery.text,
        delivery.kind === "final",
      );
      result("sent");
    } catch (error) {
      const code =
        error && typeof error === "object" && "errcode" in error
          ? (error as { errcode: unknown }).errcode
          : undefined;
      if (typeof code === "number" && Number.isInteger(code) && code !== 0)
        result([-1, 45009].includes(code) ? "retry" : "failed", code);
      else result("unknown");
    }
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  runWorker();
