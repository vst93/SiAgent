import { WSClient, type WsFrame } from "@wecom/aibot-node-sdk";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

// The transport worker owns only the SDK connection. Go owns durable admission,
// authorization, sessions and delivery retries. No Agent/model code runs here.
export function runWorker(wsUrl?: string) {
  let client: WSClient | undefined;
  let connected = false;
  let closing = false;
  const outstanding = new Set<string>();
  const output = (value: object) => {
    if (closing) return;
    if (process.stdout.writableLength > 1048576) {
      close();
      return;
    }
    process.stdout.write(JSON.stringify({ v: 1, ...value }) + "\n");
  };
  const state = (value: string) => output({ type: "status", state: value });
  const close = () => {
    if (closing) return;
    closing = true;
    connected = false;
    client?.disconnect();
    lines.close();
    process.stdin.destroy();
  };
  const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
  // Bound partial frames as well as complete lines.
  let bytes = 0;
  process.stdin.on("data", (chunk: Buffer) => {
    for (const byte of chunk) {
      bytes = byte === 10 ? 0 : bytes + 1;
      if (bytes > 262144) {
        close();
        break;
      }
    }
  });
  lines.on("close", close);
  process.on("SIGTERM", close);
  process.on("SIGINT", close);
  lines.on("line", (line) => {
    try {
      if (Buffer.byteLength(line) > 262144) {
        close();
        return;
      }
      const frame = JSON.parse(line);
      if (frame.v !== 1) {
        close();
        return;
      }
      if (frame.type === "configure" && !client) {
        const values = frame.config?.config;
        const secrets = frame.config?.secrets;
        if (
          typeof values?.bot_id !== "string" ||
          typeof secrets?.secret !== "string"
        ) {
          close();
          return;
        }
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
          state("connected");
        });
        client.on("disconnected", () => {
          connected = false;
          state("disconnected");
        });
        client.on("reconnecting", () => {
          connected = false;
          state("reconnecting");
        });
        client.on("error", () => {
          connected = false;
          state("failed");
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
          if (outstanding.size >= 128) {
            client?.disconnect();
            connected = false;
            state("failed");
            return;
          }
          outstanding.add(body.msgid);
          output({
            type: "incoming",
            incoming: {
              id: body.msgid,
              chat_type: body.chattype,
              sender_id: sender,
              group_id: chat,
              session: JSON.stringify([body.chattype, chat, sender]),
              kind: body.msgtype,
              text:
                body.msgtype === "text" &&
                typeof body.text?.content === "string"
                  ? body.text.content
                  : "",
              reply_context: {
                req_id: req,
                chat_id: chat,
                sender,
                received_at: String(Date.now()),
              },
            },
          });
        });
        state("connecting");
        client.connect();
      } else if (frame.type === "receipt") {
        outstanding.delete(frame.id);
        if (!frame.accepted) {
          connected = false;
          client?.disconnect();
          state("failed");
        }
      } else if (frame.type === "send") {
        void send(frame.id, frame.delivery);
      } else close();
    } catch {
      close();
    }
  });
  async function send(id: string, delivery: any) {
    const result = (state: string, code = 0) =>
      output({
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
