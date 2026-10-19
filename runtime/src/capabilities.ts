import { fork, type ChildProcess } from "node:child_process";
import { mkdir, readFile, realpath } from "node:fs/promises";
import { join, resolve, sep } from "node:path";
import { createHash } from "node:crypto";
import { once } from "node:events";
import { fileURLToPath } from "node:url";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import {
  ReadBuffer,
  serializeMessage,
} from "@modelcontextprotocol/sdk/shared/stdio.js";
import type { Transport } from "@modelcontextprotocol/sdk/shared/transport.js";
import {
  ErrorCode,
  McpError,
  type JSONRPCMessage,
} from "@modelcontextprotocol/sdk/types.js";
import { Type } from "@earendil-works/pi-ai";
import {
  defineExtension,
  defineTool,
  type ToolRegistration,
} from "@earendil-works/pi-durable";

export type MCPTool = {
  name: string;
  description: string;
  input_schema: Record<string, unknown>;
  read_only: boolean;
};
export type MCPConfig = {
  id: string;
  name: string;
  transport: "stdio" | "http";
  command: string;
  args: string[];
  url: string;
  tools: MCPTool[];
};
export type MCPSecret = {
  env: Record<string, string>;
  headers: Record<string, string>;
};
export type Skill = {
  id: string;
  name: string;
  description: string;
  content: string;
  files: { path: string; size: number }[];
};
export type Capabilities = { skills: Skill[]; mcps: MCPConfig[] };

// The SDK's stdio transport inherits host variables. This thin Transport uses its
// framing/parser but passes exactly the administrator's environment, without a shell.
class ExactEnvironmentTransport implements Transport {
  onclose?: () => void;
  onerror?: (error: Error) => void;
  onmessage?: Transport["onmessage"];
  private child?: ChildProcess;
  constructor(
    private config: MCPConfig,
    private env: Record<string, string>,
    private cwd: string,
  ) {}
  async start() {
    const child = fork(
      fileURLToPath(new URL("./mcp-worker.js", import.meta.url)),
      [],
      {
        cwd: this.cwd,
        env: {},
        execArgv: [],
        detached: process.platform !== "win32",
        stdio: ["pipe", "pipe", "ignore", "ipc"],
      },
    );
    this.child = child;
    const buffer = new ReadBuffer({ maxBufferSize: 2 * 1024 * 1024 });
    child.on("error", () => this.onerror?.(new Error("mcp_process_failed")));
    child.on("close", () => this.onclose?.());
    child.stdin!.on("error", () =>
      this.onerror?.(new Error("mcp_pipe_failed")),
    );
    child.stdout!.on("data", (chunk: Buffer) => {
      try {
        buffer.append(chunk);
        let message;
        while ((message = buffer.readMessage())) this.onmessage?.(message);
      } catch {
        this.onerror?.(new Error("mcp_invalid_message"));
        void this.close();
      }
    });
    await once(child, "spawn");
    child.send({
      command: this.config.command,
      args: this.config.args,
      env: this.env,
    });
  }
  async send(message: JSONRPCMessage) {
    const child = this.child;
    if (!child || child.exitCode !== null || child.signalCode !== null)
      throw new Error("mcp_closed");
    await new Promise<void>((resolve, reject) =>
      child.stdin!.write(serializeMessage(message), (error) =>
        error ? reject(error) : resolve(),
      ),
    );
  }
  async close() {
    const child = this.child;
    if (!child) return;
    this.child = undefined;
    const signal = (name: NodeJS.Signals) => {
      try {
        if (process.platform !== "win32" && child.pid)
          process.kill(-child.pid, name);
        else child.kill(name);
      } catch {
        /* Already exited. */
      }
    };
    const exited =
      child.exitCode !== null || child.signalCode !== null
        ? Promise.resolve()
        : new Promise<void>((resolve) => child.once("close", () => resolve()));
    child.stdin!.end();
    signal("SIGTERM");
    const timer = setTimeout(() => signal("SIGKILL"), 1000);
    try {
      await exited;
    } finally {
      clearTimeout(timer);
      signal("SIGKILL");
    }
  }
}

