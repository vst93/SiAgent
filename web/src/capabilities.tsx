import { Icon } from "./icons";
import { LinkedAgents } from "./linked-agents";
import { KnowledgeDialog as SettingsDialog } from "./knowledge-dialog";
import { FormEvent, useEffect, useState } from "react";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
export type Skill = {
  id: string;
  name: string;
  description: string;
  content: string;
  compatibility: string;
  files: { path: string; size: number }[];
};
type MCP = {
  version: number;
  id: string;
  name: string;
  transport: "stdio" | "http";
  command: string;
  args: string[];
  url: string;
  tools: {
    name: string;
    description: string;
    input_schema: unknown;
    read_only: boolean;
  }[];
  env_keys: string[];
  header_keys: string[];
};
type Bindings = { skills: string[]; mcps: { id: string; tools: string[] }[] };
const empty: Bindings = { skills: [], mcps: [] };
const initialMCP = {
  id: "",
  name: "",
  transport: "stdio" as "stdio" | "http",
  command: "",
  args: "[]",
  url: "",
  env: "",
  headers: "",
  clear_secrets: false,
  trusted: false,
};
function secretObject(text: string): Record<string, string> | undefined {
  if (!text.trim()) return undefined;
  const value: unknown = JSON.parse(text);
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    !Object.values(value).every((item) => typeof item === "string")
  )
    throw new Error(
      "环境变量和请求头须为 JSON 对象，所有值必须为字符串；清空配置请填写 {}。",
    );
  return value as Record<string, string>;
}
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作未完成，请稍后重试。";

