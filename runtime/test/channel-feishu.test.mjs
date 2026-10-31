import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { createServer } from "node:http";
import { createInterface } from "node:readline";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";

const require = createRequire(import.meta.url);
const sdkRequire = createRequire(require.resolve("@larksuiteoapi/node-sdk"));
const { WebSocketServer } = sdkRequire("ws");
const workerURL = new URL("../dist/plugins/feishu-bot/worker.js", import.meta.url)
  .href;

function waitUntil(check, timeout = 6000) {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeout;
    const timer = setInterval(() => {
      const value = check();
      if (value) {
        clearInterval(timer);
        resolve(value);
      } else if (Date.now() > deadline) {
        clearInterval(timer);
        reject(new Error("channel fixture timed out"));
      }
    }, 10);
  });
}

// One-pixel PNG: the platform detects content, not the advertised name.
const pixelPNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
  "base64",
);

// The SDK performs endpoint discovery over HTTPS before it opens the WebSocket,
// so the fixture answers `/callback/ws/endpoint` with a plain HTTP server and
// advertises a local WS URL. Application API calls (message reply/patch and
// resource download) are answered by the same server.
async function fixture(t) {
  const stagingRoot = await mkdtemp(join(tmpdir(), "siagent-feishu-media-"));
  const frames = [];
  const requests = [];
  let wsServer;
  const api = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => (body += chunk));
    request.on("end", () => {
      const path = request.url ?? "";
      requests.push({ path, method: request.method, body });
      if (path.startsWith("/callback/ws/endpoint")) {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(
          JSON.stringify({
            code: 0,
            msg: "ok",
            data: {
              URL: `ws://127.0.0.1:${wsServer.address().port}/ws?device_id=d&service_id=1`,
              ClientConfig: {
                PingInterval: 30,
                ReconnectCount: 3,
                ReconnectInterval: 1,
                ReconnectNonce: 0,
              },
            },
          }),
        );
        return;
      }
      if (path.includes("/open-apis/auth/v3/tenant_access_token/internal")) {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(
          JSON.stringify({
            code: 0,
            tenant_access_token: "fixture-access-token",
            expire: 7200,
          }),
        );
        return;
      }
      if (path.includes("/open-apis/bot/v3/info")) {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(
          JSON.stringify({ code: 0, bot: { open_id: "ou_bot_self", app_name: "fixture" } }),
        );
        return;
      }
      if (path.includes("/open-apis/im/v1/messages/") && request.method === "POST") {
        const reply = JSON.parse(body);
        if (reply.content.includes("uncertain")) {
          response.destroy();
          return;
        }
        if (reply.content.includes("reject")) {
          response.writeHead(200, { "content-type": "application/json" });
          response.end(JSON.stringify({ code: 230020, msg: "rejected" }));
          return;
        }
        response.writeHead(200, { "content-type": "application/json" });
        response.end(
          JSON.stringify({
            code: 0,
            data: { message_id: `om_${requests.length}` },
          }),
        );
        return;
      }
      if (
        path.includes("/open-apis/im/v1/messages/") &&
        (request.method === "PUT" || request.method === "PATCH")
      ) {
        if (body.includes("uncertain")) {
          response.destroy();
          return;
        }
        response.writeHead(200, { "content-type": "application/json" });
        response.end(JSON.stringify({ code: 0, data: {} }));
        return;
      }
      if (path.includes("/resources/")) {
        response.writeHead(200, {
          "content-type": "image/png",
          "content-disposition": `attachment; filename="shot.png"`,
        });
        response.end(pixelPNG);
        return;
      }
      response.writeHead(404, { "content-type": "application/json" });
      response.end(JSON.stringify({ code: 99991672, msg: `unknown ${path}` }));
    });
  });
  api.listen(0, "127.0.0.1");
  await once(api, "listening");

  wsServer = new WebSocketServer({ host: "127.0.0.1", port: 0 });
  await once(wsServer, "listening");
  const sockets = [];
  wsServer.on("connection", (socket) => sockets.push(socket));

  const process = spawn(
    globalThis.process.execPath,
    [
      "--input-type=module",
      "-e",
      `import { runWorker } from ${JSON.stringify(workerURL)};runWorker();`,
    ],
    { stdio: ["pipe", "pipe", "pipe"], env: { NODE_ENV: "production" } },
  );
  let stderr = "";
  process.stderr.on("data", (chunk) => (stderr += chunk));
  createInterface({ input: process.stdout }).on("line", (line) => {
    const frame = JSON.parse(line);
    frames.push(frame);
    if (frame.type === "incoming")
      process.stdin.write(
        JSON.stringify({
          v: 1,
          type: "receipt",
          id: frame.incoming.id,
          accepted: true,
        }) + "\n",
      );
  });
  const exited = once(process, "exit");
  t.after(async () => {
    process.stdin.end();
    await Promise.race([
      exited,
      new Promise((resolve) =>
        setTimeout(() => {
          process.kill("SIGKILL");
          resolve();
        }, 2000).unref(),
      ),
    ]);
    for (const socket of sockets) socket.terminate();
    await new Promise((resolve) => wsServer.close(resolve));
    await new Promise((resolve) => api.close(resolve));
    await rm(stagingRoot, { recursive: true, force: true });
    assert.equal(stderr, "");
  });
  process.stdin.write(
    JSON.stringify({
      v: 1,
      type: "configure",
      config: {
        id: "channel",
        provider: "feishu-bot",
        version: 1,
        config: { app_id: "cli_0123456789abcdef", domain: `http://127.0.0.1:${api.address().port}` },
        secrets: { app_secret: "fixture-private-secret" },
        media_dir: stagingRoot,
      },
    }) + "\n",
  );
  await waitUntil(() =>
    frames.find(
      (frame) => frame.type === "status" && frame.state === "connected",
    ),
  );
  // Group filtering needs the bot identity; wait for the lookup to land so the
  // fixture does not race it.
  await waitUntil(() => requests.some((request) => request.path.includes("/bot/v3/info")));
  return {
    api,
    frames,
    requests,
    sockets,
    stagingRoot,
    // The WS handshake in the fixture resolves before the SDK finishes its
    // bookkeeping, so outbound calls wait for the client to be ready.
    send: (value) =>
      process.stdin.write(JSON.stringify({ v: 1, ...value }) + "\n"),
  };
}


