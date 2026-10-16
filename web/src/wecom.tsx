import { FormEvent, useEffect, useState } from "react";
import type { Agent } from "./agents";
type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Channel = {
  id: string;
  corp_id: string;
  app_id: number;
  agent_id: string;
  enabled: boolean;
  archived: boolean;
  version: number;
  verified: boolean;
  has_secrets: boolean;
  callback_path: string;
};
type Inbox = {
  id: string;
  member: string;
  msg_id: string;
  state: string;
  error: string;
  preview: string;
  conversation_id: string;
  created: number;
};
type Delivery = {
  id: string;
  inbox_id: string;
  member: string;
  kind: string;
  part: number;
  total: number;
  state: string;
  attempts: number;
  code: number;
  error: string;
};
const labels: Record<string, string> = {
  pending: "等待处理",
  admitted: "已创建任务",
  rejected: "未创建任务",
  sending: "正在发送",
  sent: "企微已接受",
  retry: "等待有限重试",
  failed: "明确失败",
  unknown: "结果不明，待核对",
  blocked: "前段未成功，已停止",
  cancelled: "已取消",
};
export function WecomWorkspace({
  request,
  agents,
  onConversation,
}: {
  request: Api;
  agents: Agent[];
  onConversation: (id: string) => Promise<void>;
}) {
  const blank = {
    id: "",
    corp_id: "",
    app_id: 0,
    agent_id: agents.find((agent) => !agent.archived)?.id ?? "",
    enabled: false,
    archived: false,
    version: 0,
    verified: false,
    has_secrets: false,
    callback_path: "",
  };
  const [channels, setChannels] = useState<Channel[]>([]);
  const [editor, setEditor] = useState<Channel>(blank);
  const [secret, setSecret] = useState("");
  const [token, setToken] = useState("");
  const [aes, setAES] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [diagnostics, setDiagnostics] = useState<{
    inbox: Inbox[];
    outbox: Delivery[];
  }>({ inbox: [], outbox: [] });
  const base = "/wecom/channels";
  const note = (error: unknown) =>
    error instanceof Error ? error.message : "无法连接服务，请稍后重试。";
  useEffect(() => {
    let active = true;
    void request<Channel[]>(base)
      .then((values) => {
        if (active) {
          setChannels(values);
          if (values[0]) setEditor(values[0]);
        }
      })
      .catch((error) => {
        if (active) setError(note(error));
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    let active = true;
    setDiagnostics({ inbox: [], outbox: [] });
    if (!editor.id) return;
    const refresh = () =>
      void request<{ inbox: Inbox[]; outbox: Delivery[] }>(
        `${base}/${editor.id}/diagnostics`,
      )
        .then((value) => {
          if (active) setDiagnostics(value);
        })
        .catch((error) => {
          if (active) setError(note(error));
        });
    refresh();
    const timer = setInterval(refresh, 2000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [editor.id]);
  function edit(channel: Channel) {
    setEditor(channel);
    setSecret("");
    setToken("");
    setAES("");
    setConfirmed(false);
    setError("");
    setFeedback("");
  }
  async function action(work: () => Promise<void>) {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      await work();
    } catch (error) {
      setError(note(error));
    } finally {
      setPending(false);
    }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    await action(async () => {
      const saved = await request<Channel>(
        editor.id ? `${base}/${editor.id}` : base,
        editor.id ? "PUT" : "POST",
        {
          corp_id: editor.corp_id,
          app_id: editor.app_id,
          agent_id: editor.agent_id,
          enabled: editor.enabled,
          secret,
          token,
          aes_key: aes,
          confirm: confirmed,
        },
      );
      edit(saved);
      setChannels(await request<Channel[]>(base));
      setFeedback(
        "渠道配置已保存。请在企微管理后台完成回调验证，并通过内部成员实际测试收发。",
      );
    });
  }
  return (
    <div className="wecom-workspace">
      <aside className="wecom-channel-list">
        <div className="knowledge-section-head">
          <h2>企微内部应用</h2>
          <button disabled={pending} onClick={() => edit(blank)}>
            新增应用
          </button>
        </div>
        {channels.map((channel) => (
          <button
            className={`wecom-channel ${editor.id === channel.id ? "selected" : ""}`}
            key={channel.id}
            disabled={pending}
            onClick={() => edit(channel)}
          >
            <strong>
              {channel.corp_id} · {channel.app_id}
            </strong>
            <small>
              {channel.archived
                ? "已归档"
                : channel.enabled
                  ? "已启用"
                  : "已停用"}{" "}
              ·{" "}
              {agents.find((agent) => agent.id === channel.agent_id)?.name ??
                "未绑定助手"}
            </small>
          </button>
        ))}
        <p className="muted">
          仅支持企业微信内部自建应用的文本私聊。不同企业、应用和成员使用独立会话。
        </p>
        <p className="muted">成员可发送 /new 开启新会话。</p>
      </aside>
      <section className="wecom-channel-editor">
        <h2>{editor.id ? "应用配置" : "新增内部应用"}</h2>
        <form onSubmit={save}>
          <fieldset disabled={pending || editor.archived}>
            <label>
              企业 CorpID
              <input
                required
                maxLength={128}
                readOnly={Boolean(editor.id)}
                value={editor.corp_id}
                onChange={(event) =>
                  setEditor({ ...editor, corp_id: event.target.value })
                }
              />
            </label>
            <label>
              应用 AgentID
              <input
                type="number"
                required
                min={1}
                max={2147483647}
                readOnly={Boolean(editor.id)}
                value={editor.app_id || ""}
                onChange={(event) =>
                  setEditor({ ...editor, app_id: Number(event.target.value) })
                }
              />
            </label>
            <small>企业与应用标识保存后固定，其他应用请新增配置。</small>
            <label>
              应用 Secret
              <input
                type="password"
                required={!editor.has_secrets}
                autoComplete="new-password"
                maxLength={8192}
                value={secret}
                onChange={(event) => setSecret(event.target.value)}
                placeholder={
                  editor.has_secrets
                    ? "留空保留原 Secret"
                    : "从自建应用后台取得"
                }
              />
            </label>
            <label>
              回调 Token
              <input
                type="password"
                required={!editor.has_secrets}
                autoComplete="new-password"
                maxLength={256}
                value={token}
                onChange={(event) => setToken(event.target.value)}
                placeholder={
                  editor.has_secrets
                    ? "留空保留原 Token"
                    : "与企微接收消息配置一致"
                }
              />
            </label>
            <label>
              EncodingAESKey
              <input
                type="password"
                required={!editor.has_secrets}
                autoComplete="new-password"
                minLength={43}
                maxLength={43}
                value={aes}
                onChange={(event) => setAES(event.target.value)}
                placeholder={
                  editor.has_secrets ? "留空保留原 Key" : "43 位 EncodingAESKey"
                }
              />
            </label>
            <label>
              绑定助手
              <select
                aria-label="绑定助手"
                value={editor.agent_id}
                onChange={(event) =>
                  setEditor({ ...editor, agent_id: event.target.value })
                }
              >
                <option value="">解除绑定</option>
                {agents
                  .filter((agent) => !agent.archived)
                  .map((agent) => (
                    <option key={agent.id} value={agent.id}>
                      {agent.name}
                      {agent.enabled ? "" : "（已停用）"}
                    </option>
                  ))}
              </select>
            </label>
            <small>
              修改助手绑定前先保存停用状态；重新绑定后不会混入旧助手上下文。
            </small>
            <label className="checkbox-label">
              <input
                type="checkbox"
                checked={editor.enabled}
                onChange={(event) => {
                  setEditor({ ...editor, enabled: event.target.checked });
                  setConfirmed(false);
                }}
              />
              启用企微渠道
            </label>
            <small>
              停用后停止接收新消息和后续发送；已经在途的发送无法撤销。
            </small>
            {editor.enabled && (
              <label className="checkbox-label">
                <input
                  type="checkbox"
                  required
                  checked={confirmed}
                  onChange={(event) => setConfirmed(event.target.checked)}
                />
                我确认内部成员消息及检索片段可发送至绑定助手的模型服务
              </label>
            )}
            <button className="primary">保存企微配置</button>
          </fieldset>
        </form>
        {editor.id && (
          <section className="wecom-guide">
            <h3>完成企微后台配置</h3>
            <label>
              回调 URL
              <input readOnly value={location.origin + editor.callback_path} />
            </label>
            <ol>
              <li>配置应用可见范围，并按企微要求设置服务器可信 IP 和域名。</li>
              <li>
                在接收消息配置填写上述 HTTPS URL，以及保存的
                Token、EncodingAESKey；修改密钥时须同步两端。
              </li>
              <li>
                回调验证通过后，在此启用渠道，让可见范围内的内部成员发送文字测试。
              </li>
            </ol>
            <p className="muted">
              {editor.verified
                ? "已收到 URL 验证请求。"
                : "尚未记录 URL 验证，请在企微后台保存配置后刷新。"}{" "}
              凭据测试不代表真实成员收发已验收。
            </p>
            <div className="form-actions">
              <button
                disabled={pending || editor.archived}
                onClick={() =>
                  void action(async () => {
                    const result = await request<{ message: string }>(
                      `${base}/${editor.id}/test`,
                      "POST",
                    );
                    setFeedback(result.message);
                  })
                }
              >
                测试企微凭据
              </button>
              <button
                disabled={pending}
                onClick={() =>
                  void action(async () => {
                    const values = await request<Channel[]>(base);
                    setChannels(values);
                    const current = values.find(
                      (channel) => channel.id === editor.id,
                    );
                    if (current) setEditor(current);
                    setFeedback("渠道状态已刷新。");
                  })
                }
              >
                刷新渠道状态
              </button>
            </div>
          </section>
        )}
        {editor.id && !editor.archived && (
          <details className="agent-archive">
            <summary>归档企微渠道</summary>
            <p>
              先保存停用状态。归档解除助手绑定，停止尚未发送的回复；已有任务及历史仍保留。
            </p>
            <button
              disabled={pending}
              onClick={() =>
                void action(async () => {
                  await request(`${base}/${editor.id}`, "DELETE");
                  const values = await request<Channel[]>(base);
                  setChannels(values);
                  const current = values.find(
                    (channel) => channel.id === editor.id,
                  );
                  if (current) edit(current);
                  setFeedback("渠道已归档，历史保留。");
                })
              }
            >
              确认归档渠道
            </button>
          </details>
        )}
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {feedback && (
          <p className="success" role="status">
            {feedback}
          </p>
        )}
        {editor.id && (
          <section className="wecom-diagnostics">
            <h3>最近入站消息</h3>
            {!diagnostics.inbox.length && (
              <p className="muted">
                暂无入站记录。配置完成后使用内部成员发送文本。
              </p>
            )}
            {diagnostics.inbox.map((item) => (
              <article key={item.id}>
                <div>
                  <strong>{item.member}</strong>
                  <small>
                    {new Date(item.created).toLocaleString("zh-CN")} ·{" "}
                    {labels[item.state] ?? item.state}
                  </small>
                </div>
                <p className="wecom-preview">{item.preview}</p>
                {item.error && <p className="muted">{item.error}</p>}
                {item.conversation_id && (
                  <button
                    onClick={() =>
                      void action(() => onConversation(item.conversation_id))
                    }
                  >
                    查看对应会话
                  </button>
                )}
              </article>
            ))}
            <h3>最近发送记录</h3>
            {diagnostics.outbox.map((item) => (
              <article key={item.id}>
                <strong>
                  {item.member} ·{" "}
                  {item.kind === "hint" ? "处理中提示" : "最终通知"} ·{" "}
                  {item.part}/{item.total}
                </strong>
                <p className={item.state === "unknown" ? "error" : "muted"}>
                  {labels[item.state] ?? item.state} · 已尝试 {item.attempts} 次
                  {item.code ? ` · 错误码 ${item.code}` : ""}
                </p>
                <p className="muted">{item.error}</p>
                {item.state === "failed" && (
                  <button
                    disabled={pending}
                    onClick={() =>
                      void action(async () => {
                        await request(
                          `/wecom/deliveries/${item.id}/retry`,
                          "POST",
                        );
                        setFeedback(
                          "已安排重试明确失败的发送；不会重新执行 Agent 任务。",
                        );
                      })
                    }
                  >
                    重试已拒绝的发送
                  </button>
                )}
              </article>
            ))}
          </section>
        )}
      </section>
    </div>
  );
}
