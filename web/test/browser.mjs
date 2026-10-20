import { createCipheriv, createHash, randomBytes } from "node:crypto";
import { chromium } from "playwright";
import { createServer } from "node:http";
import { spawn } from "node:child_process";
import { appendFileSync } from "node:fs";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
const root = fileURLToPath(new URL("../../", import.meta.url));
const artifacts =
  process.env.SIAGENT_BROWSER_ARTIFACTS ??
  join(tmpdir(), "siagent-browser-artifacts");
await mkdir(artifacts, { recursive: true });
const messageText = (content) => typeof content === "string" ? content : content.filter((part) => part.type === "text").at(-1)?.text ?? "";
const modelInputs = [];
const modelRequests = [];
const modelListRequests = [];
let releaseHold;
let releaseChannelHold;
const channelHeld = new Promise((resolve) => { releaseChannelHold = resolve; });
const held = new Promise((resolve) => {
  releaseHold = resolve;
});
const model = createServer(async (req, res) => {
  if (req.method === "GET" && req.url === "/v1/models") {
    modelListRequests.push({ authorization: req.headers.authorization });
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify({
        data: [
          { id: "browser-fixture" },
          { id: "another-fixture" },
          { id: "browser-fixture" },
        ],
      }),
    );
    return;
  }

  let raw = "";
  for await (const chunk of req) raw += chunk;
  const body = JSON.parse(raw);
  modelRequests.push({ body, authorization: req.headers.authorization });
  if (!body.stream) {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ choices: [{ message: { content: "OK" } }] }));
    return;
  }
  res.writeHead(200, { "Content-Type": "text/event-stream" });
  const send = (delta, finish_reason = null) =>
    res.write(
      `data: ${JSON.stringify({ id: "browser", object: "chat.completion.chunk", created: 1, model: "fixture", choices: [{ index: 0, delta, finish_reason }] })}\n\n`,
    );
  const question = messageText(body.messages.at(-1).content);
  modelInputs.push(question);
  if (question === "browser knowledge") {
    send({ role: "assistant", content: "" });
    send({
      tool_calls: [
        {
          index: 0,
          id: "browser-knowledge-call",
          type: "function",
          function: {
            name: "knowledge_search",
            arguments: JSON.stringify({ query: "发票开具流程" }),
          },
        },
      ],
    });
    send({}, "tool_calls");
    res.end("data: [DONE]\n\n");
    return;
  }
  if (question === "browser MCP") {
    send({ role: "assistant", content: "" });
    send({
      tool_calls: [
        {
          index: 0,
          id: "browser-mcp-call",
          type: "function",
          function: {
            name: body.tools.find((tool) =>
              tool.function.name.startsWith("mcp_"),
            ).function.name,
            arguments: "{}",
          },
        },
      ],
    });
    send({}, "tool_calls");
    res.end("data: [DONE]\n\n");
    return;
  }
  send({ role: "assistant", content: "" });
  if (question === "browser markdown") {
    send({
      content:
        '## Markdown 渲染示例\n\n这是 **重点** 与 `inline code`。\n\n- 第一项\n- 第二项\n\n> 引用内容\n\n| 模块 | 状态 | 说明 |\n| --- | --- | --- |\n| 知识库 | 就绪 | 可检索 |\n\n```javascript\nconst message = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx";\nconsole.log(message);\n```\n\n[安全链接](https://example.com) [危险链接](javascript:alert(1))\n\n![远程图片](https://example.invalid/image.png)\n\n<img src=x onerror=window.markdownPwned=true>\n<script>window.markdownPwned=true</script>',
    });
    send({}, "stop");
    res.end("data: [DONE]\n\n");
    return;
  }
  if (question === "browser channel hold") {
    send({ content: "渠道处理中…" });
    await channelHeld;
  }
  if (question === "browser hold") {
    send({ content: "正在处理…" });
    await held;
  }
  send({ content: "收到：" + question });
  send({}, "stop");
  res.end("data: [DONE]\n\n");
});
model.listen(0, "127.0.0.1");
await once(model, "listening");
const directory = await mkdtemp(join(tmpdir(), "siagent-browser-"));
const adapterDirectory = join(directory, "channel-adapters", "browser-fixture");
await mkdir(adapterDirectory, { recursive: true });
await writeFile(join(adapterDirectory, "adapter.json"), JSON.stringify({
  v: 1, entry: "worker.mjs", provider: {
    id: "zz-browser-channel", name: "Browser channel fixture", transport: "websocket", max_text_bytes: 1024,
    identity_fields: ["account"], fields: [{ key: "account", label: "Account", required: true }],
  },
}));
await writeFile(join(adapterDirectory, "worker.mjs"), String.raw`import {createInterface} from 'node:readline';
const output = frame => process.stdout.write(JSON.stringify({v:1,...frame})+'\n');
const lines = createInterface({input:process.stdin});
lines.on('line',line=>{const frame=JSON.parse(line);if(frame.type==='configure'){output({type:'status',state:'connected'});output({type:'incoming',incoming:{id:'browser-channel-message',session:'remote-member',kind:'text',text:'browser channel hold'}});}if(frame.type==='send')output({type:'result',id:frame.id,result:{state:'sent'}});});
lines.on('close',()=>process.stdin.destroy());`);
const skillDirectory = await mkdtemp(join(tmpdir(), "siagent-browser-skill-"));
await writeFile(
  join(skillDirectory, "SKILL.md"),
  "---\nname: browser-skill\ndescription: Browser fixture skill.\ncompatibility: Inspect the supplied requirements file.\n---\nUse explicit tools only. <img src=x onerror=window.pwned=true>\n",
);
await writeFile(
  join(skillDirectory, "requirements.txt"),
  "No dependencies installed automatically.\n",
);
await mkdir(join(skillDirectory, "references"));
await writeFile(
  join(skillDirectory, "references", "guide.md"),
  "Nested reference from the uploaded skill.\n",
);
const portProbe = createServer();
portProbe.listen(0, "127.0.0.1");
await once(portProbe, "listening");
const port = portProbe.address().port;
await new Promise((resolve) => portProbe.close(resolve));
const base = `http://127.0.0.1:${port}`;
const child = spawn(
  "./bin/siagent",
  ["-data-dir", directory, "-listen", `127.0.0.1:${port}`],
  {
    cwd: root,
    env: Object.fromEntries(
      Object.entries(process.env).filter(
        ([key]) =>
          !key.startsWith("SIAGENT_") &&
          ![
            "INVOCATION_ID",
            "NOTIFY_SOCKET",
            "SUPERVISOR_ENABLED",
            "CONTAINER",
          ].includes(key.toUpperCase()),
      ),
    ),
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let setupToken = "";
child.stderr.on("data", (chunk) => {
  const text = String(chunk);
  if (process.env.SIAGENT_BROWSER_SERVER_LOG)
    appendFileSync(process.env.SIAGENT_BROWSER_SERVER_LOG, text);
  const match = text.match(/令牌.*?: (\S+)/);
  if (match) setupToken = match[1];
});
child.on("exit", (code, signal) => {
  if (process.env.SIAGENT_BROWSER_SERVER_LOG)
    appendFileSync(
      process.env.SIAGENT_BROWSER_SERVER_LOG,
      `\nSERVER EXIT code=${code} signal=${signal}\n`,
    );
});
let browser;
try {
  for (let i = 0; i < 100; i++) {
    try {
      const response = await fetch(base + "/health/ready");
      if (response.ok) break;
    } catch {}
    await new Promise((r) => setTimeout(r, 50));
  }
  browser = await chromium.launch({
    executablePath: process.env.SIAGENT_CHROMIUM,
    headless: true,
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => {
    errors.push(error.message);
    console.error(error.stack);
  });
  page.on("console", (message) => {
    if (
      message.type() === "error" &&
      message.text().includes("Content Security Policy")
    )
      errors.push(message.text());
  });
  const config = async (name) => {
    const modules = page.getByRole("navigation", { name: "主导航" });
    if (await modules.isVisible()) {
      // The rail scrolls; reset it so the target stops moving before clicking.
      await modules.evaluate((nav) => {
        nav.scrollTop = 0;
      });
      await modules.getByRole("button", { name, exact: true }).click();
    } else {
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
      await page
        .getByRole("navigation", { name: "移动导航" })
        .getByRole("button", { name, exact: true })
        .click();
    }
    await page
      .locator(".configuration-heading")
      .getByRole("heading", { name, exact: true })
      .waitFor();
  };
  const chat = async () => {
    const modules = page.getByRole("navigation", { name: "主导航" });
    if (await modules.isVisible()) {
      await modules.getByRole("button", { name: "Agent 对话", exact: true }).click();
    } else {
      await page.getByRole("button", { name: "打开导航", exact: true }).click();
      await page
        .getByRole("navigation", { name: "移动导航" })
        .getByRole("button", { name: "Agent 对话", exact: true })
        .click();
    }
    await page.getByLabel("发送给助手的消息").waitFor();
  };
  const agentTab = async (agent, tab) => {
    await config("Agent");
    await page
      .locator(".agent-card")
      .filter({ hasText: agent })
      .locator(".agent-card-select")
      .click();
    await page
      .getByRole("navigation", { name: "Agent 配置" })
      .getByRole("button", { name: tab, exact: true })
      .click();
  };
  await page.goto(base);
  const icon = await page.locator('link[rel="icon"]').getAttribute("href");
  assert.equal(icon, "./favicon.svg");
  const favicon = await page.request.get(new URL(icon, base + "/").href);
  assert.equal(favicon.status(), 200);
  assert((await favicon.text()).includes("<svg"));
  await page.getByLabel("首次启动令牌").fill(setupToken);
  await page.getByLabel("管理员密码").fill("browser-long-test-password");
  await page.getByRole("button", { name: "设置密码并继续" }).click();
  await page
    .getByLabel("服务地址")
    .fill(`http://127.0.0.1:${model.address().port}/v1`);
  const providerSelect = page.getByLabel("模型供应商", { exact: true });
  await providerSelect.waitFor();
  assert(
    (await providerSelect.locator("option", { hasText: "DeepSeek" }).first().innerText()).includes("需启用扩展"),
    "official providers should be locked until the pack is enabled",
  );
  await page
    .getByRole("button", { name: "启用供应商扩展", exact: true })
    .first()
    .click();
  await page.getByText("已启用", { exact: true }).waitFor();
  assert(
    !(await providerSelect.locator("option", { hasText: "DeepSeek" }).first().innerText()).includes("需启用扩展"),
    "official providers should unlock after enabling the pack",
  );
  await page.getByLabel("API 密钥").fill("browser-key");
  await page.getByRole("button", { name: "获取模型列表", exact: true }).click();
  await page.getByText("已获取 2 个模型", { exact: true }).waitFor();
  assert.equal(
    await page.evaluate(
      async () => (await (await fetch("/api/models")).json()).length,
    ),
    0,
  );
  await page
    .getByLabel("模型名称", { exact: true })
    .selectOption("browser-fixture");
  await page.getByRole("button", { name: "手动填写", exact: true }).click();
  await page.getByLabel("模型名称", { exact: true }).fill("custom-model-id");
  await page.getByRole("button", { name: "从列表选择", exact: true }).click();
  assert.equal(
    await page.getByLabel("模型名称", { exact: true }).inputValue(),
    "custom-model-id",
  );
  await page
    .getByLabel("模型名称", { exact: true })
    .selectOption("browser-fixture");
  assert.equal(modelListRequests.at(-1).authorization, "Bearer browser-key");

  await page.getByRole("button", { name: "保存并开始对话" }).click();
  await page
    .getByLabel("发送给助手的消息")
    .fill("你好，请帮我整理今天的工作。");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "收到：你好" })
    .waitFor();
  await page.getByText("已完成", { exact: true }).waitFor();
  assert.equal(await page.locator(".run-feedback").count(), 0);
  await page.screenshot({
    path: join(artifacts, "desktop.png"),
    fullPage: true,
  });
  let remoteImageRequests = 0;
  page.on("request", (request) => {
    if (request.url().startsWith("https://example.invalid/"))
      remoteImageRequests++;
  });
  await page.getByLabel("发送给助手的消息").fill("browser markdown");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  const rendered = page.locator(".message.assistant .message-markdown").filter({
    has: page.getByRole("heading", {
      name: "Markdown 渲染示例",
      exact: true,
    }),
  });
  await rendered.locator("pre code.language-javascript").waitFor();
  assert.equal(await rendered.locator("ul li").count(), 2);
  assert.equal(await rendered.locator("table tbody tr").count(), 1);
  assert.equal(await rendered.locator("strong").innerText(), "重点");
  assert.equal(await rendered.locator("blockquote").innerText(), "引用内容");
  assert.equal(
    await rendered.locator("img,script,a[href^='javascript:']").count(),
    0,
  );
  assert.equal(await page.evaluate(() => window.markdownPwned), undefined);
  assert.equal(remoteImageRequests, 0);
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 1000 });
    assert(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    await page.screenshot({
      path: join(artifacts, `markdown-${width}.png`),
      fullPage: true,
    });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByLabel("发送给助手的消息").fill("保留跨模块草稿");
  await config("模型连接");
  const beforeDiscovery = await page.evaluate(
    async () => await (await fetch("/api/models")).json(),
  );
  await page.getByRole("button", { name: "获取模型列表", exact: true }).click();
  await page.getByText("已获取 2 个模型", { exact: true }).waitFor();
  assert.equal(modelListRequests.at(-1).authorization, "Bearer browser-key");
  assert.deepEqual(
    await page.evaluate(async () => await (await fetch("/api/models")).json()),
    beforeDiscovery,
  );
  await page.getByLabel("API 密钥", { exact: true }).fill("changed-editor-key");
  assert.equal(
    await page
      .getByLabel("模型名称", { exact: true })
      .evaluate((element) => element.tagName),
    "INPUT",
  );
  await page.getByLabel("API 密钥", { exact: true }).fill("");
  assert.equal(await page.getByLabel("角色指令").count(), 0);
  await chat();
  assert.equal(
    await page.getByLabel("发送给助手的消息").inputValue(),
    "保留跨模块草稿",
  );
  await page.getByLabel("发送给助手的消息").fill("");
  await config("模型连接");
  await page
    .getByRole("button", { name: "将当前页设为默认页", exact: true })
    .click();
  await config("知识库");
  await page.goto(base);
  await page
    .locator(".configuration-heading h1")
    .filter({ hasText: "模型连接" })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "当前页已是默认页", exact: true })
      .getAttribute("aria-pressed"),
    "true",
  );
  await page.goto(base + "#/settings/system");
  await page
    .locator(".configuration-heading h1")
    .filter({ hasText: "运行与维护" })
    .waitFor();
  await config("Agent");
  await config("知识库");
  await page.goBack();
  await page
    .locator(".configuration-heading h1")
    .filter({ hasText: /^Agent$/ })
    .waitFor();
  await chat();
  await page
    .getByRole("button", { name: "将当前页设为默认页", exact: true })
    .click();
  await config("模型连接");
  await page.goto(base);
  await page.getByLabel("发送给助手的消息").waitFor();
  await config("模型连接");
  const other = await page.context().newPage();
  other.on("pageerror", (error) => errors.push(error.message));
  await other.goto(base + "#/settings/knowledge");
  await other
    .getByRole("button", { name: "将当前页设为默认页", exact: true })
    .click();
  assert.match(
    (await page
      .getByRole("button", { name: "将当前页设为默认页", exact: true })
      .getAttribute("title")) ?? "",
    /配置 · 知识库/,
  );
  await other.goto(base);
  await other
    .locator(".configuration-heading h1")
    .filter({ hasText: "知识库" })
    .waitFor();
  await other.goto(base + "#/settings/agents");
  await other.getByRole("link", { name: "打开默认页面", exact: true }).click();
  await other
    .locator(".configuration-heading h1")
    .filter({ hasText: "知识库" })
    .waitFor();
  await other.evaluate(() =>
    localStorage.setItem("siagent.default-page", "settings/invalid"),
  );
  await other.goto(base + "#/unknown-page");
  await other.getByLabel("发送给助手的消息").waitFor();
  await other.addInitScript(() => {
    const original = Storage.prototype.setItem;
    Storage.prototype.setItem = function (key, value) {
      if (key === "siagent.default-page")
        throw new DOMException("blocked", "SecurityError");
      return original.call(this, key, value);
    };
  });
  await other.goto(base + "#/settings/system");
  await other.reload();
  await other
    .getByRole("button", { name: "将当前页设为默认页", exact: true })
    .click();
  await other
    .getByRole("alert")
    .filter({ hasText: "浏览器未允许保存默认页面" })
    .waitFor();
  await other
    .locator(".configuration-heading h1")
    .filter({ hasText: "运行与维护" })
    .waitFor();
  await other.close();
  await page.getByRole("button", { name: "测试模型连接", exact: true }).click();
  await page.getByText("连接成功，模型已返回文本响应。").waitFor();
  assert.equal(await page.getByLabel("API 密钥").inputValue(), "");
  await config("扩展");
  await page.getByRole("heading", { name: "已安装", exact: true }).waitFor();
  await page.getByRole("heading", { name: "随系统分发", exact: true }).waitFor();
  await page.getByRole("heading", { name: "最近事件", exact: true }).waitFor();
  await config("集成接口");
  await page.getByRole("heading", { name: "创建密钥", exact: true }).waitFor();
  await page.getByLabel("名称", { exact: true }).fill("browser-key");
  await page.getByRole("button", { name: "创建密钥", exact: true }).click();
  await page.locator(".apikey-token code").waitFor();
  const issued = await page.locator(".apikey-token code").innerText();
  assert(issued.startsWith("siagent_"), "token should be shown once");
  await page.getByRole("button", { name: "查看", exact: true }).first().click();
  await page.locator(".apikey-revealed").waitFor();
  assert(
    (await page.locator(".apikey-revealed").first().innerText()).startsWith(
      "siagent_",
    ),
    "revealed key should be the full token",
  );
  await page.getByRole("button", { name: "隐藏", exact: true }).click();
  await page.getByRole("button", { name: "吊销", exact: true }).first().click();
  await page.getByText("密钥已吊销。", { exact: false }).waitFor();
  await page.getByRole("button", { name: "删除", exact: true }).first().click();
  await page.getByText("密钥已删除。", { exact: false }).waitFor();
  await config("接口文档");
  await page.getByRole("heading", { name: "SiAgent 集成接口", exact: true }).waitFor();
  await page
    .getByRole("link", { name: /完整接口说明/ })
    .waitFor();
  await config("运行与维护");
  // The maintenance page groups status, storage, updates and both limit forms
  // into labeled panels instead of one flat column.
  for (const name of ["运行状态", "存储", "应用更新", "运行限制", "知识库上传限制"]) {
    await page.getByRole("heading", { name, exact: true }).waitFor();
  }
  assert.equal(await page.locator(".settings-page .panel").count(), 5);
  await page.getByRole("heading", { name: "应用更新", exact: true }).waitFor();
  const updateState = await page.evaluate(() =>
    fetch("/api/updates").then((response) => response.json()),
  );
  assert.equal(updateState.supported, false);
  assert(["source", "docker"].includes(updateState.mode));
  assert.equal(
    await page
      .getByRole("button", { name: "更新并自动重启", exact: true })
      .count(),
    0,
  );
  // A never-checked instance must not show a stale "latest version" beside the
  // current one; only an available update reveals it.
  const fresh = await page.evaluate(() =>
    fetch("/api/updates").then((response) => response.json()),
  );
  if (!fresh.available) {
    assert.equal(
      await page.getByText("最新版本", { exact: true }).count(),
      0,
      "latest version must stay hidden before a check finds an update",
    );
  }
  // Knowledge upload limits live on this page and apply without a restart.
  await page.getByRole("heading", { name: "知识库上传限制", exact: true }).waitFor();
  const fileLimit = page.getByLabel("单文件上限（MiB）", { exact: true });
  assert.equal(await fileLimit.inputValue(), "20");
  await fileLimit.fill("25");
  await page.getByRole("button", { name: "保存上传限制", exact: true }).click();
  await page.getByText("上传限制已保存", { exact: false }).waitFor();
  assert.equal(
    await page.evaluate(() =>
      fetch("/api/knowledge/status")
        .then((response) => response.json())
        .then((status) => status.max_file_bytes),
    ),
    26214400,
  );
  await page.getByRole("button", { name: "恢复默认", exact: true }).click();
  await page.getByText("已恢复默认上传限制", { exact: false }).waitFor();
  assert.equal(await fileLimit.inputValue(), "20");
  await page.screenshot({
    path: join(artifacts, "updates-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 320, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({
    path: join(artifacts, "updates-mobile.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  let updatePhase = "ready";
  const updateFixture = () => ({
    mode: "installer",
    supported: true,
    current: "v0.0.1",
    latest: "v0.0.2",
    available: true,
    phase: updatePhase,
    automatic: updatePhase !== "ready",
    message: {
      ready: "下载与校验已完成。",
      downloading: "正在下载并校验发布包。",
      waiting: "正在等待现有任务结束，完成后自动重启。",
      restarting: "正在停机、备份和重启，请等待自动重新连接。",
    }[updatePhase],
  });
  await page.route("**/api/updates", (route) =>
    route.fulfill({ json: updateFixture() }),
  );
  await page.route("**/api/updates/start", async (route) => {
    assert.equal(route.request().postDataJSON().confirm, true);
    updatePhase = "downloading";
    await route.fulfill({ status: 202, json: updateFixture() });
  });
  const installUpdate = page.getByRole("button", {
    name: "更新并自动重启",
    exact: true,
  });
  await installUpdate.waitFor();
  assert.equal(await installUpdate.isEnabled(), true);
  await page.screenshot({
    path: join(artifacts, "updates-ready-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 320, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({
    path: join(artifacts, "updates-ready-mobile.png"),
    fullPage: true,
  });
  await installUpdate.click();
  await page.getByRole("status").filter({ hasText: "正在下载并校验发布包" }).waitFor();
  assert.equal(await page.locator(".update-spinner").count(), 1);
  assert.equal(await installUpdate.isDisabled(), true);
  updatePhase = "waiting";
  await page.getByRole("status").filter({ hasText: "正在等待现有任务结束" }).waitFor();
  assert.equal(await page.getByRole("button", { name: "取消本次更新", exact: true }).isEnabled(), true);
  updatePhase = "restarting";
  await page
    .getByRole("status")
    .filter({ hasText: "正在停机、备份和重启" })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "检查新版本", exact: true })
      .isDisabled(),
    true,
  );
  await page.unroute("**/api/updates");
  await page.unroute("**/api/updates/start");
  await page.setViewportSize({ width: 1440, height: 1000 });
  await config("模型连接");
  await config("运行与维护");
  assert.equal(
    await page.getByLabel("上下文保守上限（字节）", { exact: true }).count(),
    0,
  );
  // Each limit states its unit and what it bounds.
  assert.match(
    await page.locator(".run-limits-grid > label").first().innerText(),
    /同时执行的任务数（个任务）/,
  );
  assert.match(
    await page.locator(".run-limits-grid > label").first().innerText(),
    /每个任务都占用内存与模型并发额度/,
  );
  // The host recommendation is advisory only: it is shown, can be applied with
  // one click, and a value above it is still accepted.
  const capacity = await page.locator(".capacity-hint").innerText();
  assert.match(capacity, /本机规格/);
  assert.match(capacity, /不是强制上限/);
  assert.match(capacity, /个任务，超出部分会排队等待而不是被拒绝/);
  await page.getByRole("button", { name: "使用推荐值", exact: true }).click();
  const reported = await page.evaluate(() =>
    fetch("/api/limits").then((response) => response.json()),
  );
  assert.equal(
    Number(await page.getByLabel("同时执行的任务数", { exact: true }).inputValue()),
    reported.capacity.recommended.concurrent,
  );
  await page
    .getByLabel("同时执行的任务数", { exact: true })
    .fill(String(reported.capacity.runtime_active + 5));
  await page.getByRole("button", { name: "保存运行限制" }).click();
  await page.getByText("限制已保存。", { exact: false }).waitFor();
  assert.equal(
    await page.evaluate(() =>
      fetch("/api/limits")
        .then((response) => response.json())
        .then((data) => data.concurrent),
    ),
    reported.capacity.runtime_active + 5,
    "values above the recommendation must be accepted",
  );
  await page.getByLabel("单次运行时限", { exact: true }).fill("60");
  await page.getByLabel("同时执行的任务数", { exact: true }).fill("2");
  await page.getByRole("button", { name: "保存运行限制" }).click();
  await page.getByText("限制已保存。", { exact: false }).waitFor();
  assert.equal(
    await page.evaluate(() =>
      fetch("/api/limits")
        .then((response) => response.json())
        .then((data) => data.run_timeout_seconds),
    ),
    60,
  );
  await config("插件接入");
  assert.equal(await page.getByLabel("Skill 在服务器上的绝对目录").count(), 0);
  assert.equal(
    await page
      .getByRole("tab", { name: "Skills 技能文件", exact: true })
      .getAttribute("aria-selected"),
    "true",
  );
  await page.getByRole("button", { name: "上传 Skill", exact: false }).click();
  await page.getByLabel("上传方式", { exact: true }).selectOption("folder");
  await page
    .getByLabel("选择技能文件夹", { exact: true })
    .setInputFiles(skillDirectory);
  await page.getByLabel("我已确认该技能来自可信来源").check();
  await page.getByRole("button", { name: "上传并保存", exact: true }).click();
  await page
    .getByText("“browser-skill”已上传，可到 Agent 配置中挂载。", {
      exact: true,
    })
    .waitFor();
  assert.equal(
    await page.evaluate(
      async () =>
        (await (await fetch("/api/agents/default/capabilities")).json()).skills
          .length,
    ),
    0,
  );
  await page.getByRole("button", { name: "上传 Skill", exact: false }).click();
  await page.getByLabel("选择技能包", { exact: true }).setInputFiles({
    name: "SKILL.md",
    mimeType: "text/markdown",
    buffer: Buffer.from(
      "---\nname: disposable-skill\ndescription: Archive flow fixture.\n---\nReference only.\n",
    ),
  });
  await page.getByLabel("我已确认该技能来自可信来源").check();
  await page.getByRole("button", { name: "上传并保存", exact: true }).click();
  await page
    .getByRole("heading", { name: "disposable-skill", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "删除技能", exact: true }).click();
  assert.equal(
    await page.evaluate(() => document.activeElement.textContent),
    "取消",
  );
  await page.keyboard.press("Escape");
  await page
    .getByRole("heading", { name: "disposable-skill", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "删除技能", exact: true }).click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await page
    .getByRole("heading", { name: "browser-skill", exact: true })
    .waitFor();
  await page
    .locator(".skill-markdown-preview")
    .filter({ hasText: "Use explicit tools only." })
    .waitFor();
  assert.equal(await page.locator(".skill-file-preview img").count(), 0);
  await page.getByRole("button", { name: "guide.md", exact: true }).click();
  await page
    .locator(".skill-markdown-preview")
    .filter({ hasText: "Nested reference" })
    .waitFor();
  await page
    .getByRole("button", { name: "requirements.txt", exact: true })
    .click();
  await page
    .locator(".skill-file-preview pre")
    .filter({ hasText: "No dependencies" })
    .waitFor();
  await page.screenshot({
    path: join(artifacts, "skills-desktop.png"),
    fullPage: true,
  });
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    await page.screenshot({
      path: join(artifacts, `skills-${width}.png`),
      fullPage: true,
    });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("tab", { name: "MCP 工具服务", exact: true }).click();
  await page.getByText("添加 MCP 服务", { exact: true }).click();
  await page.getByLabel("MCP 名称", { exact: true }).fill("Browser MCP");
  await page.getByRole("tab", { name: "Skills 技能文件", exact: true }).click();
  assert.equal(
    await page.getByLabel("MCP 名称", { exact: true }).isVisible(),
    false,
  );
  await page
    .locator(".skill-file-preview pre")
    .filter({ hasText: "No dependencies" })
    .waitFor();
  // The redesigned detail pane keeps the file actions usable while editing.
  await page.getByRole("button", { name: "编辑", exact: true }).click();
  await page
    .getByLabel("编辑 requirements.txt", { exact: true })
    .fill("Edited dependency note.\n");
  await page.getByRole("button", { name: "保存文件", exact: true }).click();
  await page
    .locator(".skill-file-preview pre")
    .filter({ hasText: "Edited dependency note." })
    .waitFor();
  await page.getByRole("tab", { name: "MCP 工具服务", exact: true }).click();
  assert.equal(
    await page.getByLabel("MCP 名称", { exact: true }).inputValue(),
    "Browser MCP",
  );

  await page.getByLabel("程序绝对路径", { exact: true }).fill(process.execPath);
  await page
    .getByLabel("参数（JSON 字符串数组）", { exact: true })
    .fill(JSON.stringify([join(root, "runtime/test/mcp-fixture.mjs")]));
  await page
    .getByLabel("环境变量（JSON 对象，留空沿用已保存值）", { exact: true })
    .fill(JSON.stringify({ TOKEN: "browser-mcp-secret" }));
  await page
    .getByLabel(
      "我确认服务及代码可信，并允许连接测试启动本地进程或访问远程地址",
    )
    .check();
  await page
    .getByRole("button", { name: "保存 MCP 配置", exact: true })
    .click();
  await page.getByText("MCP 已保存。", { exact: false }).waitFor();
  await page
    .getByRole("button", { name: "测试连接与刷新工具", exact: true })
    .click();
  await page.getByText("inspect · 服务声明只读", { exact: true }).click();
  await page.screenshot({
    path: join(artifacts, "plugins-desktop.png"),
    fullPage: true,
  });
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth <= innerWidth &&
          document.querySelector(".configuration-content").scrollWidth <=
            document.querySelector(".configuration-content").clientWidth,
      ),
    );
  }
  await page.screenshot({
    path: join(artifacts, "plugins-mobile.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await agentTab("默认助手", "工具授权");
  await page.getByLabel("browser-skill", { exact: true }).check();
  await page.getByText("inspect · 服务声明只读", { exact: true }).click();
  await page.getByLabel("授权默认助手使用 inspect", { exact: true }).check();
  await page
    .getByLabel("我已检查所选内容与权限，仅授予默认助手必要的能力")
    .check();
  await page
    .getByRole("button", { name: "保存助手工具授权", exact: true })
    .click();
  await page
    .getByText("助手授权已保存，新提交的任务将使用这些技能与工具。", {
      exact: true,
    })
    .waitFor();
  const toolConfig = await page.evaluate(() =>
    fetch("/api/mcps").then((response) => response.json()),
  );
  assert(!JSON.stringify(toolConfig).includes("browser-mcp-secret"));
  await page.screenshot({
    path: join(artifacts, "tools-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({
    path: join(artifacts, "tools-mobile.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await chat();
  await page.getByLabel("发送给助手的消息").fill("browser MCP");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "[redacted]" })
    .waitFor();
  const presentedTools = page.locator(".run-tool-output").last();
  await presentedTools.locator("summary").click();
  await presentedTools
    .getByText("Browser MCP / inspect", { exact: true })
    .waitFor();
  assert.equal(
    await presentedTools
      .getByText("查看调用参数与结果", { exact: true })
      .count(),
    0,
  );
  await presentedTools.locator("summary").click();
  await page.locator(".run-diagnostics summary").last().click();
  await page
    .locator(".run-diagnostics")
    .last()
    .getByText("Browser MCP / inspect", { exact: true })
    .waitFor();
  await page.getByText("查看调用参数与结果", { exact: true }).click();
  await page
    .locator(".resource-preview")
    .filter({ hasText: "[redacted]" })
    .last()
    .waitFor();
  assert(
    !(await page.locator("body").innerText()).includes("browser-mcp-secret"),
  );
  await page.locator(".run-diagnostics > summary").last().click();
  await config("知识库");
  await page.getByRole("button", { name: "新建知识库", exact: false }).click();
  await page.getByLabel("知识库名称", { exact: true }).fill("财务资料");
  await page.getByRole("button", { name: "创建知识库", exact: true }).click();
  await page.getByRole("button", { name: "上传文档", exact: true }).click();
  await page.getByLabel("选择文档", { exact: true }).setInputFiles({
    name: "发票手册.md",
    mimeType: "text/markdown",
    buffer: Buffer.from(
      "# 发票开具流程\n\n发票开具流程：提供订单编号，七个工作日内完成。 <img src=x onerror=window.knowledgePwned=true>\n",
    ),
  });
  await page
    .getByRole("button", { name: "上传并建立索引", exact: true })
    .click();
  await page
    .locator(".knowledge-state")
    .filter({ hasText: /^可检索$/ })
    .waitFor();
  // Exercise isolated library state, filters, modal dismissal and deletion.
  const libraryPicker = page.getByLabel("选择知识库", { exact: true });
  const financeLibrary = await libraryPicker.inputValue();
  // A delayed pre-upload list must not discard the newly accepted document.
  let releaseOldList, oldListRequested;
  const oldListHeld = new Promise((resolve) => {
    releaseOldList = resolve;
  });
  const oldListStarted = new Promise((resolve) => {
    oldListRequested = resolve;
  });
  const oldListURL = `**/api/knowledge/bases/${financeLibrary}/documents`;
  let holdListOnce = true;
  await page.route(oldListURL, async (route) => {
    if (!holdListOnce) {
      await route.continue();
      return;
    }
    holdListOnce = false;
    const response = await route.fetch();
    oldListRequested();
    await oldListHeld;
    await route.fulfill({ response });
  });
  await page.getByRole("button", { name: "刷新文档状态", exact: true }).click();
  await oldListStarted;
  await page.getByRole("button", { name: "上传文档", exact: true }).click();
  await page.getByLabel("选择文档", { exact: true }).setInputFiles({
    name: "并发校验.md",
    mimeType: "text/markdown",
    buffer: Buffer.from("独立索引校验资料。"),
  });
  await page
    .getByRole("button", { name: "上传并建立索引", exact: true })
    .click();
  await page
    .locator(".kb-document-name > button")
    .filter({ hasText: "并发校验.md" })
    .waitFor();
  releaseOldList();
  await page
    .getByRole("button", { name: "刷新文档状态", exact: true })
    .waitFor();
  assert.equal(await page.locator(".kb-document-name").count(), 2);
  await page.unroute(oldListURL);
  // The row menu must escape the list card: the last row keeps a clickable,
  // on-screen menu instead of a dropdown clipped by the scroll container.
  const lastMenu = page
    .locator(".kb-document-row:not(.kb-table-heading) .row-menu-trigger")
    .last();
  await lastMenu.scrollIntoViewIfNeeded();
  await lastMenu.click();
  const menuBox = await page.getByRole("menu").boundingBox();
  const viewport = page.viewportSize();
  assert(menuBox && viewport && menuBox.y >= 0 && menuBox.y + menuBox.height <= viewport.height + 1);
  await page.keyboard.press("Escape");
  await page
    .locator(".kb-document-row")
    .filter({ hasText: "并发校验.md" })
    .locator(".row-menu-trigger")
    .click();
  await page.getByRole("menuitem", { name: "删除文档", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "确认删除", exact: true })
    .click();
  await page
    .locator(".kb-document-name > button")
    .filter({ hasText: "并发校验.md" })
    .waitFor({ state: "hidden" });

  // Bulk deletion: multi-select replaces deleting documents one by one.
  for (const name of ["批量一.md", "批量二.md"]) {
    await page.getByRole("button", { name: "上传文档", exact: true }).click();
    await page.getByLabel("选择文档", { exact: true }).setInputFiles({
      name,
      mimeType: "text/markdown",
      buffer: Buffer.from(`${name} 批量删除校验资料。`),
    });
    await page
      .getByRole("button", { name: "上传并建立索引", exact: true })
      .click();
    await page
      .locator(".kb-document-name > button")
      .filter({ hasText: name })
      .waitFor();
  }
  for (const name of ["批量一.md", "批量二.md"]) {
    await page
      .locator(".kb-document-row")
      .filter({ hasText: name })
      .locator('input[type="checkbox"]')
      .check();
  }
  await page.getByText("已选 2 份文档", { exact: false }).waitFor();
  await page.getByRole("button", { name: "删除所选", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "确认删除", exact: true })
    .click();
  for (const name of ["批量一.md", "批量二.md"]) {
    await page
      .locator(".kb-document-name > button")
      .filter({ hasText: name })
      .waitFor({ state: "hidden" });
  }

  await page.getByRole("button", { name: "新建知识库", exact: false }).click();
  await page.getByLabel("知识库名称", { exact: true }).fill("独立测试资料");
  await page.getByRole("button", { name: "创建知识库", exact: true }).click();
  await page
    .getByRole("heading", { name: "还没有文档", exact: true })
    .waitFor();
  const isolatedLibrary = await libraryPicker.inputValue();
  let releaseDocuments, documentsRequested;
  const delayedDocuments = new Promise((resolve) => {
    releaseDocuments = resolve;
  });
  const documentsStarted = new Promise((resolve) => {
    documentsRequested = resolve;
  });
  const documentsURL = `**/api/knowledge/bases/${financeLibrary}/documents`;
  await page.route(documentsURL, async (route) => {
    documentsRequested();
    await delayedDocuments;
    await route.continue();
  });
  await libraryPicker.selectOption(financeLibrary);
  await documentsStarted;
  await libraryPicker.selectOption(isolatedLibrary);
  await page
    .getByRole("heading", { name: "还没有文档", exact: true })
    .waitFor();
  const lateDocuments = page.waitForResponse((response) =>
    response.url().endsWith(`/knowledge/bases/${financeLibrary}/documents`),
  );
  releaseDocuments();
  await lateDocuments;
  await page.unroute(documentsURL);
  assert.equal(await page.locator(".kb-document-name").count(), 0);
  await libraryPicker.selectOption(financeLibrary);
  await page.locator(".knowledge-state.ready").waitFor();
  await page.getByLabel("搜索文档", { exact: true }).fill("不存在的文件");
  await page
    .getByText("没有匹配的文档，请调整名称或状态筛选。", { exact: true })
    .waitFor();
  await page.getByLabel("搜索文档", { exact: true }).fill("");
  await page.getByLabel("筛选文档状态", { exact: true }).selectOption("failed");
  assert.equal(await page.locator(".kb-document-name").count(), 0);
  await page.getByLabel("筛选文档状态", { exact: true }).selectOption("all");
  await page.locator(".kb-document-name > button").first().click();
  await page
    .getByRole("dialog", { name: "文档详情", exact: true })
    .getByText("原始文件", { exact: true })
    .waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "上传文档", exact: true }).click();
  const uploadDialog = page.getByRole("dialog", {
    name: "上传文档",
    exact: true,
  });
  await uploadDialog.waitFor();
  assert(
    await uploadDialog
      .getByRole("button", { name: "上传并建立索引", exact: true })
      .isDisabled(),
  );
  await page.mouse.click(1, 1);
  await uploadDialog.waitFor({ state: "hidden" });
  await libraryPicker.selectOption(isolatedLibrary);
  await page.locator(".kb-library-controls .row-menu-trigger").click();
  await page.getByRole("menuitem", { name: "删除知识库", exact: true }).click();
  assert.equal(
    await page.evaluate(() => document.activeElement.textContent),
    "取消",
  );
  await page.keyboard.press("Escape");
  assert.equal(await libraryPicker.inputValue(), isolatedLibrary);
  await page.locator(".kb-library-controls .row-menu-trigger").click();
  await page.getByRole("menuitem", { name: "删除知识库", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "确认删除", exact: true })
    .click();
  await page.locator(".knowledge-state.ready").waitFor();
  assert.equal(await libraryPicker.inputValue(), financeLibrary);
  await page.screenshot({
    path: join(artifacts, "knowledge-documents-desktop.png"),
    fullPage: true,
  });
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert(
      await page.evaluate(() => {
        const group = document.querySelector(".kb-library-controls");
        const manage = group
          .querySelector(".row-menu-trigger")
          .getBoundingClientRect();
        const create = group
          .querySelector(":scope > button.primary")
          .getBoundingClientRect();
        return (
          Math.abs(manage.top - create.top) < 0.5 &&
          Math.abs(manage.bottom - create.bottom) < 0.5 &&
          create.left - manage.right > 0 &&
          create.left - manage.right <= 10
        );
      }),
      `Knowledge toolbar visible buttons are misaligned at ${width}px`,
    );
    await page.locator(".kb-library-toolbar").screenshot({
      path: join(artifacts, `knowledge-toolbar-${width}.png`),
    });
    assert(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    await page.screenshot({
      path: join(artifacts, `knowledge-documents-${width}.png`),
      fullPage: true,
    });
    await page.getByRole("button", { name: "上传文档", exact: true }).click();
    assert(
      await page.evaluate(() => {
        const b = document.querySelector(".kb-dialog").getBoundingClientRect();
        return b.left >= 0 && b.right <= innerWidth;
      }),
    );
    await page.screenshot({
      path: join(artifacts, `knowledge-upload-${width}.png`),
      fullPage: true,
    });
    await page.keyboard.press("Escape");
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  assert.equal(
    await page
      .getByRole("button", { name: "保存知识库关联", exact: true })
      .count(),
    0,
  );
  await agentTab("默认助手", "知识库关联");
  await page.getByRole("checkbox", { name: "财务资料", exact: true }).check();
  await page
    .getByRole("checkbox", {
      name: "我确认检索片段可发送给当前模型服务",
      exact: true,
    })
    .check();
  await page
    .getByRole("button", { name: "保存知识库关联", exact: true })
    .click();
  await page
    .getByText("知识库关联已保存，新提交的任务将使用这些知识库。", {
      exact: true,
    })
    .waitFor();
  await config("知识库");
  await page.getByRole("tab", { name: "检索测试", exact: true }).click();
  await page.getByLabel("检索问题", { exact: true }).fill("发票开具流程");
  await page
    .getByRole("button", { name: "检索当前知识库", exact: true })
    .click();
  await page.locator(".knowledge-hits li").first().waitFor();
  const traceSummary = page.locator(".kb-trace > summary");
  await traceSummary.waitFor();
  assert.match(await traceSummary.innerText(), /检索过程/);
  await traceSummary.click();
  await page.locator(".kb-trace-stages li").first().waitFor();
  assert(
    (await page.locator(".kb-trace-hits li").count()) > 0,
    "trace should list selected chunks",
  );
  assert.equal(
    await page.getByLabel(/使用模型重排/).count(),
    1,
    "rerank toggle should be available",
  );
  await page.getByRole("tab", { name: "评测", exact: true }).click();
  await page.getByLabel("评测问题", { exact: true }).fill("发票开具流程");
  await page.locator(".kb-eval-docs .checkbox-label").first().click();
  await page.getByRole("button", { name: "添加问题", exact: true }).click();
  await page.locator(".kb-eval-questions li").first().waitFor();
  await page
    .getByRole("button", { name: "运行检索评测", exact: true })
    .click();
  await page.locator(".kb-eval-metrics").waitFor();
  assert(
    (await page.locator(".kb-eval-cases li").count()) > 0,
    "evaluation should report cases",
  );
  await page.locator(".kb-eval-history li").first().waitFor();
  await page.screenshot({
    path: join(artifacts, "knowledge-eval-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: join(artifacts, "knowledge-eval-390.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.getByRole("tab", { name: "检索测试", exact: true }).click();
  await page.getByLabel("检索问题", { exact: true }).fill("完全不存在的问题关键词");
  await page.getByRole("button", { name: "检索当前知识库", exact: true }).click();
  await page.getByText("没有找到依据。", { exact: false }).waitFor();
  await page.getByRole("tab", { name: "内容缺口", exact: true }).click();
  await page.locator(".kb-gap-list li").first().waitFor();
  assert.equal(
    await page.locator(".kb-gap-list li").first().getByText("完全不存在的问题关键词").count(),
    1,
    "empty search should be recorded as a content gap",
  );
  await page.getByRole("button", { name: "标记已解决", exact: true }).first().click();
  await page.getByText("还没有记录到未命中的问题", { exact: false }).waitFor();
  await page.getByRole("tab", { name: "检索测试", exact: true }).click();
  await page.getByLabel("检索问题", { exact: true }).fill("发票开具流程");
  await page.getByRole("button", { name: "检索当前知识库", exact: true }).click();
  await page.locator(".knowledge-hits li").first().waitFor();
  const sourceHref = await page
    .locator(".knowledge-hits a")
    .first()
    .getAttribute("href");
  const sourcePopup = page.waitForEvent("popup");
  await page.locator(".knowledge-hits a").first().click();
  const sourcePage = await sourcePopup;
  await sourcePage
    .locator(".knowledge-original")
    .filter({ hasText: "七个工作日" })
    .waitFor();
  assert.equal(await sourcePage.locator(".knowledge-original img").count(), 0);
  assert.equal(
    await sourcePage.evaluate(() => window.knowledgePwned),
    undefined,
  );
  await sourcePage.close();
  await page.screenshot({
    path: join(artifacts, "knowledge-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(() => {
      const pane = document.querySelector(".knowledge-workspace");
      return (
        document.documentElement.scrollWidth <= innerWidth &&
        pane.scrollWidth <= pane.clientWidth
      );
    }),
  );
  await page.screenshot({
    path: join(artifacts, "knowledge-mobile.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await chat();
  await page.getByLabel("发送给助手的消息").fill("browser knowledge");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "七个工作日" })
    .waitFor();
  await page.locator(".answer-sources a").first().waitFor();
  await config("知识库");
  await page.getByRole("tab", { name: "文档管理", exact: false }).click();
  await page
    .locator(".kb-document-row:not(.kb-table-heading) .row-menu-trigger")
    .first()
    .click();
  await page.getByRole("menuitem", { name: "替换文件", exact: true }).click();
  await page.getByLabel("选择文档", { exact: true }).setInputFiles({
    name: "bad.md",
    mimeType: "text/markdown",
    buffer: Buffer.from([255, 254]),
  });
  await page.getByRole("button", { name: "上传替换版本", exact: true }).click();
  await page
    .locator(".knowledge-state")
    .filter({ hasText: /^处理失败$/ })
    .waitFor();
  await page.getByText("仍使用已发布的 v1。", { exact: false }).waitFor();
  await page.getByRole("tab", { name: "检索测试", exact: true }).click();
  await page.getByLabel("检索问题", { exact: true }).fill("七个工作日");
  await page
    .getByRole("button", { name: "检索当前知识库", exact: true })
    .click();
  await page
    .locator(".knowledge-hits li")
    .filter({ hasText: "七个工作日" })
    .first()
    .waitFor();
  await page.getByRole("tab", { name: "文档管理", exact: false }).click();
  await page
    .locator(".kb-document-row:not(.kb-table-heading) .row-menu-trigger")
    .first()
    .click();
  await page.getByRole("menuitem", { name: "替换文件", exact: true }).click();
  await page.getByLabel("选择文档", { exact: true }).setInputFiles({
    name: "updated.md",
    mimeType: "text/markdown",
    buffer: Buffer.from("发票开具流程：新规定为十个工作日内完成。"),
  });
  await page.getByRole("button", { name: "上传替换版本", exact: true }).click();
  await page
    .locator(".knowledge-state")
    .filter({ hasText: /^可检索$/ })
    .waitFor();
  await page.getByRole("tab", { name: "检索测试", exact: true }).click();
  await page.getByLabel("检索问题", { exact: true }).fill("十个工作日");
  await page
    .getByRole("button", { name: "检索当前知识库", exact: true })
    .click();
  await page
    .locator(".knowledge-hits li")
    .filter({ hasText: "十个工作日" })
    .first()
    .waitFor();
  const historical = await page.context().newPage();
  await historical.goto(base + sourceHref);
  await historical.getByText("历史版本 v1", { exact: false }).waitFor();
  await historical
    .locator(".knowledge-original")
    .filter({ hasText: "七个工作日" })
    .waitFor();
  await historical.close();
  await page.getByRole("tab", { name: "文档管理", exact: false }).click();
  await page
    .locator(".kb-document-row:not(.kb-table-heading) .row-menu-trigger")
    .first()
    .click();
  await page.getByRole("menuitem", { name: "删除文档", exact: true }).click();
  await page
    .getByRole("dialog", { name: "删除文档？" })
    .getByRole("button", { name: "确认删除", exact: true })
    .click();
  await page
    .getByText("文档已删除，不再参与新检索。已有来源链接显示已删除。", {
      exact: true,
    })
    .waitFor();
  await page.getByRole("tab", { name: "检索测试", exact: true }).click();
  await page.getByLabel("检索问题", { exact: true }).fill("发票开具流程");
  await page
    .getByRole("button", { name: "检索当前知识库", exact: true })
    .click();
  await page.getByText("没有找到依据。", { exact: false }).waitFor();
  const deletedSource = await page.context().newPage();
  await deletedSource.goto(base + sourceHref);
  await deletedSource.getByText("来源已删除。", { exact: false }).waitFor();
  await deletedSource.close();
  await chat();
  await page.reload();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "收到：你好" })
    .waitFor();
  await page
    .getByLabel("发送给助手的消息")
    .fill("<img src=x onerror=window.pwned=true>");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "<img" })
    .last()
    .waitFor();
  assert.equal(await page.evaluate(() => window.pwned), undefined);
  assert.equal(await page.locator(".message-text img").count(), 0);
  await page.locator(".run-diagnostics summary").first().click();
  await page.getByText("资源已释放", { exact: true }).waitFor();
  assert((await page.locator(".event-list li").count()) > 0);
  await page.locator(".run-diagnostics summary").first().click();
  await page.getByLabel("发送给助手的消息").fill("browser hold");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "正在处理…" })
    .waitFor();
  await page.getByLabel("发送给助手的消息").fill("queued from browser");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  await page.getByText("排队中", { exact: true }).waitFor();
  await page.getByRole("button", { name: "取消排队", exact: true }).click();
  await page.getByText("已取消", { exact: true }).waitFor();
  // Copying: one message at a time and the whole transcript, both confirmed in
  // place without a page reload or error.
  await page.locator(".chat-stats").waitFor();
  assert.match(await page.locator(".chat-stats").innerText(), /\d+ 轮/);
  await page.locator(".message.assistant").last().hover();
  await page
    .getByRole("button", { name: "复制这条消息" })
    .last()
    .click();
  await page
    .getByRole("button", { name: "已复制这条消息", exact: true })
    .first()
    .waitFor();
  await page.getByRole("button", { name: "复制对话", exact: true }).click();
  await page.getByRole("button", { name: "已复制", exact: true }).waitFor();
  // Collapsed rail: the four footer actions stack in a single centered column
  // with distinct rows, instead of a cramped two-by-two block.
  await page.locator(".rail-collapse").click();
  await page.waitForTimeout(300);
  const railActions = await page.evaluate(() => {
    const actions = document.querySelector(".rail-actions");
    if (!actions) return null;
    const items = [...actions.children].map((el) => {
      const rect = el.getBoundingClientRect();
      return {
        x: Math.round(rect.x),
        y: Math.round(rect.y),
        width: Math.round(rect.width),
        height: Math.round(rect.height),
      };
    });
    return { items, display: getComputedStyle(actions).display };
  });
  assert(railActions && railActions.items.length >= 4, "rail actions missing");
  assert.equal(
    new Set(railActions.items.map((item) => item.x)).size,
    1,
    "collapsed rail actions must share one column",
  );
  assert.equal(
    new Set(railActions.items.map((item) => item.y)).size,
    railActions.items.length,
    "collapsed rail actions must not share a row",
  );
  assert(
    railActions.items.every((item) => item.width >= 24 && item.height >= 24),
    "collapsed rail targets are too small",
  );
  // No horizontal scrollbar in the collapsed rail: the scroll containers clip
  // horizontally, and the navigation content is no wider than its box.
  const railScroll = await page.evaluate(() => {
    const nav = document.querySelector(".rail-nav");
    const rail = document.querySelector(".app-rail");
    return {
      railOverflowX: rail ? getComputedStyle(rail).overflowX : "",
      navOverflowX: nav ? getComputedStyle(nav).overflowX : "",
      navContent: nav ? nav.scrollWidth - nav.clientWidth : 0,
    };
  });
  assert.equal(railScroll.railOverflowX, "hidden");
  assert.equal(railScroll.navOverflowX, "hidden");
  assert(
    railScroll.navContent <= 0,
    `collapsed navigation is wider than its box by ${railScroll.navContent}px`,
  );
  await page.screenshot({
    path: join(artifacts, "rail-collapsed-desktop.png"),
  });
  await page.locator(".rail-collapse").click();
  // Wait for the rail layout to settle before clicking inside it again.
  await page.waitForTimeout(600);
  await config("模型连接");
  releaseHold();
  await chat();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "收到：browser hold" })
    .waitFor();
  assert(!modelInputs.includes("queued from browser"));
  assert.equal(
    modelInputs.filter((value) => value === "browser hold").length,
    1,
  );
  // The vision checkbox sits inside its own labelled section, and its box is
  // vertically centered with the text on one line.
  await config("模型连接");
  const visionGeometry = await page.evaluate(() => {
    const label = [...document.querySelectorAll("label")].find((item) =>
      item.textContent.includes("支持图片输入"),
    );
    if (!label) return null;
    const box = label.querySelector("input").getBoundingClientRect();
    const text = label.getBoundingClientRect();
    const section = label.closest(".model-form-section");
    return {
      inSection: Boolean(section),
      sameRow:
        Math.abs(box.y + box.height / 2 - (text.y + box.height / 2)) <= 3,
      aligned: getComputedStyle(label).alignItems,
    };
  });
  assert(visionGeometry, "vision checkbox missing");
  assert(visionGeometry.inSection, "vision checkbox must sit in its own section");
  assert(
    visionGeometry.sameRow,
    "vision checkbox must be centered with its label text",
  );
  assert.notEqual(
    visionGeometry.aligned,
    "flex-start",
    "single-line checkboxes must not be top aligned",
  );
  await config("渠道接入");
  await page.getByRole("button", { name: "新增渠道", exact: false }).click();
  assert.equal(
    await page.getByLabel("渠道类型", { exact: true }).inputValue(),
    "wecom-bot",
  );
  assert.equal(
    await page.getByLabel("企业 CorpID", { exact: true }).count(),
    0,
  );
  await page.getByLabel("渠道名称", { exact: true }).fill("Browser bot");
  await page.getByLabel("BotID", { exact: true }).fill("browser-bot");
  await page
    .getByLabel("机器人 Secret", { exact: true })
    .fill("browser-bot-private");
  await page.getByRole("button", { name: "保存渠道配置", exact: true }).click();
  await page.waitForTimeout(400);
  // Diagnostic lists are bounded panes: the rule applies whether or not the
  // fixture has messages, so check the computed style of an injected row.
  const recordPane = await page.evaluate(() => {
    const probe = document.createElement("ol");
    probe.className = "channel-record-list";
    const item = document.createElement("li");
    item.textContent = "probe";
    probe.append(item);
    document.querySelector(".channel-detail")?.append(probe);
    const style = getComputedStyle(probe);
    const result = { maxHeight: style.maxHeight, overflow: style.overflowY };
    probe.remove();
    return result;
  });
  assert.notEqual(recordPane.maxHeight, "none", "channel lists must be height-bounded");
  assert.equal(recordPane.overflow, "auto", "channel lists must scroll internally");
  await page
    .getByRole("heading", { name: "Browser bot", exact: true })
    .waitFor();
  assert.equal(await page.getByLabel("回调 URL", { exact: true }).count(), 0);
  await page.getByRole("button", { name: "编辑配置", exact: true }).click();
  assert.equal(
    await page.getByLabel("机器人 Secret", { exact: true }).inputValue(),
    "",
  );
  assert.equal(
    await page.getByLabel("BotID", { exact: true }).isDisabled(),
    true,
  );
  await page.screenshot({
    path: join(artifacts, "channel-bot-editor-desktop.png"),
    fullPage: true,
  });
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    await page.screenshot({
      path: join(artifacts, `channel-bot-editor-${width}.png`),
      fullPage: true,
    });
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  // Close the channel editor first: an open dialog blocks navigation clicks.
  await page
    .locator("dialog.channel-editor, .channel-editor")
    .getByRole("button", { name: "取消", exact: true })
    .click()
    .catch(() => {});
  await config("插件接入");
  // Skill rows follow the card rhythm, and the related-Agent block is a titled
  // panel with pill rows instead of plain inline text.
  const skillRow = page.locator(".skill-list-item").first();
  await skillRow.waitFor();
  const skillRowLayout = await skillRow.evaluate((el) => {
    const style = getComputedStyle(el);
    return {
      display: style.display,
      columns: style.gridTemplateColumns.split(" ").length,
      icon: Boolean(el.querySelector(".skill-list-icon")),
    };
  });
  assert.equal(skillRowLayout.display, "grid");
  assert.equal(skillRowLayout.columns, 3);
  assert(skillRowLayout.icon, "skill rows need a leading icon");
  const linked = page.locator(".linked-agents").first();
  if (await linked.count()) {
    assert.equal(
      await linked.locator(".linked-agents-title").count(),
      1,
      "related-Agent block needs a title row",
    );
    const pill = await linked.locator("li").first().evaluate((el) => {
      const style = getComputedStyle(el);
      return { radius: style.borderRadius, border: style.borderTopWidth };
    });
    assert.match(pill.radius, /px|9999px/);
    assert.notEqual(pill.border, "0px");
  }
  await config("渠道接入");
  const genericChannels = await page.evaluate(() =>
    fetch("/api/channels").then((response) => response.text()),
  );
  assert(!genericChannels.includes("browser-bot-private"));
  await page.getByRole("button", { name: "新增渠道", exact: false }).click();
  await page.getByLabel("渠道类型", { exact: true }).selectOption("wecom-app");
  const channelKey = Buffer.alloc(32, 7);
  const channelAES = channelKey.toString("base64").replace(/=$/, "");
  await page.getByLabel("企业 CorpID", { exact: true }).fill("wx-browser");
  await page.getByLabel("应用 AgentID", { exact: true }).fill("7");
  await page
    .getByLabel("应用 Secret", { exact: true })
    .fill("browser-channel-secret");
  await page.getByLabel("回调 Token", { exact: true }).fill("browserToken");
  await page.getByLabel("EncodingAESKey", { exact: true }).fill(channelAES);
  await page.getByRole("button", { name: "保存渠道配置", exact: true }).click();
  await page.getByText("渠道配置已保存。", { exact: false }).waitFor();
  await page.getByRole("button", { name: "编辑配置", exact: true }).click();
  assert.equal(
    await page.getByLabel("应用 Secret", { exact: true }).inputValue(),
    "",
  );
  assert.equal(
    await page.getByLabel("回调 Token", { exact: true }).inputValue(),
    "",
  );
  assert.equal(
    await page.getByLabel("EncodingAESKey", { exact: true }).inputValue(),
    "",
  );
  await page.getByRole("button", { name: "取消", exact: true }).click();
  const callbackURL = await page
    .getByLabel("回调 URL", { exact: true })
    .inputValue();
  const echo = Buffer.from("browser-callback-echo");
  const length = Buffer.alloc(4);
  length.writeUInt32BE(echo.length);
  let frame = Buffer.concat([
    randomBytes(16),
    length,
    echo,
    Buffer.from("wx-browser"),
  ]);
  const padding = 32 - (frame.length % 32);
  frame = Buffer.concat([frame, Buffer.alloc(padding, padding)]);
  const cipher = createCipheriv(
    "aes-256-cbc",
    channelKey,
    channelKey.subarray(0, 16),
  );
  cipher.setAutoPadding(false);
  const encrypted = Buffer.concat([
    cipher.update(frame),
    cipher.final(),
  ]).toString("base64");
  const timestamp = String(Math.floor(Date.now() / 1000));
  const nonce = "browser-nonce";
  const signature = createHash("sha1")
    .update(["browserToken", timestamp, nonce, encrypted].sort().join(""))
    .digest("hex");
  const callbackResult = await page.evaluate(
    async ({ callbackURL, timestamp, nonce, signature, encrypted }) => {
      const params = new URLSearchParams({
        timestamp,
        nonce,
        msg_signature: signature,
        echostr: encrypted,
      });
      const response = await fetch(callbackURL + "?" + params, {
        credentials: "omit",
      });
      return { status: response.status, text: await response.text() };
    },
    { callbackURL, timestamp, nonce, signature, encrypted },
  );
  assert.deepEqual(callbackResult, {
    status: 200,
    text: "browser-callback-echo",
  });
  await page.getByRole("button", { name: "刷新渠道状态", exact: true }).click();
  await page.getByText("已收到 URL 验证请求。", { exact: false }).waitFor();
  const safeChannels = await page.evaluate(() =>
    fetch("/api/wecom/channels").then((response) => response.text()),
  );
  assert(
    !safeChannels.includes("browser-channel-secret") &&
      !safeChannels.includes("browserToken") &&
      !safeChannels.includes(channelAES),
  );
  await page.screenshot({
    path: join(artifacts, "wecom-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(() => {
      const pane = document.querySelector(".channels-workspace");
      return (
        document.documentElement.scrollWidth <= innerWidth &&
        pane.scrollWidth <= pane.clientWidth
      );
    }),
  );
  await page.screenshot({
    path: join(artifacts, "wecom-mobile.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  const remoteChannel = await page.evaluate(async () => {
    const auth = await fetch("/api/auth/status").then((response) => response.json());
    const response = await fetch("/api/channels", {
      method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": auth.csrf },
      body: JSON.stringify({ name: "浏览器渠道对话", provider: "zz-browser-channel", config: { account: "browser" }, agent_id: "default", enabled: true, confirm: true }),
    });
    if (response.status !== 201) throw new Error(await response.text());
    return response.json();
  });
  await chat();
  await config("渠道接入");
  await page.locator(".channel-catalog button").filter({ hasText: "浏览器渠道对话" }).click();
  await page.getByRole("button", { name: "查看会话", exact: true }).waitFor();
  await page.getByRole("button", { name: "查看会话", exact: true }).click();
  await page
    .locator(".channel-view-label")
    .filter({ hasText: "私聊 · 只读" })
    .waitFor();
  assert.equal(
    await page.locator(".channel-view-label").getAttribute("title"),
    "私聊 · remote-member · 只读",
  );
  assert(
    (await page
      .locator(".chat-header")
      .evaluate((element) => element.getBoundingClientRect().height)) <= 60,
  );
  await page.locator(".message.assistant .message-text").filter({ hasText: "渠道处理中…" }).waitFor();
  assert.equal(await page.getByLabel("发送给助手的消息").count(), 0);
  assert.equal(await page.getByRole("button", { name: "发送", exact: false }).count(), 0);
  assert.equal(await page.locator(".message.user .message-meta strong").textContent(), "remote-member");
  assert.equal(await page.locator(".conversation .channel-tag").textContent(), "私聊 · remote-member");
  assert.match(await page.locator(".message.user time").textContent(), /\d{2}\/\d{2} \d{2}:\d{2}/);
  await page.screenshot({ path: join(artifacts, "channel-chat-desktop.png"), fullPage: true });
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    assert.equal(await page.getByLabel("发送给助手的消息").count(), 0);
    await page.screenshot({ path: join(artifacts, `channel-chat-${width}.png`), fullPage: true });
  }
  await page.getByRole("button", { name: "取消运行", exact: true }).click();
  await page.getByText("已取消", { exact: true }).waitFor();
  releaseChannelHold();
  await page.evaluate(async (channel) => {
    const auth = await fetch("/api/auth/status").then((response) => response.json());
    const response = await fetch(`/api/channels/${channel.id}`, { method: "PUT", headers: { "Content-Type": "application/json", "X-CSRF-Token": auth.csrf }, body: JSON.stringify({ name: channel.name, provider: channel.provider, config: channel.config, agent_id: "default", enabled: false }) });
    if (!response.ok) throw new Error(await response.text());
  }, remoteChannel);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("button", { name: "新对话", exact: true }).click();
  await page.getByLabel("发送给助手的消息").waitFor();
  assert.equal(await page.locator(".channel-view-label").count(), 0);
  await config("模型连接");
  await page.getByRole("button", { name: "新增模型连接", exact: true }).click();
  const modelForm = page.locator(".agents-model-editor");
  await modelForm.locator(".model-advanced > summary").click();
  await modelForm
    .getByLabel("接口协议", { exact: true })
    .selectOption("anthropic-messages");
  assert.equal(
    await modelForm.getByLabel("服务地址", { exact: true }).inputValue(),
    "https://api.anthropic.com",
  );
  assert.equal(await modelForm.getByLabel("Top P", { exact: true }).count(), 0);
  await modelForm.getByLabel("模型支持推理", { exact: true }).check();
  await modelForm.getByLabel("推理强度", { exact: true }).selectOption("high");
  assert(
    await modelForm.getByLabel("Temperature", { exact: true }).isDisabled(),
  );
  await modelForm.getByLabel("模型支持推理", { exact: true }).uncheck();
  await modelForm
    .getByLabel("接口协议", { exact: true })
    .selectOption("openai-responses");
  assert.equal(
    await modelForm.getByLabel("输出长度字段", { exact: true }).count(),
    0,
  );
  await modelForm
    .getByLabel("接口协议", { exact: true })
    .selectOption("openai-completions");

  await modelForm
    .getByLabel("服务地址", { exact: true })
    .fill(`http://127.0.0.1:${model.address().port}/v1`);
  await modelForm.getByLabel("模型名称", { exact: true }).fill("finance-model");
  await modelForm
    .getByLabel("API 密钥", { exact: true })
    .fill("finance-model-secret");
  await modelForm.getByLabel("连接名称", { exact: true }).fill("财务生产连接");
  await modelForm.getByLabel("上下文窗口", { exact: true }).fill("65536");
  await modelForm.getByLabel("最大输出长度", { exact: true }).fill("2048");
  await modelForm.getByLabel("Temperature", { exact: true }).fill("0.25");
  await modelForm.getByLabel("Top P", { exact: true }).fill("0.8");
  await modelForm.getByText("协议兼容设置", { exact: true }).click();
  await modelForm
    .getByLabel("输出长度字段", { exact: true })
    .selectOption("max_tokens");
  await modelForm
    .getByRole("button", { name: "保存模型连接", exact: true })
    .click();
  await page
    .getByText("模型连接已保存，密钥不会回显。", { exact: true })
    .waitFor();
  assert.equal(
    await modelForm.getByLabel("API 密钥", { exact: true }).inputValue(),
    "",
  );
  await modelForm
    .getByRole("button", { name: "测试模型连接", exact: true })
    .click();
  await page
    .getByText("连接成功，模型已返回文本响应。", { exact: true })
    .waitFor();
  await page
    .locator(".configuration-content")
    .evaluate((element) => (element.scrollTop = 0));
  await page.screenshot({
    path: join(artifacts, "models-desktop.png"),
    fullPage: true,
  });
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    assert(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth <= innerWidth &&
          document.querySelector(".configuration-content").scrollWidth <=
            document.querySelector(".configuration-content").clientWidth,
      ),
    );
  }
  await page
    .locator(".configuration-content")
    .evaluate((element) => (element.scrollTop = 0));
  await page.screenshot({
    path: join(artifacts, "models-mobile.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await config("Agent");
  await page.getByRole("button", { name: "新建助手", exact: true }).click();
  await page.getByLabel("助手名称", { exact: true }).fill("财务助手");
  await page.getByLabel("使用模型", { exact: true }).selectOption("2");
  await page
    .getByLabel("角色指令", { exact: true })
    .fill("你是财务助手，使用财务范围的资料。");
  await page.getByRole("button", { name: "创建助手", exact: true }).click();
  await page
    .getByText("助手已保存，新任务使用新配置；已有任务保持提交时的配置。", {
      exact: true,
    })
    .waitFor();
  const policyTab = (name) =>
    page
      .getByRole("navigation", { name: "Agent 配置", exact: true })
      .getByRole("button", { name, exact: true });
  const savePolicy = async () => {
    await page.getByRole("button", { name: "保存助手", exact: true }).click();
    await page
      .getByText("助手已保存，新任务使用新配置；已有任务保持提交时的配置。", {
        exact: true,
      })
      .waitFor();
  };
  await policyTab("会话限制").click();
  await page
    .getByLabel("允许的会话类型", { exact: true })
    .selectOption("group");
  await page
    .getByLabel("群聊上下文范围", { exact: true })
    .selectOption("shared");
  await page
    .getByLabel("空闲多久后开启新会话（分钟）", { exact: true })
    .fill("60");
  await page
    .getByLabel("允许用户通过 /new 开启新会话", { exact: true })
    .uncheck();
  await savePolicy();
  let policyAgents = await page.evaluate(() =>
    fetch("/api/agents").then((response) => response.json()),
  );
  let financePolicy = policyAgents.find((agent) => agent.name === "财务助手");
  assert.deepEqual(financePolicy.policy.session, {
    types: "group",
    group_scope: "shared",
    idle_minutes: 60,
    allow_reset: false,
  });
  await page.screenshot({
    path: join(artifacts, "agent-session-desktop.png"),
    fullPage: true,
  });
  await page
    .locator(".agent-card")
    .filter({ hasText: "财务助手" })
    .getByRole("button", { name: "进入对话", exact: true })
    .click();
  assert(
    await page
      .getByRole("button", { name: "新对话", exact: true })
      .isDisabled(),
  );
  assert(await page.getByLabel("发送给助手的消息").isDisabled());
  await config("Agent");
  await page
    .locator(".agent-card")
    .filter({ hasText: "财务助手" })
    .locator(".agent-card-select")
    .click();
  await policyTab("会话限制").click();
  await page.getByLabel("允许的会话类型", { exact: true }).selectOption("all");
  await savePolicy();
  await policyTab("上下文").click();
  await page.getByLabel("上下文策略", { exact: true }).selectOption("summary");
  await page.getByLabel("输入预算使用阈值（%）", { exact: true }).fill("65");
  await page.getByLabel("保留近期原文（Token）", { exact: true }).fill("2048");
  await savePolicy();
  await page.screenshot({
    path: join(artifacts, "agent-context-desktop.png"),
    fullPage: true,
  });
  await page.getByLabel("上下文策略", { exact: true }).selectOption("time");
  await page.getByLabel("最多保留对话轮数", { exact: true }).fill("5");
  await page.getByLabel("历史时间窗口（分钟）", { exact: true }).fill("60");
  await savePolicy();
  await policyTab("回复输出").click();
  await page.getByLabel("回复预设", { exact: true }).selectOption("detailed");
  assert(
    await page
      .getByLabel("展示模型返回的思考摘要", { exact: true })
      .isChecked(),
  );
  assert.equal(
    await page.getByLabel("工具调用展示", { exact: true }).inputValue(),
    "detailed",
  );
  await page.getByLabel("回复预设", { exact: true }).selectOption("concise");
  assert(
    !(await page
      .getByLabel("渠道排队超过 30 秒或处理超过 90 秒时发送提示", { exact: true })
      .isChecked()),
  );
  // The concise preset keeps the channel acknowledgement and streaming on.
  await page.getByLabel("回复预设", { exact: true }).selectOption("concise");
  assert(
    await page
      .getByLabel("渠道收到消息后立即确认并提示正在作答", { exact: true })
      .isChecked(),
  );
  await page.getByLabel("回复预设", { exact: true }).selectOption("detailed");
  await page
    .getByLabel("思考标签兼容", { exact: true })
    .selectOption("keep");
  await page
    .getByLabel("渠道思考摘要最多字符", { exact: true })
    .fill("800");
  await page
    .getByLabel("渠道收到消息后立即确认并提示正在作答", { exact: true })
    .uncheck();
  await page
    .getByLabel("渠道在作答过程中持续输出已生成的内容", { exact: true })
    .uncheck();
  await savePolicy();
  policyAgents = await page.evaluate(() =>
    fetch("/api/agents").then((response) => response.json()),
  );
  financePolicy = policyAgents.find((agent) => agent.name === "财务助手");
  assert.equal(financePolicy.policy.output.think_tags, "keep");
  assert.equal(financePolicy.policy.output.thinking_max_chars, 800);
  assert.equal(financePolicy.policy.output.acknowledge, false);
  assert.equal(financePolicy.policy.output.stream, false);
  // Environment variable values are encrypted and never returned; only names
  // come back to the editor.
  await policyTab("基本信息").click();
  const envName = page.getByLabel("新环境变量名", { exact: true });
  await envName.fill("API_TOKEN");
  await page
    .getByLabel("新环境变量值", { exact: true })
    .fill("browser-private-token");
  await page
    .locator(".agent-env-add")
    .getByRole("button", { name: "添加", exact: true })
    .click();
  await savePolicy();
  const envAgents = await page.evaluate(() =>
    fetch("/api/agents").then((response) => response.json()),
  );
  financePolicy = envAgents.find((agent) => agent.name === "财务助手");
  const agentId = financePolicy.id;
  assert.deepEqual(financePolicy.env_keys, ["API_TOKEN"]);
  assert(!JSON.stringify(envAgents).includes("browser-private-token"));
  assert(
    await page
      .getByLabel("API_TOKEN 的值", { exact: true })
      .evaluate((input) => input.value === ""),
  );
  // Stored values need a password confirmation and are only shown inside the
  // short reveal window.
  assert.equal(
    await page.evaluate(
      (id) =>
        fetch(`/api/agents/${id}/environment`).then(
          (response) => response.status,
        ),
      agentId,
    ),
    403,
  );
  const revealTrigger = page.getByRole("button", {
    name: "验证后查看已保存的值",
    exact: true,
  });
  await revealTrigger.waitFor();
  await revealTrigger.click();
  await page.getByLabel("管理员密码", { exact: true }).fill("wrong-password");
  await page.getByRole("button", { name: "验证并查看", exact: true }).click();
  // A wrong password must not sign the administrator out (that is reserved for
  // an expired session) and must keep the editor usable.
  await page.getByText("密码不正确", { exact: false }).waitFor();
  await page.getByLabel("管理员密码", { exact: true }).fill("browser-long-test-password");
  await page.getByRole("button", { name: "验证并查看", exact: true }).click();
  await page.getByText("已临时解锁", { exact: false }).waitFor();
  assert(
    await page
      .getByLabel("API_TOKEN 的值", { exact: true })
      .evaluate(
        (input) => input.value === "browser-private-token" && input.type === "text",
      ),
    "the revealed value must be shown in plain text",
  );
  // Restoring the balanced preset returns the channel defaults, including the
  // immediate acknowledgement and streamed drafts.
  await policyTab("回复输出").click();
  await page.getByLabel("回复预设", { exact: true }).selectOption("balanced");
  await savePolicy();
  policyAgents = await page.evaluate(() =>
    fetch("/api/agents").then((response) => response.json()),
  );
  financePolicy = policyAgents.find((agent) => agent.name === "财务助手");
  assert.equal(financePolicy.policy.context.strategy, "time");
  assert.equal(financePolicy.policy.context.max_turns, 5);
  assert.equal(financePolicy.policy.context.max_age_minutes, 60);
  assert.equal(financePolicy.policy.context.summary_threshold, 65);
  assert.equal(financePolicy.policy.output.tools, "summary");
  assert.equal(financePolicy.policy.output.acknowledge, true);
  assert.equal(financePolicy.policy.output.stream, true);
  assert.equal(financePolicy.policy.output.think_tags, "strip");
  assert.equal(financePolicy.policy.output.thinking_max_chars, 1200);
  for (const width of [320, 390]) {
    await page.setViewportSize({ width, height: 844 });
    for (const label of ["基本信息", "会话限制", "上下文", "回复输出"]) {
      await policyTab(label).click();
      await page.locator(".agent-editor").evaluate((editor) => {
        const pane = document.querySelector(".configuration-content");
        pane.scrollTop +=
          editor.getBoundingClientRect().top -
          pane.getBoundingClientRect().top -
          8;
      });
      assert(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth <= innerWidth &&
            document.querySelector(".configuration-content").scrollWidth <=
              document.querySelector(".configuration-content").clientWidth,
        ),
        `Agent policy overflow: ${label}/${width}`,
      );
      await page.screenshot({
        path: join(artifacts, `agent-policy-${label}-${width}.png`),
        fullPage: true,
      });
    }
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  await policyTab("基本信息").click();
  assert.equal(
    await page
      .getByLabel("允许此 Agent 使用本地工作区工具", { exact: false })
      .count(),
    1,
    "advanced tools toggle should exist",
  );
  await page.screenshot({
    path: join(artifacts, "agents-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(() => {
      const pane = document.querySelector(".agents-workspace");
      return (
        document.documentElement.scrollWidth <= innerWidth &&
        pane.scrollWidth <= pane.clientWidth
      );
    }),
  );
  await page.screenshot({
    path: join(artifacts, "agents-mobile.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page
    .locator(".agent-card")
    .filter({ hasText: "财务助手" })
    .getByRole("button", { name: "进入对话", exact: true })
    .click();
  assert.equal(await page.locator(".conversation-list button").count(), 0);
  await config("Agent");
  assert.equal(
    await page
      .locator(".agent-card-select")
      .first()
      .evaluate((element) => getComputedStyle(element).alignItems),
    "flex-start",
  );
  await page.locator(".agent-card").filter({ hasText: "财务助手" }).locator(".agent-card-select").click();
  assert.equal(await page.getByLabel("时区", { exact: true }).inputValue(), "Asia/Shanghai");
  await page.getByLabel("时区", { exact: true }).fill("America/New_York");
  await page.getByRole("button", { name: "保存助手", exact: true }).click();
  await page.getByText("助手已保存，新任务使用新配置", { exact: false }).waitFor();
  await page.locator(".agent-card").filter({ hasText: "财务助手" }).getByRole("button", { name: "进入对话", exact: true }).click();
  await page.getByLabel("发送给助手的消息").fill("finance isolated question");
  await page.getByRole("button", { name: "发送", exact: false }).click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "收到：finance isolated question" })
    .waitFor();
  const financeRequest = modelRequests.find(
    (call) => messageText(call.body.messages.at(-1).content) === "finance isolated question",
  );
  assert.equal(financeRequest.body.model, "finance-model");
  assert.match(financeRequest.body.messages.at(-1).content[0].text, /America\/New_York/);
  assert(!JSON.stringify(financeRequest.body.messages.filter((message) => message.role === "system")).includes("平台时间上下文"));
  assert.equal(financeRequest.body.max_tokens, 2048);
  assert.equal(financeRequest.body.temperature, 0.25);
  assert.equal(financeRequest.body.top_p, 0.8);

  assert.equal(financeRequest.authorization, "Bearer finance-model-secret");
  assert(!financeRequest.body.tools?.length);
  assert(
    !JSON.stringify(financeRequest.body.messages).includes("browser hold"),
  );
  await agentTab("财务助手", "知识库关联");
  await page.getByText("财务助手可检索的知识库", { exact: true }).waitFor();
  assert.equal(
    await page
      .getByRole("checkbox", { name: "财务资料", exact: true })
      .isChecked(),
    false,
  );
  await chat();
  await page.reload();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "收到：finance isolated question" })
    .waitFor();
  assert.equal(
    await page
      .getByRole("combobox", { name: "选择助手", exact: true })
      .locator("option:checked")
      .textContent(),
    "财务助手",
  );
  await config("Agent");
  await page
    .locator(".agent-card")
    .filter({ hasText: "财务助手" })
    .locator(".agent-card-select")
    .click();
  await page
    .getByRole("checkbox", { name: "允许接收新任务", exact: true })
    .uncheck();
  await page.getByRole("button", { name: "保存助手", exact: true }).click();
  await page
    .getByText("助手已保存，新任务使用新配置；已有任务保持提交时的配置。", {
      exact: true,
    })
    .waitFor();
  await page.locator(".agent-archive summary").click();
  await page.getByRole("button", { name: "确认归档助手", exact: true }).click();
  await page.getByText("助手已归档，历史仍可查看。", { exact: true }).waitFor();
  await page
    .locator(".agent-card")
    .filter({ hasText: "财务助手" })
    .getByRole("button", { name: "查看历史", exact: true })
    .click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "收到：finance isolated question" })
    .waitFor();
  assert(
    await page.getByRole("button", { name: "发送", exact: false }).isDisabled(),
  );
  await page
    .getByRole("combobox", { name: "选择助手", exact: true })
    .selectOption("default");
  await page.locator(".conversation-list .conversation").filter({ hasText: "你好，请帮我整理今天的工作。" }).click();
  await page
    .locator(".message.assistant .message-text")
    .filter({ hasText: "收到：你好" })
    .waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: join(artifacts, "mobile.png"),
    fullPage: true,
  });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.getByRole("button", { name: "删除对话", exact: true }).click();
  await page.getByRole("dialog", { name: "删除此对话？" }).waitFor();
  assert(
    await page
      .getByRole("button", { name: "确认删除会话", exact: true })
      .isDisabled(),
  );
  const modalBounds = await page.getByRole("dialog").boundingBox();
  await page.mouse.click(modalBounds.x + 5, modalBounds.y + 5);
  assert(await page.getByRole("dialog").isVisible());
  await page.screenshot({
    path: join(artifacts, "delete-dialog-mobile.png"),
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  assert.equal(await page.getByRole("dialog").count(), 0);
  await page.getByRole("button", { name: "删除对话", exact: true }).click();
  await page.mouse.click(4, 4);
  assert.equal(await page.getByRole("dialog").count(), 0);
  await page.getByRole("button", { name: "删除对话", exact: true }).click();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  assert.equal(await page.getByRole("dialog").count(), 0);
  await page.getByRole("button", { name: "删除对话", exact: true }).click();
  await page
    .getByLabel("我已核对历史，确认永久删除此会话", { exact: true })
    .check();
  await page.getByRole("button", { name: "确认删除会话", exact: true }).click();
  await page
    .getByText("会话已移出列表，清理请求已保存。", { exact: false })
    .waitFor();
  const storage = await page.evaluate(() =>
    fetch("/api/storage").then((response) => response.json()),
  );
  assert(
    storage.disk_total_bytes > 0 && storage.data_bytes > 0 && storage.files > 0,
  );
  // Every configuration page must fit its content pane at desktop widths.
  await page.setViewportSize({ width: 1440, height: 1000 });
  for (const name of [
    "Agent",
    "知识库",
    "模型连接",
    "插件接入",
    "扩展",
    "渠道接入",
    "集成接口",
    "接口文档",
    "运行与维护",
  ]) {
    await config(name);
    const overflow = await page.evaluate(() => {
      const content = document.querySelector(".configuration-content");
      return content ? content.scrollWidth - content.clientWidth : 0;
    });
    assert(overflow <= 1, `${name} overflows its content pane by ${overflow}px`);
  }
  await page
    .getByRole("navigation", { name: "主导航" })
    .getByRole("button", { name: "Agent 对话", exact: true })
    .click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(async () => {
    const state = await fetch("/api/auth/status").then((response) =>
      response.json(),
    );
    await fetch("/api/auth/logout", {
      method: "POST",
      headers: { "X-CSRF-Token": state.csrf },
    });
  });
  await page.locator(".new-chat").click();
  await page.getByRole("heading", { name: "欢迎回来" }).waitFor();
  await page.getByLabel("管理员密码").fill("browser-long-test-password");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await page.getByRole("button", { name: "打开导航", exact: true }).waitFor();
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  assert(
    await page
      .locator(".mobile-menu")
      .getByRole("button", { name: "退出登录", exact: true })
      .isVisible(),
  );
  await page
    .locator(".mobile-menu")
    .getByRole("button", { name: "关闭导航", exact: true })
    .click();
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  await page
    .getByRole("button", { name: "Switch to English", exact: true })
    .click();
  await page
    .locator(".mobile-menu")
    .getByRole("button", { name: "Agent chat", exact: true })
    .waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.lang), "en");
  assert.equal(
    await page.evaluate(() => localStorage.getItem("siagent.language")),
    "en",
  );
  await page.getByRole("button", { name: "Close navigation", exact: true }).click();
  await page.reload();
  await page.getByRole("button", { name: "Open navigation", exact: true }).waitFor();
  assert.equal(await page.evaluate(() => document.documentElement.lang), "en");
  await page.getByRole("button", { name: "Open navigation", exact: true }).click();
  await page
    .locator(".mobile-menu")
    .getByRole("button", { name: "Agent chat", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "Switch to Chinese", exact: true })
    .click();
  await page
    .locator(".mobile-menu")
    .getByRole("button", { name: "Agent 对话", exact: true })
    .waitFor();
  assert.equal(
    await page.evaluate(() => document.documentElement.lang),
    "zh-CN",
  );
  assert.equal(
    await page.evaluate(() => localStorage.getItem("siagent.language")),
    "zh",
  );
  await page.getByRole("button", { name: "关闭导航", exact: true }).click();
  // Command palette navigates without leaving the keyboard.
  await page.locator(".mobile-command-button").click();
  await page.locator(".command-input-row input").fill("知识");
  await page.keyboard.press("Enter");
  await page
    .locator(".configuration-heading")
    .getByRole("heading", { name: "知识库", exact: true })
    .waitFor();
  await page.keyboard.press("Control+K");
  await page.locator(".command-panel").waitFor();
  await page.keyboard.press("Escape");
  await page.locator(".command-panel").waitFor({ state: "detached" });
  // Theme cycles light -> dark -> auto; automatic mode resolves from the clock
  // and the choice survives a reload.
  const themeButton = page.locator(".mobile-topbar .theme-toggle");
  await themeButton.click();
  assert.equal(
    await page.evaluate(() => document.documentElement.dataset.theme),
    "dark",
  );
  assert.equal(await themeButton.getAttribute("data-mode"), "dark");
  await page.reload();
  await page.waitForTimeout(300);
  assert.equal(
    await page.evaluate(() => document.documentElement.dataset.theme),
    "dark",
  );
  assert.equal(
    await page.locator(".mobile-topbar .theme-toggle").getAttribute("data-mode"),
    "dark",
  );
  await page.locator(".mobile-topbar .theme-toggle").click();
  assert.equal(
    await page.locator(".mobile-topbar .theme-toggle").getAttribute("data-mode"),
    "auto",
  );
  const resolved = await page.evaluate(() => {
    const hour = new Date().getHours();
    const expected = hour >= 7 && hour < 19 ? "light" : "dark";
    return {
      actual: document.documentElement.dataset.theme,
      expected,
      stored: localStorage.getItem("siagent.theme"),
    };
  });
  assert.equal(resolved.stored, "auto");
  assert.equal(
    resolved.actual,
    resolved.expected,
    "automatic mode must follow the local clock",
  );
  await page.reload();
  await page.waitForTimeout(400);
  assert.equal(
    await page.locator(".mobile-topbar .theme-toggle").getAttribute("data-mode"),
    "auto",
  );
  assert.equal(await page.evaluate(() => localStorage.getItem("siagent.theme")), "auto");
  await page.locator(".mobile-topbar .theme-toggle").click();
  assert.equal(
    await page.evaluate(() => document.documentElement.dataset.theme),
    "light",
  );
  assert.equal(await page.evaluate(() => localStorage.getItem("siagent.theme")), "light");
  assert.deepEqual(errors, []);
  console.log(
    "Browser passed: Agent default/custom timezone and per-turn model time context, real channel conversation without composer on desktop/320px/390px with cancellation and return to local chat, dated message history, safe Markdown headings/lists/tables/code/links with no automatic remote images and 320px/390px layouts, live unsaved model discovery/dropdown/manual entry/saved-key reuse without persistence, favicon, structured model configuration, protocol-specific controls, context/output/sampling wire parameters, modal deletion with cancel/Escape, two workspaces, draft and active-run continuity, pinned exact default page, deep links and history, cross-tab preference sync, invalid/stored-denied preference fallback, resource management separate from Agent authorization, 320px/390px configuration layouts, explicit UI logout, first setup, model save, chat, connection test, masked key, refresh history, safe text rendering, mobile width, CSP, queue submission/cancel, runtime limits, diagnostics, managed Skill folder/single-file upload, nested resources and archive, Skills/MCP tabs and draft preservation, MCP connection/permission/tool call, credential redaction, desktop/mobile tool settings, native knowledge library selector/tabs/filtering/isolated delayed responses/upload and confirmation dialogs/320px and 390px layouts/index/search/binding/Agent tools/source viewing, failed/successful document replacement, historical/deleted references, Agent session restrictions/shared group scope, context strategies/output presets, policy persistence and 320px/390px policy layouts, multiple model/Agent creation and isolated tool/context scope, Agent selection/refresh/archive/history, generic channel adapters, WeCom bot configuration and masked secrets, legacy app/independent Node crypto callback verification/desktop and mobile, explicit conversation deletion and authenticated disk usage, Chinese/English switch with reload persistence, no JS errors.",
  );
} finally {
  releaseHold();
  releaseChannelHold();
  // A crashed browser or a server that ignores SIGTERM must not hang the run.
  const bounded = (work, ms) =>
    Promise.race([
      work.catch(() => {}),
      new Promise((resolve) => setTimeout(resolve, ms)),
    ]);
  await bounded(browser?.close(), 15000);
  const exited = once(child, "close");
  child.kill("SIGTERM");
  await bounded(exited, 15000);
  model.closeAllConnections();
  await bounded(new Promise((resolve) => model.close(resolve)), 5000);
  await rm(directory, { recursive: true, force: true });
  await rm(skillDirectory, { recursive: true, force: true });
}