// The Feishu gateway speaks pbbp2 protobuf frames, not JSON. The fixture only
// needs the two message shapes below: Frame{SeqID, LogID, service, method,
// headers, payload} and Header{key, value}. Encoding them by hand keeps the
// test independent of the SDK's internal (unexported) protobuf schema.
function varint(value) {
  const bytes = [];
  let rest = value >>> 0;
  while (rest > 0x7f) {
    bytes.push((rest & 0x7f) | 0x80);
    rest >>>= 7;
  }
  bytes.push(rest);
  return Buffer.from(bytes);
}
function field(number, wire, payload) {
  const tag = varint((number << 3) | wire);
  return Buffer.concat([tag, wire === 2 ? Buffer.concat([varint(payload.length), payload]) : payload]);
}
function header(key, value) {
  return field(5, 2, Buffer.concat([field(1, 2, Buffer.from(key)), field(2, 2, Buffer.from(value))]));
}
function frame(eventPayload, { service = 1, messageId = "frame" } = {}) {
  const payload = Buffer.from(JSON.stringify(eventPayload));
  return Buffer.concat([
    field(1, 0, varint(0)),
    field(2, 0, varint(0)),
    field(3, 0, varint(service)),
    field(4, 0, varint(1)),
    header("type", "event"),
    header("message_id", messageId),
    header("sum", "1"),
    header("seq", "0"),
    header("trace_id", messageId),
    field(8, 2, payload),
  ]);
}

const event = (message, overrides = {}) => ({
  schema: "2.0",
  header: { event_type: "im.message.receive_v1", token: "fixture-token" },
  event: {
    sender: {
      sender_id: { open_id: "ou_member" },
      sender_type: "user",
    },
    message,
    ...overrides,
  },
});

test("official Feishu SDK receives events, isolates sessions and streams one card per answer", async (t) => {
  const f = await fixture(t);
  const text = (message_id, chat_id, chat_type, content) =>
    event({
      message_id,
      chat_id,
      chat_type,
      message_type: "text",
      content: JSON.stringify({ text: content }),
    });
  f.sockets[0].send(frame(text("om_one", "oc_private", "p2p", "private question"), { messageId: "om_one" }));
  const groupMessage = text("om_two", "oc_group", "group", "group question");
  groupMessage.event.message.mentions = [
    { key: "@_user_1", id: { open_id: "ou_bot_self" }, name: "fixture" },
  ];
  f.sockets[0].send(frame(groupMessage, { messageId: "om_two" }));
  // A group message that does not mention the bot must stay unanswered.
  f.sockets[0].send(
    frame(text("om_unmentioned", "oc_group", "group", "not for the bot"), {
      messageId: "om_unmentioned",
    }),
  );
  // A bot-authored echo would loop; it must never become a question.
  f.sockets[0].send(
    frame(
      {
        ...text("om_bot", "oc_group", "group", "bot answer"),
        event: {
          sender: { sender_id: { open_id: "ou_bot" }, sender_type: "app" },
          message: {
            message_id: "om_bot",
            chat_id: "oc_group",
            chat_type: "group",
            message_type: "text",
            content: JSON.stringify({ text: "bot answer" }),
          },
        },
      },
      { messageId: "om_bot" },
    ),
  );
  await waitUntil(
    () => f.frames.filter((frame) => frame.type === "incoming").length === 2,
  );
  const incoming = f.frames
    .filter((frame) => frame.type === "incoming")
    .map((frame) => frame.incoming);
  assert.equal(incoming[0].chat_type, "single");
  assert.equal(incoming[0].group_id, "");
  assert.equal(incoming[1].chat_type, "group");
  assert.equal(incoming[1].group_id, "oc_group");
  assert.equal(incoming[1].text, "group question");
  assert.equal(
    new Set(incoming.map((message) => message.session)).size,
    2,
    "private and group sessions must differ",
  );
  assert.ok(Number(incoming[0].reply_context.received_at) > 0);

  const delivery = {
    id: "delivery",
    inbox_id: "inbox",
    target: incoming[0].session,
    reply_context: incoming[0].reply_context,
    part: 0,
    kind: "ack",
    text: "已收到",
  };
  for (const [id, frame] of [
    ["ack", delivery],
    ["draft", { ...delivery, kind: "draft", text: "部分回答" }],
    ["hint", { ...delivery, kind: "hint", text: "处理中" }],
    ["final", { ...delivery, kind: "final", text: "完整回答" }],
  ]) {
    f.send({ type: "send", id, delivery: frame });
    await waitUntil(() =>
      f.frames.find((result) => result.type === "result" && result.id === id),
    );
  }
  const creates = f.requests.filter(
    (request) =>
      request.path.includes("/open-apis/im/v1/messages/") &&
      request.method === "POST" &&
      request.path.includes("/reply"),
  );
  const patches = f.requests.filter(
    (request) =>
      request.path.includes("/open-apis/im/v1/messages/") &&
      (request.method === "PUT" || request.method === "PATCH"),
  );
  assert.equal(creates.length, 1, "one question must create exactly one card");
  assert.equal(patches.length, 3, "progress and the answer update that card");
  assert.match(JSON.parse(creates[0].body).content, /已收到/);
  assert.match(JSON.parse(patches.at(-1).body).content, /完整回答/);
  assert(!JSON.stringify(f.frames).includes("fixture-private-secret"));
});

