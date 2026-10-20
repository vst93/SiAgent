import { useEffect, useState } from "react";
import { Icon } from "./icons";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type LinkedAgent = { id: string; name: string; archived: boolean };

export function LinkedAgents({
  request,
  kind,
  id,
}: {
  request: Api;
  kind: "skills" | "mcps";
  id: string;
}) {
  const [agents, setAgents] = useState<LinkedAgent[]>([]);
  const [error, setError] = useState("");
  const [pending, setPending] = useState("");
  useEffect(() => {
    let active = true;
    void request<LinkedAgent[]>(`/${kind}/${id}/agents`)
      .then((values) => {
        if (active) setAgents(values);
      })
      .catch((failure) => {
        if (active)
          setError(
            failure instanceof Error ? failure.message : "无法读取关联 Agent。",
          );
      });
    return () => {
      active = false;
    };
  }, [kind, id]);
  async function unlink(agent: LinkedAgent) {
    setPending(agent.id);
    setError("");
    try {
      await request(`/${kind}/${id}/agents/${agent.id}`, "DELETE");
      setAgents((values) => values.filter((value) => value.id !== agent.id));
    } catch (failure) {
      setError(
        failure instanceof Error ? failure.message : "取消关联失败。",
      );
    } finally {
      setPending("");
    }
  }
  return (
    <div className="linked-agents">
      <div className="linked-agents-head">
        <strong>关联 Agent</strong>
        <span>{agents.length}</span>
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {!agents.length && !error && (
        <p className="muted">尚未关联 Agent。</p>
      )}
      {!!agents.length && (
        <ul>
          {agents.map((agent) => (
            <li key={agent.id}>
              <span>
                {agent.name}
                {agent.archived ? "（已归档）" : ""}
              </span>
              <button
                type="button"
                className="linked-agent-unlink"
                disabled={pending === agent.id}
                onClick={() => void unlink(agent)}
                aria-label={`取消 ${agent.name} 的关联`}
                title="取消关联"
              >
                <Icon name="close" size={14} />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
