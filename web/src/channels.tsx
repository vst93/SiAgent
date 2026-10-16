import { Icon } from "./icons";
import { FormEvent, useEffect, useState } from "react";
import type { Agent } from "./agents";
import { KnowledgeDialog as SettingsDialog } from "./knowledge-dialog";
type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Field = {
  key: string;
  label: string;
  secret?: boolean;
  required?: boolean;
  type?: string;
  help?: string;
};
type Provider = {
  id: string;
  name: string;
  description: string;
  documentation?: string;
  transport: string;
  fields: Field[];
  identity_fields: string[];
  legacy?: boolean;
};
type Status = { state: string; message: string };
type Channel = {
  id: string;
  name: string;
  provider: string;
  config: Record<string, string>;
  agent_id: string;
  enabled: boolean;
  archived: boolean;
  version: number;
  verified: boolean;
  has_secrets: boolean;
  available: boolean;
  callback_path?: string;
  status: Status;
};
type Inbox = {
  id: string;
  member: string;
  state: string;
  error: string;
  preview: string;
  conversation_id: string;
  created: number;
};
type Delivery = {
  id: string;
  inbox_id: string;
  kind: string;
  part: number;
  total: number;
  state: string;
  attempts: number;
  code: number;
  error: string;
};
type Diagnostics = { inbox: Inbox[]; outbox: Delivery[]; status: Status };
const empty: Diagnostics = {
  inbox: [],
  outbox: [],
  status: { state: "stopped", message: "渠道尚未启用。" },
};
const labels: Record<string, string> = {
  pending: "等待处理",
  admitted: "已创建任务",
  rejected: "未创建任务",
  sending: "正在发送",
  sent: "渠道已接受",
  retry: "等待有限重试",
  failed: "明确失败",
  unknown: "结果不明，待核对",
  blocked: "前段未成功，已停止",
  cancelled: "已取消",
  connected: "已连接",
  connecting: "正在连接",
  reconnecting: "正在重连",
  disconnected: "连接断开",
  stopped: "已停用",
  unavailable: "未安装适配器",
  webhook: "回调模式",
};
const note = (error: unknown) =>
  error instanceof Error ? error.message : "操作未完成，请稍后重试。";