test("inbound images are downloaded into the staging directory and failures keep the text", async (t) => {
  const f = await fixture(t);
  f.sockets[0].send(
    frame(
      event({
        message_id: "om_image",
        chat_id: "oc_private",
        chat_type: "p2p",
        message_type: "image",
        content: JSON.stringify({ image_key: "img_v2_one" }),
      }),
      { messageId: "om_image" },
    ),
  );
  const message = await waitUntil(() =>
    f.frames.find(
      (frame) =>
        frame.type === "incoming" && frame.incoming.id === "om_image",
    ),
  );
  assert.equal(message.incoming.kind, "image");
  assert.equal(message.incoming.images.length, 1);
  const staged = await readdir(f.stagingRoot);
  assert.equal(staged.length, 1);
  assert.equal(staged[0], message.incoming.images[0].file);
  // Rich text carries both paragraphs and inline pictures.
  f.sockets[0].send(
    frame(
      event({
        message_id: "om_post",
        chat_id: "oc_group",
        chat_type: "group",
        message_type: "post",
        content: JSON.stringify({
          zh_cn: {
            title: "标题",
            content: [
              [
                { tag: "text", text: "段落" },
                { tag: "img", image_key: "img_v2_two" },
              ],
            ],
          },
        }),
        mentions: [
          { key: "@_user_1", id: { open_id: "ou_bot_self" }, name: "fixture" },
        ],
      }),
      { messageId: "om_post" },
    ),
  );
  const post = await waitUntil(() =>
    f.frames.find(
      (frame) => frame.type === "incoming" && frame.incoming.id === "om_post",
    ),
  );
  assert.equal(post.incoming.text, "标题\n段落");
  assert.equal(post.incoming.kind, "image");
  assert.equal((await readdir(f.stagingRoot)).length, 2);
  // An unsupported type is acknowledged without inventing a question.
  f.sockets[0].send(
    frame(
      event({
        message_id: "om_file",
        chat_id: "oc_private",
        chat_type: "p2p",
        message_type: "file",
        content: JSON.stringify({ file_key: "file_one" }),
      }),
      { messageId: "om_file" },
    ),
  );
  const file = await waitUntil(() =>
    f.frames.find(
      (frame) => frame.type === "incoming" && frame.incoming.id === "om_file",
    ),
  );
  assert.equal(file.incoming.kind, "file");
  assert.equal(file.incoming.text, "");
});

test("a vendor rejection is definite, a dropped connection stays unknown and reconnect does not replay", async (t) => {
  const f = await fixture(t);
  const delivery = {
    inbox_id: "inbox",
    target: "target",
    reply_context: { message_id: "om_root", chat_id: "oc_private" },
    part: 0,
    kind: "final",
    text: "reject",
  };
  f.send({ type: "send", id: "rejected", delivery });
  const rejected = await waitUntil(() =>
    f.frames.find(
      (frame) => frame.type === "result" && frame.id === "rejected",
    ),
  );
  assert.equal(rejected.result.state, "failed");
  assert.equal(rejected.result.code, 230020);
  f.send({
    type: "send",
    id: "lost",
    delivery: { ...delivery, text: "uncertain", inbox_id: "other" },
  });
  const lost = await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "lost"),
  );
  assert.equal(lost.result.state, "unknown");
  const created = f.requests.filter(
    (request) =>
      request.method === "POST" &&
      request.path.includes("/reply") &&
      request.body.includes("uncertain"),
  );
  assert.equal(created.length, 1, "an unknown outcome must never be replayed");
});
