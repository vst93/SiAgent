import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { createServer } from "node:http";
import { createInterface } from "node:readline";
import { once } from "node:events";

const require = createRequire(import.meta.url);
const sdkRequire = createRequire(require.resolve("ws"));
const { WebSocketServer } = sdkRequire("ws");
const workerURL = new URL("../dist/plugins/qqbot/worker.js", import.meta.url).href;

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
        reject(new Error("qqbot fixture timed out"));
      }
    }, 10);
  });
}

// The worker talks to the official token/gateway/OpenAPI endpoints; the test
// redirects them to a local server and a local gateway socket.
async function fixture(t) {
  const frames = [];
  const requests = [];
  let wsServer;
  let mode = "ok";
  const api = createServer((request, response) => {
    let body = "";
    request.on("data", (chunk) => (body += chunk));
    request.on("end", () => {
      const path = request.url ?? "";
      requests.push({ path, method: request.method, body, authorization: request.headers.authorization });
      if (path.startsWith("/app/getAppAccessToken")) {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(JSON.stringify({ access_token: "fixture-token", expires_in: "7200" }));
        return;
      }
      if (path.startsWith("/gateway/bot")) {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(
          JSON.stringify({ url: `ws://127.0.0.1:${wsServer.address().port}/` }),
        );
        return;
      }
      if (path.startsWith("/v2/")) {
        if (body.includes("uncertain")) {
          response.destroy();
          return;
        }
        if (mode === "reject") {
          response.writeHead(400, { "content-type": "application/json" });
          response.end(JSON.stringify({ code: 40034, message: "invalid message" }));
          return;
        }
        if (mode === "rate") {
          response.writeHead(429, { "content-type": "application/json" });
          response.end(JSON.stringify({ code: 42900, message: "rate limited" }));
          return;
        }
        response.writeHead(200, { "content-type": "application/json" });
        response.end(JSON.stringify({ id: "qq-message", timestamp: new Date().toISOString() }));
        return;
      }
      response.writeHead(404, { "content-type": "application/json" });
      response.end(JSON.stringify({ code: 404, message: "unknown" }));
    });
  });
  api.listen(0, "127.0.0.1");
  await once(api, "listening");

  wsServer = new WebSocketServer({ host: "127.0.0.1", port: 0 });
  await once(wsServer, "listening");
  const sockets = [];
  let sequence = 0;
  wsServer.on("connection", (socket) => {
    sockets.push(socket);
    socket.send(JSON.stringify({ op: 10, d: { heartbeat_interval: 30000 } }));
    socket.on("message", (raw) => {
      const frame = JSON.parse(raw.toString());
      if (frame.op === 2) {
        assert.equal(frame.d.token, "QQBot fixture-token");
        assert.equal(frame.d.intents, (1 << 25) | (1 << 26));
        sequence += 1;
        socket.send(
          JSON.stringify({
            op: 0,
            s: sequence,
            t: "READY",
            d: { session_id: "fixture-session", user: { id: "bot" } },
          }),
        );
      } else if (frame.op === 1) {
        socket.send(JSON.stringify({ op: 11 }));
      }
    });
  });

  const process = spawn(
    globalThis.process.execPath,
    [
      "--input-type=module",
      "-e",
      `import { runWorker } from ${JSON.stringify(workerURL)};runWorker({tokenURL:${JSON.stringify(
        `http://127.0.0.1:${api.address().port}/app/getAppAccessToken`,
      )},apiBase:${JSON.stringify(`http://127.0.0.1:${api.address().port}`)}});`,
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
    assert.equal(stderr, "");
  });
  process.stdin.write(
    JSON.stringify({
      v: 1,
      type: "configure",
      config: {
        id: "channel",
        provider: "qqbot",
        version: 1,
        config: { app_id: "1020304050", environment: "production" },
        secrets: { app_secret: "fixture-secret" },
      },
    }) + "\n",
  );
  await waitUntil(() =>
    frames.find((frame) => frame.type === "status" && frame.state === "connected"),
  );
  return {
    frames,
    requests,
    sockets,
    send: (value) => process.stdin.write(JSON.stringify({ v: 1, ...value }) + "\n"),
    dispatch: (event) => {
      const socket = sockets.at(-1);
      socket.send(JSON.stringify({ op: 0, s: ++sequence, ...event }));
    },
    setMode: (value) => (mode = value),
  };
}

test("official QQ Bot gateway authenticates, normalizes group and private messages and replies with msg_seq", async (t) => {
  const f = await fixture(t);
  f.dispatch({
    t: "GROUP_AT_MESSAGE_CREATE",
    d: {
      id: "group-event-1",
      content: "<@!bot> 群里提问",
      group_openid: "group-openid",
      author: { member_openid: "member-openid" },
      timestamp: new Date().toISOString(),
    },
  });
  f.dispatch({
    t: "C2C_MESSAGE_CREATE",
    d: {
      id: "c2c-event-1",
      content: "私聊提问",
      author: { user_openid: "user-openid" },
      timestamp: new Date().toISOString(),
    },
  });
  await waitUntil(
    () => f.frames.filter((frame) => frame.type === "incoming").length === 2,
  );
  const messages = f.frames
    .filter((frame) => frame.type === "incoming")
    .map((frame) => frame.incoming);
  const group = messages.find((message) => message.chat_type === "group");
  const single = messages.find((message) => message.chat_type === "single");
  assert.equal(group.group_id, "group-openid");
  assert.equal(group.sender_id, "member-openid");
  assert.equal(group.text, "群里提问");
  assert.equal(single.sender_id, "user-openid");
  assert.equal(single.group_id, "");
  assert.notEqual(group.session, single.session);
  assert.ok(Number(group.reply_context.received_at) > 0);

  const delivery = {
    id: "delivery",
    inbox_id: "inbox",
    target: group.session,
    reply_context: group.reply_context,
    part: 0,
    kind: "final",
    text: "最终回答",
  };
  f.send({ type: "send", id: "final", delivery });
  const result = await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "final"),
  );
  assert.equal(result.result.state, "sent");
  const sent = f.requests.find((request) => request.path.startsWith("/v2/groups/"));
  assert.ok(sent, "group reply must use the group endpoint");
  assert.equal(sent.authorization, "QQBot fixture-token");
  const body = JSON.parse(sent.body);
  assert.equal(body.content, "最终回答");
  assert.equal(body.msg_id, "group-event-1");
  assert.equal(body.msg_seq, 1);
  // A second delivery for the same event must increase msg_seq, not repeat it.
  f.send({ type: "send", id: "second", delivery: { ...delivery, id: "second", text: "第二部分" } });
  await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "second"),
  );
  const bodies = f.requests
    .filter((request) => request.path.startsWith("/v2/groups/"))
    .map((request) => JSON.parse(request.body));
  assert.equal(bodies.at(-1).msg_seq, 2);
  assert(!JSON.stringify(f.frames).includes("fixture-secret"));
});

test("QQ failures are classified and an uncertain send is never replayed", async (t) => {
  const f = await fixture(t);
  const base = {
    inbox_id: "inbox",
    target: "single",
    reply_context: { msg_id: "event", chat_type: "single", sender: "user-openid" },
    part: 0,
    kind: "final",
  };
  f.setMode("reject");
  f.send({ type: "send", id: "rejected", delivery: { ...base, text: "reject" } });
  const rejected = await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "rejected"),
  );
  assert.equal(rejected.result.state, "failed");
  f.setMode("rate");
  f.send({ type: "send", id: "limited", delivery: { ...base, text: "limited" } });
  const limited = await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "limited"),
  );
  assert.equal(limited.result.state, "retry");
  f.setMode("ok");
  f.send({ type: "send", id: "lost", delivery: { ...base, text: "uncertain" } });
  const lost = await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "lost"),
  );
  assert.equal(lost.result.state, "unknown");
  const attempts = f.requests.filter(
    (request) => request.path.startsWith("/v2/users/") && request.body.includes("uncertain"),
  );
  assert.equal(attempts.length, 1, "an uncertain outcome must never be replayed");
  f.send({
    type: "send",
    id: "too-large",
    delivery: { ...base, text: "x".repeat(2001) },
  });
  const oversized = await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "too-large"),
  );
  assert.equal(oversized.result.state, "failed");
});