export function redact<T>(value: T, secrets: string[]): T {
  const clean = (text: string) =>
    secrets.reduce(
      (text, secret) => (secret ? text.split(secret).join("[redacted]") : text),
      text,
    );
  const visit = (item: unknown): unknown =>
    typeof item === "string"
      ? clean(item)
      : Array.isArray(item)
        ? item.map(visit)
        : item && typeof item === "object"
          ? Object.fromEntries(
              Object.entries(item).map(([key, value]) => [key, visit(value)]),
            )
          : item;
  return visit(value) as T;
}
const canonical = (value: unknown): string =>
  JSON.stringify(value, (_key, item: unknown) =>
    item && typeof item === "object" && !Array.isArray(item)
      ? Object.fromEntries(
          Object.entries(item).sort(([a], [b]) => a.localeCompare(b)),
        )
      : item,
  );
export const alias = (id: string, name: string) =>
  `mcp_${id}_${createHash("sha256").update(name).digest("hex").slice(0, 16)}`;
export async function connectMCP(
  config: MCPConfig,
  secret: MCPSecret,
  workspace: string,
  signal: AbortSignal,
) {
  signal.throwIfAborted();
  await mkdir(workspace, { recursive: true, mode: 0o700 });
  signal.throwIfAborted();
  const client = new Client(
    { name: "siagent", version: "0.1.0" },
    { capabilities: {} },
  );
  const transport =
    config.transport === "stdio"
      ? new ExactEnvironmentTransport(config, secret.env ?? {}, workspace)
      : new StreamableHTTPClientTransport(new URL(config.url), {
          requestInit: { headers: secret.headers ?? {}, redirect: "error" },
          fetch: (url, init) =>
            fetch(
              url,
              init?.method === "DELETE"
                ? {
                    ...init,
                    signal: AbortSignal.any([
                      ...(init.signal ? [init.signal] : []),
                      AbortSignal.timeout(1000),
                    ]),
                  }
                : init,
            ),
          reconnectionOptions: {
            initialReconnectionDelay: 1000,
            maxReconnectionDelay: 1000,
            reconnectionDelayGrowFactor: 1,
            maxRetries: 0,
          },
        });
  let closing: Promise<void> | undefined;
  const close = () =>
    (closing ??= (async () => {
      try {
        if (
          transport instanceof StreamableHTTPClientTransport &&
          transport.sessionId
        )
          await transport.terminateSession();
      } catch {
        /* Session termination is best effort; never retry business calls. */
      } finally {
        await client.close();
      }
    })());
  try {
    await client.connect(transport, { signal, timeout: 10000 });
    const tools: MCPTool[] = [];
    let cursor: string | undefined;
    do {
      const page = await client.listTools(cursor ? { cursor } : {}, {
        signal,
        timeout: 10000,
      });
      for (const tool of page.tools) {
        if (
          !tool.name ||
          tool.name.length > 128 ||
          tools.some((item) => item.name === tool.name)
        )
          throw new Error("mcp_invalid_catalog");
        tools.push({
          name: tool.name,
          description: (tool.description ?? "").slice(0, 4000),
          input_schema: tool.inputSchema,
          read_only: tool.annotations?.readOnlyHint === true,
        });
      }
      if (tools.length > 128 || JSON.stringify(tools).length > 256000)
        throw new Error("mcp_catalog_too_large");
      cursor = page.nextCursor;
      if (cursor && page.tools.length === 0)
        throw new Error("mcp_invalid_pagination");
    } while (cursor);
    return { client, tools, close };
  } catch {
    await close();
    throw new Error("mcp_connection_failed");
  }
}

