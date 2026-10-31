import WebSocket from "ws";
import { randomBytes } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { serve, type Session } from "../frames.js";

// The QQ worker wraps the official QQ Open Platform Bot API v2: an app access
// token, a WebSocket gateway with heartbeat/resume, and HTTP sends. Everything
// durable (admission, dedup, sessions, retries) stays in Go; this worker only
// translates events and performs one HTTP send per delivery.

const defaultTokenURL = "https://bots.qq.com/app/getAppAccessToken";
const productionBase = "https://api.sgroup.qq.com";
const sandboxBase = "https://sandbox.api.sgroup.qq.com";

// GROUP_AND_C2C_EVENT (group @ messages + private messages) and INTERACTION_CREATE.
const intents = (1 << 25) | (1 << 26);
const maxTextBytes = 2000;
const maxInboundImages = 8;
const maxInboundImageBytes = 16 * 1024 * 1024;
const maxOutstanding = 128;

const opDispatch = 0;
const opHeartbeat = 1;
const opIdentify = 2;
const opResume = 6;
const opReconnect = 7;
const opInvalidSession = 9;
const opHello = 10;
const opHeartbeatACK = 11;

interface WorkerOverrides {
  /** Test seam: alternate token endpoint. */
  tokenURL?: string;
  /** Test seam: alternate QQ OpenAPI base URL. */
  apiBase?: string;
}

interface Configured {
  appId: string;
  appSecret: string;
  apiBase: string;
  mediaDir: string;
}

