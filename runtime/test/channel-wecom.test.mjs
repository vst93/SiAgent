import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createRequire } from "node:module";
import { createServer } from "node:http";
import { mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { createInterface } from "node:readline";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";

const require = createRequire(import.meta.url);
const sdkRequire = createRequire(require.resolve("@wecom/aibot-node-sdk"));
const { WebSocketServer } = sdkRequire("ws");
const workerURL = new URL("../dist/channel-wecom.js", import.meta.url).href;
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
async function fixture(t) {
  const server = new WebSocketServer({ host: "127.0.0.1", port: 0 });
  await once(server, "listening");
  // Images arrive as an expiring URL the worker must download itself.
  const media = createServer((request, response) => {
    if (request.url === "/broken.png") {
      response.writeHead(500).end();
      return;
    }
    response.writeHead(200, {
      "content-type": "image/png",
      "content-disposition": `attachment; filename*=UTF-8''${encodeURIComponent("截图.png")}`,
    });
    response.end(pixelPNG);
  });
  media.listen(0, "127.0.0.1");
  await once(media, "listening");
  const mediaURL = `http://127.0.0.1:${media.address().port}/photo.png`;
  const stagingRoot = await mkdtemp(join(tmpdir(), "siagent-channel-media-"));
  const packets = [],
    sockets = [],
    frames = [];
  server.on("connection", (socket) => {
    sockets.push(socket);
    socket.on("message", (data) => {
      const frame = JSON.parse(data.toString());
      packets.push(frame);
      if (frame.cmd === "aibot_subscribe") {
        assert.equal(frame.body.bot_id, "fixture-bot");
        assert.equal(frame.body.secret, "fixture-private-secret");
        socket.send(
          JSON.stringify({ headers: frame.headers, errcode: 0, errmsg: "ok" }),
        );
      } else if (frame.cmd === "aibot_respond_msg") {
        if (frame.body.stream.content === "uncertain") {
          socket.terminate();
          return;
        }
        socket.send(
          JSON.stringify({
            headers: frame.headers,
            errcode: frame.body.stream.content === "reject" ? 40013 : 0,
            errmsg: "fixture-private-secret must not leak",
          }),
        );
      } else if (frame.cmd === "ping")
        socket.send(JSON.stringify({ headers: frame.headers, errcode: 0 }));
    });
  });
  const process = spawn(
    globalThis.process.execPath,
    [
      "--input-type=module",
      "-e",
      `import { runWorker } from ${JSON.stringify(workerURL)};runWorker(${JSON.stringify(`ws://127.0.0.1:${server.address().port}`)});`,
    ],
    { stdio: ["pipe", "pipe", "pipe"], env: { NODE_ENV: "production" } },
  );
  let stderr = "";
  process.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
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
    for (const socket of server.clients) socket.terminate();
    await new Promise((resolve) => server.close(resolve));
    await new Promise((resolve) => media.close(resolve));
    await rm(stagingRoot, { recursive: true, force: true });
    assert.equal(stderr, "");
  });
  process.stdin.write(
    JSON.stringify({
      v: 1,
      type: "configure",
      config: {
        id: "channel",
        provider: "wecom-bot",
        version: 1,
        config: { bot_id: "fixture-bot" },
        secrets: { secret: "fixture-private-secret" },
        media_dir: stagingRoot,
      },
    }) + "\n",
  );
  await waitUntil(() =>
    frames.find(
      (frame) => frame.type === "status" && frame.state === "connected",
    ),
  );
  return {
    server,
    sockets,
    frames,
    packets,
    stagingRoot,
    mediaURL,
    send: (value) =>
      process.stdin.write(JSON.stringify({ v: 1, ...value }) + "\n"),
  };
}
test("official WeCom SDK authenticates, normalizes isolated text sessions and sends stable stream replies without exposing credentials", async (t) => {
  const f = await fixture(t);
  const callback = (msgid, chattype, chatid) => ({
    cmd: "aibot_msg_callback",
    headers: { req_id: `req-${msgid}` },
    body: {
      msgid,
      aibotid: "fixture-bot",
      chattype,
      chatid,
      from: { userid: "member" },
      msgtype: "text",
      text: { content: "question" },
    },
  });
  const wrongBot = callback("wrong-bot", "single", "");
  wrongBot.body.aibotid = "another-bot";
  f.sockets[0].send(JSON.stringify(wrongBot));
  f.sockets[0].send(JSON.stringify(callback("one", "single", "")));
  f.sockets[0].send(JSON.stringify(callback("two", "group", "group-a")));
  f.sockets[0].send(JSON.stringify(callback("three", "group", "group-b")));
  await waitUntil(
    () => f.frames.filter((frame) => frame.type === "incoming").length === 3,
  );
  const incoming = f.frames
    .filter((frame) => frame.type === "incoming")
    .map((frame) => frame.incoming);
  assert.equal(new Set(incoming.map((message) => message.session)).size, 3);
  const { received_at, ...routing } = incoming[1].reply_context;
  assert(Number(received_at) > 0);
  assert.deepEqual(routing, {
    req_id: "req-two",
    chat_id: "group-a",
    sender: "member",
  });
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
    ["hint", { ...delivery, kind: "hint", text: "working" }],
  ]) {
    f.send({ type: "send", id, delivery: frame });
    await waitUntil(() =>
      f.frames.find((result) => result.type === "result" && result.id === id),
    );
  }
  f.send({
    type: "send",
    id: "final",
    delivery: { ...delivery, kind: "final", text: "answer" },
  });
  await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "final"),
  );
  const replies = f.packets.filter(
    (frame) => frame.cmd === "aibot_respond_msg",
  );
  // Only the final answer finishes the stream; ack, draft and notice all update
  // the same message.
  assert.equal(replies.length, 4);
  assert.equal(replies[0].headers.req_id, "req-one");
  assert.equal(replies[0].body.stream.content, "已收到");
  assert.equal(replies[0].body.stream.finish, false);
  assert.equal(replies[1].body.stream.content, "部分回答");
  assert.equal(replies[1].body.stream.finish, false);
  assert.equal(replies[2].body.stream.finish, false);
  assert.equal(replies[3].body.stream.finish, true);
  assert.equal(new Set(replies.map((reply) => reply.body.stream.id)).size, 1);
  f.send({
    type: "send",
    id: "late-final",
    delivery: {
      ...delivery,
      kind: "final",
      text: "late answer",
      reply_context: {
        ...delivery.reply_context,
        received_at: String(Date.now() - 11 * 60 * 1000),
      },
    },
  });
  await waitUntil(() =>
    f.frames.find(
      (frame) => frame.type === "result" && frame.id === "late-final",
    ),
  );
  const lateReply = f.packets
    .filter((frame) => frame.cmd === "aibot_respond_msg")
    .at(-1);
  assert.notEqual(lateReply.body.stream.id, replies[0].body.stream.id);
  assert.match(lateReply.body.stream.id, /-r1$/);
  assert.equal(lateReply.body.stream.finish, true);
  // A late draft starts the same replacement message as the late final, so the
  // answer still finishes the message the member is reading.
  f.send({
    type: "send",
    id: "late-draft",
    delivery: {
      ...delivery,
      kind: "draft",
      text: "late progress",
      reply_context: {
        ...delivery.reply_context,
        received_at: String(Date.now() - 10 * 60 * 1000),
      },
    },
  });
  await waitUntil(() =>
    f.frames.find(
      (frame) => frame.type === "result" && frame.id === "late-draft",
    ),
  );
  const lateDraft = f.packets
    .filter((frame) => frame.cmd === "aibot_respond_msg")
    .at(-1);
  assert.equal(lateDraft.body.stream.id, lateReply.body.stream.id);
  assert.equal(lateDraft.body.stream.finish, false);
  // WeCom accepts at most 20480 bytes per stream update; the platform reserves
  // headroom, and the worker rejects anything beyond the declared limit.
  f.send({
    type: "send",
    id: "too-large",
    delivery: { ...delivery, kind: "final", text: "x".repeat(20001) },
  });
  const oversized = await waitUntil(() =>
    f.frames.find(
      (frame) => frame.type === "result" && frame.id === "too-large",
    ),
  );
  assert.equal(oversized.result.state, "failed");
  assert(!JSON.stringify(f.frames).includes("fixture-private-secret"));
});
test("official SDK rejection is definite, a disconnected send remains unknown and reconnect does not replay business messages", async (t) => {
  const f = await fixture(t);
  const delivery = {
    inbox_id: "inbox",
    reply_context: { req_id: "req-reply" },
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
  assert.equal(rejected.result.code, 40013);
  assert(!rejected.result.message.includes("fixture-private-secret"));
  f.send({
    type: "send",
    id: "lost",
    delivery: { ...delivery, text: "uncertain" },
  });
  const lost = await waitUntil(() =>
    f.frames.find((frame) => frame.type === "result" && frame.id === "lost"),
  );
  assert.equal(lost.result.state, "unknown");
  await waitUntil(() => f.sockets.length === 2);
  await waitUntil(
    () =>
      f.frames.filter(
        (frame) => frame.type === "status" && frame.state === "connected",
      ).length === 2,
  );
  assert.equal(
    f.packets.filter(
      (frame) =>
        frame.cmd === "aibot_respond_msg" &&
        frame.body.stream.content === "uncertain",
    ).length,
    1,
  );
});

test("inbound images are downloaded, decrypted and staged for the platform instead of travelling inside a frame", async (t) => {
  const f = await fixture(t);
  const callback = (msgid, body) => ({
    cmd: "aibot_msg_callback",
    headers: { req_id: `req-${msgid}` },
    body: {
      msgid,
      aibotid: "fixture-bot",
      chattype: "single",
      from: { userid: "member" },
      ...body,
    },
  });
  // A picture-only message.
  f.sockets[0].send(
    JSON.stringify(
      callback("image-only", {
        msgtype: "image",
        image: { url: f.mediaURL, aeskey: undefined },
      }),
    ),
  );
  // A text + image message (WeCom "mixed").
  f.sockets[0].send(
    JSON.stringify(
      callback("mixed", {
        msgtype: "mixed",
        mixed: {
          msg_item: [
            { msgtype: "text", text: { content: "这张图说了什么？" } },
            { msgtype: "image", image: { url: f.mediaURL } },
          ],
        },
      }),
    ),
  );
  // A picture whose download fails must still produce an inbound frame.
  f.sockets[0].send(
    JSON.stringify(
      callback("broken", {
        msgtype: "image",
        image: { url: f.mediaURL.replace("photo.png", "broken.png") },
      }),
    ),
  );
  await waitUntil(
    () => f.frames.filter((frame) => frame.type === "incoming").length === 3,
  );
  const byID = Object.fromEntries(
    f.frames
      .filter((frame) => frame.type === "incoming")
      .map((frame) => [frame.incoming.id, frame.incoming]),
  );
  // The frame carries a staged file name, never the image bytes.
  const imageOnly = byID["image-only"];
  assert.equal(imageOnly.kind, "image");
  assert.equal(imageOnly.text, "");
  assert.equal(imageOnly.images.length, 1);
  assert.equal(imageOnly.images[0].file.includes("/"), false);
  assert.equal(imageOnly.images[0].data, undefined);
  assert.match(imageOnly.images[0].name, /截图\.png$/);
  const staged = await readFile(join(f.stagingRoot, imageOnly.images[0].file));
  assert.deepEqual(staged, pixelPNG);

  const mixed = byID["mixed"];
  assert.equal(mixed.kind, "image");
  // The text part survives flattening so the picture is not asked about blindly.
  assert.match(mixed.text, /这张图说了什么/);
  assert.equal(mixed.images.length, 1);

  // A failed download degrades to text instead of losing the message.
  const broken = byID["broken"];
  assert.equal(broken.kind, "text");
  assert.equal(broken.images, undefined);
  assert.equal(broken.text, "");

  // Voice messages keep their transcript, which is the useful content.
  f.sockets[0].send(
    JSON.stringify(
      callback("voice", {
        msgtype: "voice",
        voice: { content: "语音转写内容" },
      }),
    ),
  );
  await waitUntil(
    () => f.frames.filter((frame) => frame.type === "incoming").length === 4,
  );
  const voice = f.frames
    .filter((frame) => frame.type === "incoming")
    .find((frame) => frame.incoming.id === "voice").incoming;
  assert.equal(voice.text, "语音转写内容");
});