export async function loadCapabilities(
  capabilities: Capabilities | undefined,
  secrets: Record<string, MCPSecret>,
  dataRoot: string,
  agentID: string,
  signal: AbortSignal,
  toolTimeout: number,
  uncertain: (
    callID: string,
    interrupt: boolean,
    reason?: "tool_uncertain" | "tool_timeout",
  ) => void,
  secretValues: string[],
  additionalTools: ToolRegistration[] = [],
  workspaceTools = false,
) {
  const tools: ToolRegistration[] = [...additionalTools];
  const connections: Awaited<ReturnType<typeof connectMCP>>[] = [];
  const skills = capabilities?.skills ?? [];
  const workspace = join(dataRoot, "workspaces", agentID);
  try {
    for (const config of capabilities?.mcps ?? []) {
      const connection = await connectMCP(
        config,
        secrets[config.id] ?? { env: {}, headers: {} },
        workspace,
        signal,
      );
      connections.push(connection);
      for (const pinned of config.tools) {
        const live = connection.tools.find((tool) => tool.name === pinned.name);
        if (
          !live ||
          canonical(redact(live.input_schema, secretValues)) !==
            canonical(pinned.input_schema)
        )
          throw new Error("mcp_catalog_changed");
        tools.push(
          defineTool({
            name: alias(config.id, pinned.name),
            description: `${config.name} / ${pinned.name}: ${pinned.description}`,
            parameters: Type.Unsafe<Record<string, unknown>>(
              pinned.input_schema,
            ),
            replay: "unsafe",
            executionMode: "sequential",
            outputLimits: { maxBytes: 32000, maxLines: 500, retain: "head" },
            execute: async (args, api, context) => {
              try {
                const result = await connection.client.callTool(
                  { name: pinned.name, arguments: args },
                  undefined,
                  { signal: context.abortSignal, timeout: toolTimeout },
                );
                const content = result.content as {
                  type: string;
                  text?: string;
                }[];
                return {
                  output: redact(
                    content.map((block) => ({
                      type: "text" as const,
                      text:
                        block.type === "text"
                          ? (block.text ?? "")
                          : `[Unsupported MCP content: ${block.type}]`,
                    })),
                    secretValues,
                  ),
                  isError: result.isError === true,
                };
              } catch (error) {
                uncertain(
                  api.callId,
                  !context.abortSignal?.aborted,
                  error instanceof McpError &&
                    error.code === ErrorCode.RequestTimeout
                    ? "tool_timeout"
                    : "tool_uncertain",
                );
                throw new Error(
                  "MCP 调用结果不明，请核对外部系统；不会自动重试。",
                );
              }
            },
          }),
        );
      }
    }
    if (skills.length)
      tools.push(
        defineTool({
          name: "skill_resource",
          description:
            "Read a UTF-8 resource from an explicitly enabled Skill. This tool returns scripts as text and never executes them; use the workspace bash tool when the Agent has it enabled.",
          parameters: Type.Object({
            skill_id: Type.String(),
            path: Type.String(),
          }),
          replay: "safe",
          execute: async ({ skill_id, path }) => {
            const skill = skills.find((skill) => skill.id === skill_id);
            const file = skill?.files.find((file) => file.path === path);
            if (!file || file.size > 128000)
              throw new Error(
                "Skill resource is unavailable or exceeds 128 KB.",
              );
            const root = await realpath(join(dataRoot, "skills", skill_id));
            const target = await realpath(resolve(root, path));
            if (!target.startsWith(root + sep))
              throw new Error("Invalid Skill resource path.");
            const bytes = await readFile(target);
            if (bytes.length > 128000)
              throw new Error("Skill resource exceeds 128 KB.");
            return {
              output: [
                {
                  type: "text" as const,
                  text: new TextDecoder("utf-8", { fatal: true }).decode(bytes),
                },
              ],
            };
          },
        }),
      );
    const instructions = skills
      .map((skill) => {
        // Workspace tools may run Skill scripts. Expose the managed directory so
        // the model can copy files into its workspace; Skill files stay inputs.
        const directory = workspaceTools
          ? `\nSkill files: ${join(dataRoot, "skills", skill.id)} (treat as read-only; copy files into the workspace before running a script)`
          : "";
        return `\n<skill id="${skill.id}" name=${JSON.stringify(skill.name)}>\n${skill.content}\nAvailable resources: ${JSON.stringify(skill.files.map((file) => file.path))}${directory}\n</skill>`;
      })
      .join("\n");
    return {
      extension: defineExtension({ name: "capabilities", tools }),
      tools,
      instructions,
      close: async () => {
        await Promise.allSettled(
          connections.map((connection) => connection.close()),
        );
      },
    };
  } catch (error) {
    await Promise.allSettled(
      connections.map((connection) => connection.close()),
    );
    throw error;
  }
}