export function runWorker(overrides: WorkerOverrides = {}) {
  let configured: Configured | undefined;
  let socket: WebSocket | undefined;
  let session: Session | undefined;
  let connected = false;
  let closing = false;
  let sessionId = "";
  let lastSeq: number | null = null;
  let heartbeatTimer: NodeJS.Timeout | undefined;
  let awaitingAck = false;
  let reconnectTimer: NodeJS.Timeout | undefined;
  let reconnectAttempt = 0;
  let accessToken = "";
  let tokenExpires = 0;
  const outstanding = new Set<string>();
  // One msg_seq per inbound message ID; QQ requires strictly increasing values
  // for multiple replies to the same event.
  const msgSeq = new Map<string, number>();
  const tokenURL = overrides.tokenURL ?? defaultTokenURL;

  serve((frame, current) => {
    session = current;
    if (frame.type === "configure" && !configured) {
      const values = frame.config?.config;
      const secrets = frame.config?.secrets;
      if (
        typeof values?.app_id !== "string" ||
        typeof secrets?.app_secret !== "string"
      ) {
        current.close();
        return;
      }
      configured = {
        appId: values.app_id,
        appSecret: secrets.app_secret,
        apiBase:
          overrides.apiBase ??
          (values.environment === "sandbox" ? sandboxBase : productionBase),
        mediaDir:
          typeof frame.config?.media_dir === "string"
            ? frame.config.media_dir
            : "",
      };
      session.onClose(() => stop());
      current.state("connecting");
      void connect();
      return;
    }
    if (frame.type === "receipt") {
      outstanding.delete(frame.id);
      if (!frame.accepted) {
        connected = false;
        current.state("failed");
        stop();
      }
      return;
    }
    if (frame.type === "send") {
      void send(frame.id, frame.delivery, current);
      return;
    }
    current.close();
  });

  function stop(): void {
    if (closing) return;
    closing = true;
    connected = false;
    if (heartbeatTimer) clearTimeout(heartbeatTimer);
    if (reconnectTimer) clearTimeout(reconnectTimer);
    heartbeatTimer = undefined;
    reconnectTimer = undefined;
    try {
      socket?.close(1000, "shutdown");
    } catch {
      // Best effort: the host kills the process group anyway.
    }
    socket = undefined;
  }

  async function connect(): Promise<void> {
    if (closing || !configured || !session) return;
    try {
      accessToken = await fetchToken();
      const gateway = await fetchGateway();
      const ws = new WebSocket(gateway);
      socket = ws;
      ws.on("message", (data) => handleFrame(ws, data.toString()));
      ws.on("error", () => {
        // The close handler owns reconnection; an error alone is not terminal.
      });
      ws.on("close", () => {
        if (closing) return;
        connected = false;
        session?.state("reconnecting");
        scheduleReconnect();
      });
    } catch (error) {
      connected = false;
      if (closing) return;
      const code = httpStatus(error);
      if (code === 401 || code === 403) {
        session?.state("failed");
        stop();
        return;
      }
      session?.state("reconnecting");
      scheduleReconnect();
    }
  }

  function scheduleReconnect(): void {
    if (closing || reconnectTimer) return;
    reconnectAttempt += 1;
    if (reconnectAttempt > 20) {
      session?.state("failed");
      stop();
      return;
    }
    const delay = Math.min(1000 * 2 ** (reconnectAttempt - 1), 30000);
    reconnectTimer = setTimeout(() => {
      reconnectTimer = undefined;
      void connect();
    }, delay);
  }

  async function fetchToken(): Promise<string> {
    if (!configured) throw new Error("not configured");
    if (accessToken && Date.now() < tokenExpires - 60000) return accessToken;
    const response = await fetch(tokenURL, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        appId: configured.appId,
        clientSecret: configured.appSecret,
      }),
    });
    const body: any = await response.json().catch(() => ({}));
    if (!response.ok || typeof body?.access_token !== "string") {
      throw new HttpError(response.status, "token request failed");
    }
    accessToken = body.access_token;
    const expires = Number(body.expires_in);
    tokenExpires = Date.now() + (Number.isFinite(expires) && expires > 0 ? expires : 7200) * 1000;
    return accessToken;
  }

  async function fetchGateway(): Promise<string> {
    if (!configured) throw new Error("not configured");
    const token = await fetchToken();
    const response = await fetch(`${configured.apiBase}/gateway/bot`, {
      headers: { Authorization: `QQBot ${token}` },
    });
    const body: any = await response.json().catch(() => ({}));
    if (!response.ok || typeof body?.url !== "string" || !body.url) {
      throw new HttpError(response.status, "gateway request failed");
    }
    return body.url;
  }

  function handleFrame(ws: WebSocket, raw: string): void {
    if (closing) return;
    let frame: any;
    try {
      frame = JSON.parse(raw);
    } catch {
      ws.close(1002, "invalid frame");
      return;
    }
    if (typeof frame?.s === "number") lastSeq = frame.s;
    switch (frame?.op) {
      case opHello: {
        const interval = Number(frame?.d?.heartbeat_interval);
        beginHeartbeat(Number.isFinite(interval) && interval > 0 ? interval : 41250);
        if (sessionId && lastSeq !== null && lastSeq > 0) resume(ws);
        else identify(ws);
        return;
      }
      case opDispatch:
        if (frame.t === "READY") {
          sessionId = typeof frame?.d?.session_id === "string" ? frame.d.session_id : "";
          reconnectAttempt = 0;
          connected = true;
          session?.state("connected");
        } else if (frame.t === "RESUMED") {
          reconnectAttempt = 0;
          connected = true;
          session?.state("connected");
        } else if (frame.t === "C2C_MESSAGE_CREATE") {
          void inbound("single", frame.d);
        } else if (frame.t === "GROUP_AT_MESSAGE_CREATE") {
          void inbound("group", frame.d);
        }
        return;
      case opHeartbeatACK:
        awaitingAck = false;
        return;
      case opReconnect:
        ws.close(4000, "server requested reconnect");
        return;
      case opInvalidSession:
        // Not resumable (or session lost): start a fresh identify next time.
        sessionId = "";
        lastSeq = null;
        ws.close(4000, "invalid session");
        return;
      case opHeartbeat:
        sendHeartbeat(ws);
        return;
      default:
        return;
    }
  }

  function identify(ws: WebSocket): void {
    if (!accessToken) return;
    ws.send(
      JSON.stringify({
        op: opIdentify,
        d: {
          token: `QQBot ${accessToken}`,
          intents,
          shard: [0, 1],
          properties: {},
        },
      }),
    );
  }

  function resume(ws: WebSocket): void {
    if (!accessToken) return;
    ws.send(
      JSON.stringify({
        op: opResume,
        d: { token: `QQBot ${accessToken}`, session_id: sessionId, seq: lastSeq },
      }),
    );
  }

  function beginHeartbeat(interval: number): void {
    if (heartbeatTimer) clearTimeout(heartbeatTimer);
    heartbeatTimer = setTimeout(() => {
      const ws = socket;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (awaitingAck) {
        // No ACK for a whole interval: treat the connection as dead.
        ws.terminate();
        return;
      }
      sendHeartbeat(ws);
      beginHeartbeat(interval);
    }, interval);
  }

  function sendHeartbeat(ws: WebSocket): void {
    awaitingAck = true;
    ws.send(JSON.stringify({ op: opHeartbeat, d: lastSeq }));
  }

  // inbound normalizes one QQ event into a platform frame. Group events only
  // arrive when the bot was mentioned; the API also leaves the @ tag in content.
  async function inbound(kind: string, data: any, sessionValue = session): Promise<void> {
    const values = configured;
    if (!values || !sessionValue) return;
    const id = data?.id;
    const author = data?.author ?? {};
    const sender =
      kind === "group"
        ? author.member_openid ?? author.union_openid
        : author.user_openid ?? author.union_openid;
    const group = kind === "group" ? data?.group_openid : "";
    if (
      typeof id !== "string" ||
      !id ||
      id.length > 256 ||
      typeof sender !== "string" ||
      !sender ||
      sender.length > 256 ||
      (kind === "group" && (typeof group !== "string" || !group || group.length > 256))
    )
      return;
    if (outstanding.size >= maxOutstanding) {
      connected = false;
      sessionValue.state("failed");
      stop();
      return;
    }
    outstanding.add(id);
    let text = typeof data?.content === "string" ? data.content : "";
    if (kind === "group") text = stripMention(text);
    text = text.trim();
    const attachments = Array.isArray(data?.attachments) ? data.attachments : [];
    let images: { name: string; file: string }[] = [];
    try {
      images = await stageImages(attachments);
    } catch {
      // A failed download must not lose the text the member sent.
      images = [];
    }
    const replyContext: Record<string, string> = {
      msg_id: id,
      chat_type: kind,
      sender,
      received_at: String(Date.now()),
    };
    if (group) replyContext.group_openid = group;
    sessionValue.output({
      type: "incoming",
      incoming: {
        id,
        chat_type: kind,
        sender_id: sender,
        group_id: group,
        session: JSON.stringify([kind, group, sender]),
        kind: images.length > 0 ? "image" : "text",
        text,
        images,
        reply_context: replyContext,
      },
    });
  }

  // stripMention removes the "<@!botid>" prefix the gateway puts in group
  // content so commands and questions read normally.
  function stripMention(content: string): string {
    let value = content.trim();
    if (value.startsWith("<@!")) {
      const end = value.indexOf(">");
      if (end >= 0) value = value.slice(end + 1).trim();
    }
    return value;
  }

  async function stageImages(attachments: any[]): Promise<{ name: string; file: string }[]> {
    const values = configured;
    if (!values?.mediaDir || !attachments.length) return [];
    const staged: { name: string; file: string }[] = [];
    for (const [index, attachment] of attachments.entries()) {
      if (staged.length >= maxInboundImages) break;
      const type = typeof attachment?.content_type === "string" ? attachment.content_type : "";
      if (!type.startsWith("image/")) continue;
      let url = typeof attachment?.url === "string" ? attachment.url : "";
      if (!url) continue;
      if (!url.startsWith("http")) url = `https://${url}`;
      try {
        const response = await fetch(url);
        if (!response.ok) continue;
        const buffer = Buffer.from(await response.arrayBuffer());
        if (buffer.length === 0 || buffer.length > maxInboundImageBytes) continue;
        const file = `qq-${Date.now()}-${index}-${randomBytes(6).toString("hex")}.bin`;
        await writeFile(join(values.mediaDir, file), buffer, { mode: 0o600 });
        staged.push({
          name:
            typeof attachment?.filename === "string" && attachment.filename
              ? attachment.filename
              : `图片${index + 1}`,
          file,
        });
      } catch {
        // Skip this attachment; the message text is still delivered.
      }
    }
    return staged;
  }

  // send performs one HTTP request per delivery. The platform retries only when
  // the vendor clearly refused the request and never replays an uncertain one,
  // so the outcome mapping below must stay honest.
  async function send(id: string, delivery: any, current: Session): Promise<void> {
    const result = (state: string, code = 0, message?: string) =>
      current.output({
        type: "result",
        id,
        result: {
          state,
          code,
          message:
            message ??
            (state === "sent"
              ? "渠道已接受。"
              : state === "retry"
                ? "渠道尚未连接或明确限流，稍后有限重试。"
                : state === "failed"
                  ? "渠道明确拒绝了消息，请检查配置和发送限制。"
                  : "未收到投递确认，请人工核对，不自动重发。"),
        },
      });
    const values = configured;
    if (!values || !connected) {
      result("retry");
      return;
    }
    const text = delivery?.text;
    const reply = delivery?.reply_context ?? {};
    if (
      typeof text !== "string" ||
      text.length === 0 ||
      Buffer.byteLength(text) > maxTextBytes ||
      typeof reply.msg_id !== "string" ||
      typeof reply.chat_type !== "string"
    ) {
      result("failed", 0, "回复内容无效或超过单条上限。");
      return;
    }
    const endpoint =
      reply.chat_type === "group"
        ? `${values.apiBase}/v2/groups/${encodeURIComponent(reply.group_openid)}/messages`
        : `${values.apiBase}/v2/users/${encodeURIComponent(reply.sender)}/messages`;
    if (reply.chat_type === "group" && !reply.group_openid) {
      result("failed", 0, "缺少群标识，无法回复。");
      return;
    }
    const body = JSON.stringify({
      content: text,
      msg_type: 0,
      msg_id: reply.msg_id,
      msg_seq: nextSeq(reply.msg_id),
    });
    try {
      const response = await postMessage(endpoint, body, true);
      if (response.status === 401) {
        // Expired access token: refresh once and retry the same request.
        accessToken = "";
        const retry = await postMessage(endpoint, body, true);
        if (retry.ok) {
          result("sent");
          return;
        }
        result(classify(retry.status, retry.code), retry.code);
        return;
      }
      if (response.ok) {
        result("sent");
        return;
      }
      result(classify(response.status, response.code), response.code);
    } catch {
      result("unknown");
    }
  }

  async function postMessage(
    endpoint: string,
    body: string,
    allowToken: boolean,
  ): Promise<{ ok: boolean; status: number; code: number }> {
    if (!allowToken || closing) return { ok: false, status: 0, code: 0 };
    const token = await fetchToken();
    const response = await fetch(endpoint, {
      method: "POST",
      headers: {
        Authorization: `QQBot ${token}`,
        "content-type": "application/json",
      },
      body,
    });
    const payload: any = await response.json().catch(() => ({}));
    const code = typeof payload?.code === "number" ? payload.code : 0;
    if (response.ok) return { ok: true, status: response.status, code: 0 };
    if (response.status === 401) return { ok: false, status: 401, code };
    if (response.status === 429) return { ok: false, status: 429, code };
    if (response.status >= 500) return { ok: false, status: response.status, code };
    return { ok: false, status: response.status, code };
  }

  function classify(status: number, code: number): "sent" | "retry" | "failed" | "unknown" {
    if (status === 429) return "retry";
    if (status === 401 || status === 403) return "failed";
    if (status >= 500 || status === 0) return "unknown";
    if (status >= 400) return "failed";
    return code === 0 ? "sent" : "failed";
  }

  function nextSeq(messageId: string): number {
    const next = (msgSeq.get(messageId) ?? 0) + 1;
    if (msgSeq.size > 512) msgSeq.clear();
    msgSeq.set(messageId, next);
    return next;
  }

  function httpStatus(error: unknown): number | undefined {
    if (error instanceof HttpError) return error.status;
    return undefined;
  }
}

class HttpError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  runWorker();
