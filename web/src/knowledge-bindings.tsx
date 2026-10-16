import { useEffect, useState } from "react";
type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
export function KnowledgeBindings({
  request,
  agentID,
  agentName,
  readOnly,
}: {
  request: Api;
  agentID: string;
  agentName: string;
  readOnly: boolean;
}) {
  const [bases, setBases] = useState<{ id: string; name: string }[]>([]);
  const [bound, setBound] = useState<string[]>([]);
  const [confirmed, setConfirmed] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  useEffect(() => {
    let active = true;
    void Promise.all([
      request<{ id: string; name: string }[]>("/knowledge/bases"),
      request<{ knowledge_ids: string[] }>(`/agents/${agentID}/knowledge`),
    ])
      .then(([bases, binding]) => {
        if (active) {
          setBases(bases);
          setBound(binding.knowledge_ids);
        }
      })
      .catch((error) => {
        if (active)
          setError(
            error instanceof Error
              ? error.message
              : "无法读取知识库关联，请重试。",
          );
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [agentID]);
  async function save() {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      const result = await request<{ knowledge_ids: string[] }>(
        `/agents/${agentID}/knowledge`,
        "PUT",
        { knowledge_ids: bound, confirm: confirmed },
      );
      setBound(result.knowledge_ids);
      setDirty(false);
      setConfirmed(false);
      setFeedback("知识库关联已保存，新提交的任务将使用这些知识库。");
    } catch (error) {
      setError(
        error instanceof Error ? error.message : "保存未完成，请稍后重试。",
      );
    } finally {
      setPending(false);
    }
  }
  return (
    <section className="agent-bindings knowledge-bindings">
      <h3>{agentName}可检索的知识库</h3>
      <p className="muted">
        选择此 Agent 可访问的资料。创建、上传和检索测试统一在
        <a href="#/settings/knowledge">知识库</a>管理。
      </p>
      {loading ? (
        <p role="status">正在读取关联…</p>
      ) : !bases.length ? (
        <p className="permission-notice">
          尚无知识库，请先在知识库配置中创建。
        </p>
      ) : (
        <fieldset disabled={readOnly || pending || loading}>
          {bases.map((base) => (
            <label className="checkbox-label" key={base.id}>
              <input
                type="checkbox"
                checked={bound.includes(base.id)}
                onChange={(event) => {
                  setBound((values) =>
                    event.target.checked
                      ? [...values, base.id]
                      : values.filter((id) => id !== base.id),
                  );
                  setDirty(true);
                  setConfirmed(false);
                  setFeedback("");
                }}
              />
              {base.name}
            </label>
          ))}
        </fieldset>
      )}
      {dirty && bound.length > 0 && (
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={confirmed}
            disabled={readOnly || pending}
            onChange={(event) => setConfirmed(event.target.checked)}
          />
          我确认检索片段可发送给当前模型服务
        </label>
      )}
      <button
        className="primary"
        disabled={
          loading ||
          readOnly ||
          pending ||
          !dirty ||
          (bound.length > 0 && !confirmed)
        }
        onClick={() => void save()}
      >
        保存知识库关联
      </button>
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
    </section>
  );
}