export function CapabilitySettings({
  request,
  agentID = "default",
  agentName = "默认助手",
  readOnly = false,
  catalog = false,
}: {
  request: Api;
  agentID?: string;
  agentName?: string;
  readOnly?: boolean;
  catalog?: boolean;
}) {
  const [loaded, setLoaded] = useState(false);
  const [skills, setSkills] = useState<Skill[]>([]);
  const [mcps, setMCPs] = useState<MCP[]>([]);
  const [bindings, setBindings] = useState<Bindings>(empty);
  const [confirmed, setConfirmed] = useState(false);
  const [form, setForm] = useState(initialMCP);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [pending, setPending] = useState(false);
  const [preview, setPreview] = useState<{
    title: string;
    text: string;
  } | null>(null);
  const [deletingMCP, setDeletingMCP] = useState<MCP | null>(null);
  async function refresh() {
    setLoaded(false);
    const [skills, mcps, bindings] = await Promise.all([
      catalog ? Promise.resolve([] as Skill[]) : request<Skill[]>("/skills"),
      request<MCP[]>("/mcps"),
      catalog
        ? Promise.resolve(empty)
        : request<Bindings>(`/agents/${agentID}/capabilities`),
    ]);
    setSkills(skills);
    setMCPs(mcps);
    setBindings(bindings);
    setConfirmed(false);
    setLoaded(true);
  }
  useEffect(() => {
    void refresh().catch((error) => setError(errorText(error)));
  }, [agentID, catalog]);
  async function action(work: () => Promise<void>) {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      await work();
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }
  function chooseSkill(id: string, checked: boolean) {
    setBindings({
      ...bindings,
      skills: checked
        ? [...bindings.skills, id]
        : bindings.skills.filter((value) => value !== id),
    });
    setConfirmed(false);
  }
  function chooseTool(id: string, name: string, checked: boolean) {
    const tools =
      bindings.mcps.find((binding) => binding.id === id)?.tools ?? [];
    const selected = checked
      ? [...tools, name]
      : tools.filter((value) => value !== name);
    setBindings({
      ...bindings,
      mcps: [
        ...bindings.mcps.filter((binding) => binding.id !== id),
        ...(selected.length ? [{ id, tools: selected }] : []),
      ],
    });
    setConfirmed(false);
  }
  function selectAll(checked: boolean) {
    setBindings({
      skills: checked ? skills.map((skill) => skill.id) : [],
      mcps: checked
        ? mcps.map((mcp) => ({
            id: mcp.id,
            tools: mcp.tools.map((tool) => tool.name),
          }))
        : [],
    });
    setConfirmed(false);
  }
  function selectMCP(mcp: MCP, checked: boolean) {
    setBindings({
      ...bindings,
      mcps: [
        ...bindings.mcps.filter((binding) => binding.id !== mcp.id),
        ...(checked && mcp.tools.length
          ? [{ id: mcp.id, tools: mcp.tools.map((tool) => tool.name) }]
          : []),
      ],
    });
    setConfirmed(false);
  }
  function edit(mcp: MCP) {
    setForm({
      ...initialMCP,
      id: mcp.id,
      name: mcp.name,
      transport: mcp.transport,
      command: mcp.command,
      args: JSON.stringify(mcp.args),
      url: mcp.url,
    });
  }
  async function saveMCP(event: FormEvent) {
    event.preventDefault();
    await action(async () => {
      const args: unknown = JSON.parse(form.args);
      if (!Array.isArray(args) || !args.every((arg) => typeof arg === "string"))
        throw new Error('程序参数须为字符串数组，例如 ["server.js"]。');
      const saved = await request<MCP>(
        form.id ? `/mcps/${form.id}` : "/mcps",
        form.id ? "PUT" : "POST",
        {
          ...form,
          args,
          env: secretObject(form.env),
          headers: secretObject(form.headers),
        },
      );
      setMCPs((items) => [
        ...items.filter((item) => item.id !== saved.id),
        saved,
      ]);
      setForm(initialMCP);
      setFeedback("MCP 已保存。请先测试连接，再到 Agent 配置中授权工具。");
    });
  }
  async function removeMCP() {
    if (!deletingMCP) return;
    await action(async () => {
      await request(`/mcps/${deletingMCP.id}`, "DELETE");
      setMCPs((items) => items.filter((item) => item.id !== deletingMCP.id));
      setDeletingMCP(null);
      setFeedback("MCP 已删除，关联 Agent 已自动解除；历史任务保留。");
    });
  }
  return (
    <section className="capability-settings">
      <div className="capability-content">
        <p className="muted">
          {catalog
            ? "维护 MCP 连接并测试可用工具，再到 Agent 配置中授予所需工具权限。"
            : `${agentName}仅能使用下方挂载并保存的 Skills 与授权工具；配置变更用于新提交任务。`}
          {!catalog && (
            <>
              {" "}
              添加和连接测试请前往<a href="#/settings/tools">插件接入</a>。
            </>
          )}
        </p>
        {!catalog && (
          <>
            <div className="capability-batch-actions">
              <span>批量授权</span>
              <button
                type="button"
                disabled={!loaded || pending || readOnly}
                onClick={() => selectAll(true)}
              >
                全选
              </button>
              <button
                type="button"
                disabled={!loaded || pending || readOnly}
                onClick={() => selectAll(false)}
              >
                全不选
              </button>
            </div>
            <fieldset disabled={!loaded || pending || (!catalog && readOnly)}>
              <legend>Skills 挂载</legend>
              {skills.length === 0 && (
                <p className="muted">尚未上传技能，请先到插件接入上传。</p>
              )}
              {skills.map((skill) => (
                <div className="capability-card" key={skill.id}>
                  <label className="checkbox-label">
                    {!catalog && (
                      <input
                        type="checkbox"
                        checked={bindings.skills.includes(skill.id)}
                        onChange={(event) =>
                          chooseSkill(skill.id, event.target.checked)
                        }
                      />
                    )}
                    <strong>{skill.name}</strong>
                  </label>
                  <p>{skill.description}</p>
                  <details>
                    <summary>
                      <Icon
                        name="caret-right"
                        size={14}
                        className="disclosure-icon"
                      />
                      查看技能内容、资源和依赖
                    </summary>
                    <p className="muted">
                      {skill.compatibility ||
                        "未声明运行环境依赖。请自行检查脚本和依赖清单。"}{" "}
                      指令会发送给模型；资源仅在助手请求时读取。脚本作为文本提供，不自动执行，不安装依赖。
                    </p>
                    <pre className="resource-preview">{skill.content}</pre>
                    <ul className="resource-list">
                      {skill.files.map((file) => (
                        <li key={file.path}>
                          <button
                            type="button"
                            onClick={() =>
                              void action(async () => {
                                const result = await request<{
                                  content: string;
                                }>(
                                  `/skills/${skill.id}/files/${file.path.split("/").map(encodeURIComponent).join("/")}`,
                                );
                                setPreview({
                                  title: `${skill.name} / ${file.path}`,
                                  text: result.content,
                                });
                              })
                            }
                          >
                            {file.path}
                          </button>
                          <small>{file.size.toLocaleString()} 字节</small>
                        </li>
                      ))}
                    </ul>
                  </details>
                </div>
              ))}
            </fieldset>
          </>
        )}
        <fieldset disabled={!loaded || pending || (!catalog && readOnly)}>
          <legend>MCP 服务与可用工具</legend>
          {mcps.length === 0 && <p className="muted">尚未配置外部工具服务。</p>}
          {mcps.map((mcp) => (
            <div className="capability-card" key={mcp.id}>
              <div className="capability-card-head">
                <strong>{mcp.name}</strong>
                <small> · 配置版本 {mcp.version}</small>
                {!catalog && mcp.tools.length > 0 && (
                  <span className="capability-inline-actions">
                    <button
                      type="button"
                      onClick={() => selectMCP(mcp, true)}
                    >
                      全选工具
                    </button>
                    <button
                      type="button"
                      onClick={() => selectMCP(mcp, false)}
                    >
                      清空
                    </button>
                  </span>
                )}
              </div>
              <p className="muted">
                {mcp.transport === "stdio"
                  ? `本地进程 · ${mcp.command}`
                  : `Streamable HTTP · ${mcp.url}`}
              </p>
              <small>
                已保存的敏感字段：
                {[...mcp.env_keys, ...mcp.header_keys].join("、") || "无"}
                。保存后不回显值。
              </small>
              {catalog && (
                <LinkedAgents request={request} kind="mcps" id={mcp.id} />
              )}
              {catalog && (
                <div className="form-actions">
                  <button
                    type="button"
                    onClick={() =>
                      void action(async () => {
                        const tested = await request<MCP>(
                          `/mcps/${mcp.id}/test`,
                          "POST",
                        );
                        setMCPs((items) =>
                          items.map((item) =>
                            item.id === mcp.id ? tested : item,
                          ),
                        );
                        setFeedback(
                          `“${mcp.name}”连接成功，发现 ${tested.tools.length} 个工具。请到 Agent 配置中授权必要的工具。`,
                        );
                      })
                    }
                  >
                    测试连接与刷新工具
                  </button>
                  <button type="button" onClick={() => edit(mcp)}>
                    编辑 MCP
                  </button>
                  <button
                    type="button"
                    onClick={() => setDeletingMCP(mcp)}
                  >
                    删除 MCP
                  </button>
                </div>
              )}
              {mcp.tools.length === 0 && (
                <p className="muted">
                  尚无可授权工具。保存配置后须重新测试连接。
                </p>
              )}
              {mcp.tools.map((tool) => (
                <details className="tool-permission" key={tool.name}>
                  <summary>
                    <Icon
                      name="caret-right"
                      size={14}
                      className="disclosure-icon"
                    />
                    {tool.name} ·{" "}
                    {tool.read_only ? "服务声明只读" : "可能写入或删除"}
                  </summary>
                  {!catalog && (
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={bindings.mcps.some(
                          (binding) =>
                            binding.id === mcp.id &&
                            binding.tools.includes(tool.name),
                        )}
                        onChange={(event) =>
                          chooseTool(mcp.id, tool.name, event.target.checked)
                        }
                      />
                      授权{agentName}使用 {tool.name}
                    </label>
                  )}
                  <p>{tool.description || "服务未提供描述。"}</p>
                  <pre className="resource-preview">
                    {JSON.stringify(tool.input_schema, null, 2)}
                  </pre>
                </details>
              ))}
            </div>
          ))}
          {catalog && (
            <details open={!!form.id}>
              <summary>
                <Icon
                  name="caret-right"
                  size={14}
                  className="disclosure-icon"
                />
                {form.id ? "编辑 MCP 配置" : "添加 MCP 服务"}
              </summary>
              <form onSubmit={saveMCP} className="mcp-form">
                <label>
                  MCP 名称
                  <input
                    required
                    value={form.name}
                    onChange={(event) =>
                      setForm({ ...form, name: event.target.value })
                    }
                    maxLength={100}
                  />
                </label>
                <label>
                  连接方式
                  <select
                    value={form.transport}
                    onChange={(event) =>
                      setForm({
                        ...form,
                        transport: event.target.value as "stdio" | "http",
                      })
                    }
                  >
                    <option value="stdio">本地 stdio 进程</option>
                    <option value="http">远程 Streamable HTTP</option>
                  </select>
                </label>
                {form.transport === "stdio" ? (
                  <>
                    <label>
                      程序绝对路径
                      <input
                        required
                        value={form.command}
                        onChange={(event) =>
                          setForm({ ...form, command: event.target.value })
                        }
                        placeholder="/usr/local/bin/node"
                      />
                    </label>
                    <label htmlFor="mcp-args">参数（JSON 字符串数组）</label>
                    <textarea
                      id="mcp-args"
                      required
                      value={form.args}
                      onChange={(event) =>
                        setForm({ ...form, args: event.target.value })
                      }
                      spellCheck={false}
                    />
                    <label htmlFor="mcp-env">
                      环境变量（JSON 对象，留空沿用已保存值）
                    </label>
                    <textarea
                      id="mcp-env"
                      value={form.env}
                      onChange={(event) =>
                        setForm({ ...form, env: event.target.value })
                      }
                      placeholder={'{"TOKEN":"…"}'}
                      spellCheck={false}
                      autoComplete="off"
                    />
                    <small>
                      程序从助手工作区启动，不经过
                      Shell，只传入显式环境变量。需要 PATH、HOME
                      等变量时请自行配置。密钥请放在环境变量中；程序和参数按普通配置保存。
                    </small>
                  </>
                ) : (
                  <>
                    <label>
                      服务地址
                      <input
                        type="url"
                        required
                        value={form.url}
                        onChange={(event) =>
                          setForm({ ...form, url: event.target.value })
                        }
                        placeholder="https://example.com/mcp"
                      />
                    </label>
                    <label htmlFor="mcp-headers">
                      请求头（JSON 对象，留空沿用已保存值）
                    </label>
                    <textarea
                      id="mcp-headers"
                      value={form.headers}
                      onChange={(event) =>
                        setForm({ ...form, headers: event.target.value })
                      }
                      placeholder={'{"Authorization":"Bearer …"}'}
                      spellCheck={false}
                      autoComplete="off"
                    />
                    <small>
                      使用请求头认证；不接受 URL 内的认证或查询参数。暂不提供
                      OAuth 授权流程。
                    </small>
                  </>
                )}
                {form.id && (
                  <label className="checkbox-label">
                    <input
                      type="checkbox"
                      checked={form.clear_secrets}
                      onChange={(event) =>
                        setForm({
                          ...form,
                          clear_secrets: event.target.checked,
                        })
                      }
                    />
                    清除已有环境变量和请求头，以本次填写为准
                  </label>
                )}
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    required
                    checked={form.trusted}
                    onChange={(event) =>
                      setForm({ ...form, trusted: event.target.checked })
                    }
                  />
                  我确认服务及代码可信，并允许连接测试启动本地进程或访问远程地址
                </label>
                <div className="form-actions">
                  <button>保存 MCP 配置</button>
                  {form.id && (
                    <button type="button" onClick={() => setForm(initialMCP)}>
                      结束编辑
                    </button>
                  )}
                </div>
              </form>
            </details>
          )}
        </fieldset>
        {!catalog && (
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void action(async () => {
                setBindings(
                  await request<Bindings>(
                    `/agents/${agentID}/capabilities`,
                    "PUT",
                    {
                      ...bindings,
                      confirm: confirmed,
                    },
                  ),
                );
                setConfirmed(false);
                setFeedback(
                  "助手授权已保存，新提交的任务将使用这些技能与工具。",
                );
              });
            }}
          >
            <p className="permission-notice">
              工具可能写入或删除外部数据；“只读”仅是服务声明。取消不能撤销已发生的操作。本地代码不受操作系统沙箱隔离，只应启用可信来源；技能与工具返回的内容不能扩大授权。
            </p>
            {(bindings.skills.length > 0 || bindings.mcps.length > 0) && (
              <label className="checkbox-label">
                <input
                  type="checkbox"
                  checked={confirmed}
                  onChange={(event) => setConfirmed(event.target.checked)}
                  required
                />
                我已检查所选内容与权限，仅授予{agentName}必要的能力
              </label>
            )}
            <button
              className="primary"
              disabled={!loaded || pending || readOnly}
            >
              保存助手工具授权
            </button>
          </form>
        )}
        {deletingMCP && (
          <SettingsDialog
            title={`删除 MCP“${deletingMCP.name}”？`}
            pending={pending}
            onClose={() => setDeletingMCP(null)}
          >
            <p>
              将永久删除 MCP 配置和密钥，并自动从所有关联 Agent
              中移除。已有任务使用提交时的快照，不受影响。
            </p>
            <LinkedAgents
              request={request}
              kind="mcps"
              id={deletingMCP.id}
            />
            <footer>
              <button
                data-dialog-cancel
                disabled={pending}
                onClick={() => setDeletingMCP(null)}
              >
                取消
              </button>
              <button
                className="danger-button"
                disabled={pending}
                onClick={() => void removeMCP()}
              >
                {pending ? "正在删除…" : "确认删除"}
              </button>
            </footer>
          </SettingsDialog>
        )}
        {preview && (
          <section className="resource-section">
            <strong>{preview.title}</strong>
            <pre className="resource-preview">{preview.text}</pre>
            <button type="button" onClick={() => setPreview(null)}>
              关闭资源预览
            </button>
          </section>
        )}
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {!loaded && !error && (
          <p className="muted" role="status">
            正在读取技能与工具…
          </p>
        )}
        {!loaded && error && (
          <button
            onClick={() => {
              setError("");
              void refresh().catch((error) => setError(errorText(error)));
            }}
          >
            重新读取技能与工具
          </button>
        )}
        {feedback && (
          <p className="success" role="status">
            {feedback}
          </p>
        )}
        {pending && (
          <p className="muted" role="status">
            正在处理…
          </p>
        )}
      </div>
    </section>
  );
}

export function ToolCallDetails({
  request,
  runID,
  callID,
}: {
  request: Api;
  runID: string;
  callID: string;
}) {
  const [detail, setDetail] = useState<{
    arguments: string;
    result: string;
  } | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (open)
      void request<{ arguments: string; result: string }>(
        `/runs/${runID}/tools/${encodeURIComponent(callID)}`,
      )
        .then(setDetail)
        .catch((error) => setError(errorText(error)));
  }, [open, runID, callID]);
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        <Icon name="caret-right" size={14} className="disclosure-icon" />
        查看调用参数与结果
      </summary>
      <small>
        业务数据仅在此展开；已知凭据已脱敏，内容最多保留 32
        KB。中断或取消后请核对外部系统。
      </small>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {detail && (
        <>
          <strong>调用参数</strong>
          <pre className="resource-preview">
            {detail.arguments || "未保留参数"}
          </pre>
          <strong>返回结果</strong>
          <pre className="resource-preview">
            {detail.result || "未收到可确认的结果"}
          </pre>
        </>
      )}
    </details>
  );
}
