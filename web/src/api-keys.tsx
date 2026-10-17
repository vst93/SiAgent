import { useEffect, useState } from "react";
import type { Agent } from "./agents";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Key = {
  id: string;
  prefix: string;
  name: string;
  scopes: string[];
  agent_id: string;
  knowledge_id: string;
  rate_limit: number;
  expires: number;
  revoked: boolean;
  created: number;
  last_used: number;
};

const scopes = [
  ["agent:run", "发起 Agent 任务"],
  ["agent:read", "读取任务与会话"],
  ["knowledge:write", "推送与删除文档"],
  ["knowledge:search", "检索与读取来源"],
  ["webhook:manage", "管理 Webhook"],
] as const;

export function ApiKeysWorkspace({
  request,
  agents,
}: {
  request: Api;
  agents: Agent[];
}) {
  const [keys, setKeys] = useState<Key[]>([]);
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<string[]>(["agent:run", "agent:read"]);
  const [agentID, setAgentID] = useState("");
  const [rate, setRate] = useState(60);
  const [days, setDays] = useState(0);
  const [token, setToken] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");

  async function load() {
    setKeys(await request<Key[]>("/api-keys"));
  }
  useEffect(() => {
    void load().catch((error) =>
      setError(error instanceof Error ? error.message : "密钥读取失败。"),
    );
  }, []);

  async function create() {
    setPending(true);
    setError("");
    setFeedback("");
    setToken("");
    try {
      const value = await request<{ key: string }>("/api-keys", "POST", {
        name,
        scopes: selected,
        agent_id: agentID,
        rate_limit: rate,
        expires_days: days,
      });
      setToken(value.key);
      setName("");
      setFeedback("密钥已创建。完整密钥只显示这一次，请立即保存。");
      await load();
    } catch (error) {
      setError(error instanceof Error ? error.message : "密钥创建失败。");
    } finally {
      setPending(false);
    }
  }

  async function revoke(id: string) {
    setPending(true);
    setError("");
    try {
      await request(`/api-keys/${id}`, "DELETE");
      await load();
      setFeedback("密钥已吊销。");
    } catch (error) {
      setError(error instanceof Error ? error.message : "吊销失败。");
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="apikeys-workspace">
      <p className="kb-help">
        集成 API 使用 <code>Authorization: Bearer &lt;key&gt;</code>
        ，与管理后台的 Cookie 登录互不影响。密钥只保存哈希，创建后无法再次查看。接口路径以 <code>/v1</code> 开头，适合业务系统、工单或内部工具调用。
      </p>
      {error && (
        <p className="kb-feedback error" role="alert">
          {error}
        </p>
      )}
      {feedback && (
        <p className="kb-feedback success" role="status">
          {feedback}
        </p>
      )}
      {token && (
        <div className="apikey-token">
          <strong>新密钥（仅显示一次）</strong>
          <code>{token}</code>
          <small>关闭或刷新页面后无法再次获取；如丢失请吊销并重新创建。</small>
        </div>
      )}

      <section className="apikeys-section">
        <h2>创建密钥</h2>
        <div className="apikey-form">
          <label>
            名称
            <input
              maxLength={100}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="例如：工单系统"
            />
          </label>
          <label>
            限定 Agent
            <select
              aria-label="限定 Agent"
              value={agentID}
              onChange={(event) => setAgentID(event.target.value)}
            >
              <option value="">全部 Agent</option>
              {agents.map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            每分钟请求上限
            <input
              type="number"
              min={1}
              max={6000}
              value={rate}
              onChange={(event) => setRate(Number(event.target.value))}
            />
          </label>
          <label>
            有效期（天，0 表示不过期）
            <input
              type="number"
              min={0}
              max={3650}
              value={days}
              onChange={(event) => setDays(Number(event.target.value))}
            />
          </label>
        </div>
        <fieldset className="apikey-scopes">
          <legend>作用域</legend>
          {scopes.map(([value, label]) => (
            <label key={value} className="checkbox-label">
              <input
                type="checkbox"
                checked={selected.includes(value)}
                onChange={(event) =>
                  setSelected((values) =>
                    event.target.checked
                      ? [...values, value]
                      : values.filter((item) => item !== value),
                  )
                }
              />
              {label} <code>{value}</code>
            </label>
          ))}
        </fieldset>
        <div className="form-actions">
          <button
            className="primary"
            disabled={pending || !name.trim() || !selected.length}
            onClick={() => void create()}
          >
            {pending ? "正在创建…" : "创建密钥"}
          </button>
        </div>
      </section>

      <section className="apikeys-section">
        <h2>已有密钥</h2>
        {!keys.length && <p className="muted">还没有创建集成密钥。</p>}
        {keys.map((key) => (
          <article className="apikey-row" key={key.id}>
            <div>
              <strong>{key.name}</strong>
              <small>
                {key.prefix}… · {key.scopes.join(" ")}
                {key.agent_id ? ` · Agent ${key.agent_id}` : ""}
                {key.expires
                  ? ` · ${new Date(key.expires * 1000).toLocaleDateString("zh-CN")} 过期`
                  : " · 不过期"}
                {key.revoked ? " · 已吊销" : ""}
              </small>
            </div>
            <button
              className="danger-button"
              disabled={pending || key.revoked}
              onClick={() => void revoke(key.id)}
            >
              吊销
            </button>
          </article>
        ))}
      </section>
    </div>
  );
}