function ChannelEditor({
  channel,
  providers,
  agents,
  request,
  onClose,
  onSaved,
}: {
  channel: Channel;
  providers: Provider[];
  agents: Agent[];
  request: Api;
  onClose: () => void;
  onSaved: (channel: Channel) => void;
}) {
  const [draft, setDraft] = useState(channel);
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const provider = providers.find((value) => value.id === draft.provider);
  async function save(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      const result = await request<Channel>(
        `/channels${draft.id ? `/${draft.id}` : ""}`,
        draft.id ? "PUT" : "POST",
        {
          name: draft.name,
          provider: draft.provider,
          config: draft.config,
          secrets,
          agent_id: draft.agent_id,
          enabled: draft.enabled,
          confirm: confirmed,
        },
      );
      setSecrets({});
      onSaved(result);
    } catch (error) {
      setError(note(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <SettingsDialog
      title={channel.id ? "编辑渠道" : "新增渠道"}
      pending={pending}
      onClose={onClose}
    >
      <form className="channel-editor" onSubmit={save}>
        <fieldset disabled={pending}>
          <label htmlFor="channel-provider">渠道类型</label>
          <select
            id="channel-provider"
            disabled={!!draft.id}
            value={draft.provider}
            onChange={(event) => {
              setDraft({ ...draft, provider: event.target.value, config: {} });
              setSecrets({});
              setError("");
            }}
          >
            {!provider && (
              <option value={draft.provider}>
                {draft.provider} · 未安装适配器
              </option>
            )}
            {providers.map((value) => (
              <option key={value.id} value={value.id}>
                {value.name}
                {value.legacy ? "（兼容）" : ""}
              </option>
            ))}
          </select>
          <p className="muted">
            {provider?.description ??
              "请安装对应适配器后再启用。可先停用或解除 Agent 绑定。"}
            {provider?.documentation && (
              <>
                {" "}
                <a
                  href={provider.documentation}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  官方配置说明 <Icon name="arrow-up-right" size={14} />
                </a>
              </>
            )}
          </p>
          <label>
            渠道名称
            <input
              maxLength={100}
              value={draft.name}
              placeholder={provider?.name ?? "渠道名称"}
              onChange={(event) =>
                setDraft({ ...draft, name: event.target.value })
              }
            />
          </label>
          {provider?.fields.map((field) => (
            <label key={field.key} htmlFor={`channel-field-${field.key}`}>
              {field.label}
              <input
                id={`channel-field-${field.key}`}
                aria-label={field.label}
                aria-describedby={
                  field.help ? `channel-help-${field.key}` : undefined
                }
                type={
                  field.secret
                    ? "password"
                    : field.type === "number"
                      ? "number"
                      : "text"
                }
                autoComplete={field.secret ? "new-password" : "off"}
                required={
                  field.required && (!field.secret || !draft.has_secrets)
                }
                disabled={
                  !!draft.id && provider.identity_fields.includes(field.key)
                }
                value={
                  field.secret
                    ? (secrets[field.key] ?? "")
                    : (draft.config[field.key] ?? "")
                }
                placeholder={
                  field.secret && draft.has_secrets
                    ? "已保存，留空保留原值"
                    : undefined
                }
                onChange={(event) =>
                  field.secret
                    ? setSecrets({
                        ...secrets,
                        [field.key]: event.target.value,
                      })
                    : setDraft({
                        ...draft,
                        config: {
                          ...draft.config,
                          [field.key]: event.target.value,
                        },
                      })
                }
              />
              {field.help && (
                <small id={`channel-help-${field.key}`}>{field.help}</small>
              )}
            </label>
          ))}
          <label htmlFor="channel-agent">绑定 Agent</label>
          <select
            id="channel-agent"
            value={draft.agent_id}
            onChange={(event) =>
              setDraft({ ...draft, agent_id: event.target.value })
            }
          >
            <option value="">暂不绑定</option>
            {agents
              .filter((agent) => !agent.archived)
              .map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.name}
                  {!agent.enabled ? "（已停用）" : ""}
                </option>
              ))}
          </select>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={draft.enabled}
              disabled={!provider}
              onChange={(event) => {
                setDraft({ ...draft, enabled: event.target.checked });
                setConfirmed(false);
              }}
            />
            启用渠道
          </label>
          {draft.enabled && (
            <label className="checkbox-label">
              <input
                type="checkbox"
                required
                checked={confirmed}
                onChange={(event) => setConfirmed(event.target.checked)}
              />
              我确认成员消息、工具及检索内容会发送至绑定 Agent 的模型服务
            </label>
          )}
          <p className="muted">
            保存后密钥不回显。修改绑定前请先停用渠道；停用不取消已提交的 Agent
            任务。
          </p>
        </fieldset>
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <footer>
          <button
            type="button"
            data-dialog-cancel
            disabled={pending}
            onClick={onClose}
          >
            取消
          </button>
          <button className="primary" disabled={pending || !draft.provider}>
            {pending ? "正在保存…" : "保存渠道配置"}
          </button>
        </footer>
      </form>
    </SettingsDialog>
  );
}
export function ChannelsWorkspace({
  request,
  agents,
  onConversation,
}: {
  request: Api;
  agents: Agent[];
  onConversation: (id: string) => Promise<void>;
}) {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [editor, setEditor] = useState<Channel | null>(null);
  const [archive, setArchive] = useState<Channel | null>(null);
  const [archiveError, setArchiveError] = useState("");
  const [pending, setPending] = useState(false);
  const [diagnostics, setDiagnostics] = useState<Diagnostics>(empty);
  const [refresh, setRefresh] = useState(0);
  const current =
    channels.find((channel) => channel.id === selected) ?? channels[0];
  const provider = providers.find((value) => value.id === current?.provider);
  useEffect(() => {
    let active = true;
    setError("");
    void Promise.all([
      request<Provider[]>("/channel-providers"),
      request<Channel[]>("/channels"),
    ])
      .then(([p, c]) => {
        if (active) {
          setProviders(p);
          setChannels(c);
          setSelected((value) =>
            c.some((channel) => channel.id === value)
              ? value
              : (c[0]?.id ?? ""),
          );
        }
      })
      .catch((error) => {
        if (active) setError(note(error));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [refresh]);
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    setDiagnostics(empty);
    async function poll() {
      try {
        const result = await request<Diagnostics>(
          `/channels/${current!.id}/diagnostics`,
        );
        if (active) setDiagnostics(result);
      } catch (error) {
        if (active) setError(note(error));
      } finally {
        if (active) timer = setTimeout(poll, 2000);
      }
    }
    if (current) void poll();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [current?.id, refresh]);
  function create() {
    setFeedback("");
    setError("");
    setEditor({
      id: "",
      name: "",
      provider:
        providers.find((value) => value.id === "wecom-bot")?.id ??
        providers[0]?.id ??
        "",
      config: {},
      agent_id:
        agents.find((agent) => agent.enabled && !agent.archived)?.id ?? "",
      enabled: false,
      archived: false,
      version: 0,
      verified: false,
      has_secrets: false,
      available: true,
      status: empty.status,
    });
  }
  async function test() {
    if (!current) return;
    setPending(true);
    setError("");
    setFeedback("");
    try {
      const result = await request<{ message: string }>(
        `/channels/${current.id}/test`,
        "POST",
      );
      setFeedback(result.message);
    } catch (error) {
      setError(note(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="channels-workspace">
      <header className="plugin-section-heading">
        <div>
          <h2>
            聊天渠道 <span>{channels.length}</span>
          </h2>
          <p>选择渠道适配器，连接聊天平台并绑定 Agent。</p>
        </div>
        <button
          className="primary"
          disabled={pending || loading || !providers.length}
          onClick={create}
        >
          <Icon name="plus" size={16} /> 新增渠道
        </button>
      </header>
      {error && (
        <p className="error plugin-feedback" role="alert">
          {error}
          <button onClick={() => setRefresh((value) => value + 1)}>
            重新读取
          </button>
        </p>
      )}
      {feedback && (
        <p className="success plugin-feedback" role="status">
          {feedback}
        </p>
      )}
      {loading ? (
        <p className="muted" role="status">
          正在读取渠道…
        </p>
      ) : !current ? (
        <div className="plugin-empty">
          <h3>让 Agent 在聊天中回复</h3>
          <p>
            企微智能机器人推荐使用 API 长连接模式。准备 BotID 和
            Secret，即可连接；无需公网回调地址。
          </p>
          <p>已安装的其他适配器也会出现在“新增渠道”中。</p>
        </div>
      ) : (
        <div className="channel-layout">
          <nav className="channel-catalog" aria-label="渠道列表">
            {channels.map((channel) => (
              <button
                key={channel.id}
                disabled={pending}
                aria-current={channel.id === current.id ? "true" : undefined}
                onClick={() => {
                  setSelected(channel.id);
                  setError("");
                  setFeedback("");
                }}
              >
                <strong>{channel.name}</strong>
                <span>
                  {providers.find((value) => value.id === channel.provider)
                    ?.name ?? channel.provider}
                </span>
                <small>
                  {channel.archived
                    ? "已归档"
                    : channel.enabled
                      ? "已启用"
                      : "已停用"}
                </small>
              </button>
            ))}
          </nav>
          <div className="channel-detail" key={current.id}>
            <header>
              <div>
                <h2>{current.name}</h2>
                <p>
                  {provider?.name ?? current.provider} ·{" "}
                  {current.archived
                    ? "已归档"
                    : current.enabled
                      ? "已启用"
                      : "已停用"}
                </p>
              </div>
              <div className="channel-detail-actions">
                <button
                  disabled={pending}
                  onClick={() => setRefresh((value) => value + 1)}
                >
                  刷新渠道状态
                </button>
                {!current.archived && (
                  <button disabled={pending} onClick={() => setEditor(current)}>
                    编辑配置
                  </button>
                )}
              </div>
            </header>
            <div className="channel-route">
              <span>{provider?.name ?? "未安装适配器"}</span>
              <Icon name="arrow-right" size={16} />
              <strong>
                {agents.find((agent) => agent.id === current.agent_id)?.name ??
                  "未绑定 Agent"}
              </strong>
            </div>
            <div className="channel-status">
              <span
                className={`badge ${diagnostics.status.state === "connected" ? "success" : ""}`}
              >
                {current.enabled
                  ? (labels[diagnostics.status.state] ??
                    diagnostics.status.state)
                  : "已停用"}
              </span>
              <p>
                {current.enabled
                  ? diagnostics.status.message
                  : "启用后开始接收消息；历史任务仍可在会话中查看。"}
              </p>
            </div>
            {current.callback_path && (
              <div className="channel-callback">
                <label>
                  回调 URL
                  <input
                    readOnly
                    value={
                      new URL(current.callback_path, window.location.origin)
                        .href
                    }
                  />
                </label>
                <small>
                  {current.verified
                    ? "已收到 URL 验证请求。"
                    : "尚未收到 URL 验证请求。请在消息平台保存回调配置。"}
                </small>
              </div>
            )}
            {!current.archived && (
              <div className="channel-controls">
                <button
                  disabled={pending || !current.available}
                  onClick={() => void test()}
                >
                  {pending ? "正在检查…" : "检查连接"}
                </button>
                <button
                  disabled={pending || current.enabled}
                  onClick={() => {
                    setArchive(current);
                    setArchiveError("");
                  }}
                >
                  归档渠道
                </button>
              </div>
            )}
            <p className="channel-help">
              {provider?.description ?? "适配器未安装，渠道配置和历史保留。"}
              {provider?.transport === "websocket" &&
                " 同一机器人仅允许一条连接，连接测试也会短暂建立连接。"}{" "}
              {provider?.documentation && (
                <a
                  href={provider.documentation}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  配置说明 <Icon name="arrow-up-right" size={14} />
                </a>
              )}
            </p>
            <section className="channel-records">
              <h3>最近入站消息</h3>
              {!diagnostics.inbox.length ? (
                <p className="muted">
                  暂无消息。启用渠道后，从聊天平台发送文字测试。
                </p>
              ) : (
                diagnostics.inbox.map((item) => (
                  <article key={item.id}>
                    <header>
                      <span>{labels[item.state] ?? item.state}</span>
                      <time>{new Date(item.created).toLocaleString()}</time>
                    </header>
                    <p>{item.preview}</p>
                    {item.error && <small>{item.error}</small>}
                    {item.conversation_id && (
                      <button
                        onClick={() =>
                          void onConversation(item.conversation_id).catch(
                            (error) => setError(note(error)),
                          )
                        }
                      >
                        查看会话
                      </button>
                    )}
                  </article>
                ))
              )}
            </section>
            <section className="channel-records">
              <h3>最近发送记录</h3>
              {!diagnostics.outbox.length ? (
                <p className="muted">暂无发送记录。</p>
              ) : (
                diagnostics.outbox.map((item) => (
                  <article key={item.id}>
                    <header>
                      <span>{labels[item.state] ?? item.state}</span>
                      <small>
                        {item.kind === "hint" ? "处理中提示" : "最终回复"} ·{" "}
                        {item.part}/{item.total} · 尝试 {item.attempts} 次
                      </small>
                    </header>
                    {item.error && (
                      <p>
                        {item.error}
                        {item.code ? `（${item.code}）` : ""}
                      </p>
                    )}
                    {item.state === "failed" && !current.archived && (
                      <button
                        disabled={pending}
                        onClick={async () => {
                          setPending(true);
                          try {
                            await request(
                              `/channel-deliveries/${item.id}/retry`,
                              "POST",
                            );
                            setRefresh((value) => value + 1);
                          } catch (error) {
                            setError(note(error));
                          } finally {
                            setPending(false);
                          }
                        }}
                      >
                        重试已拒绝的发送
                      </button>
                    )}
                  </article>
                ))
              )}
            </section>
          </div>
        </div>
      )}
      {editor && (
        <ChannelEditor
          channel={editor}
          providers={providers}
          agents={agents}
          request={request}
          onClose={() => setEditor(null)}
          onSaved={(channel) => {
            setChannels((values) =>
              values.some((value) => value.id === channel.id)
                ? values.map((value) =>
                    value.id === channel.id ? channel : value,
                  )
                : [...values, channel],
            );
            setSelected(channel.id);
            setEditor(null);
            setFeedback("渠道配置已保存。");
            setError("");
            setRefresh((value) => value + 1);
          }}
        />
      )}
      {archive && (
        <SettingsDialog
          title={`归档渠道“${archive.name}”？`}
          pending={pending}
          onClose={() => setArchive(null)}
        >
          <p>归档解除 Agent 绑定，停止未发送的回复，保留已有会话与记录。</p>
          {archiveError && (
            <p className="error" role="alert">
              {archiveError}
            </p>
          )}
          <footer>
            <button
              data-dialog-cancel
              disabled={pending}
              onClick={() => setArchive(null)}
            >
              取消
            </button>
            <button
              className="danger-button"
              disabled={pending}
              onClick={async () => {
                setPending(true);
                try {
                  await request(`/channels/${archive.id}`, "DELETE");
                  setArchive(null);
                  setRefresh((value) => value + 1);
                } catch (error) {
                  setArchiveError(note(error));
                } finally {
                  setPending(false);
                }
              }}
            >
              确认归档
            </button>
          </footer>
        </SettingsDialog>
      )}
    </section>
  );
}
