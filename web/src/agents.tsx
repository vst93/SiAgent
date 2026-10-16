import { Icon } from "./icons";
import { CapabilitySettings } from "./capabilities";
import { KnowledgeBindings } from "./knowledge-bindings";
import { FormEvent, useState } from "react";
export type ModelOptions = {
  display_name: string;
  api: "openai-completions" | "openai-responses" | "anthropic-messages";
  context_window: number;
  max_tokens: number;
  reasoning: boolean;
  thinking_level: "off" | "minimal" | "low" | "medium" | "high";
  temperature: number | null;
  top_p: number | null;
  max_tokens_field: "" | "max_tokens" | "max_completion_tokens";
};
export type Model = {
  options: ModelOptions;
  id: number;
  base_url: string;
  name: string;
  has_key: boolean;
  version: number;
  archived: boolean;
};
export type Agent = {
  id: string;
  name: string;
  instructions: string;
  enabled: boolean;
  model_id: number;
  archived: boolean;
};
type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作未完成，请稍后重试。";
export function AgentWorkspace({
  request,
  agents,
  models,
  onSaved,
  onSelect,
}: {
  request: Api;
  agents: Agent[];
  models: Model[];
  onSaved: () => Promise<void>;
  onSelect: (id: string) => void;
}) {
  const blank = {
    id: "",
    name: "",
    instructions: "你是一位简洁、可靠的助手。请使用用户的语言回答。",
    enabled: true,
    model_id: models.find((model) => !model.archived)?.id ?? 1,
    archived: false,
  };
  const [editor, setEditor] = useState<Agent>(agents[0] ?? blank);
  const [tab, setTab] = useState<"basic" | "knowledge" | "tools">("basic");
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [pending, setPending] = useState(false);
  async function action(work: () => Promise<void>) {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      await work();
      await onSaved();
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }
  async function saveAgent(event: FormEvent) {
    event.preventDefault();
    await action(async () => {
      const agent = await request<Agent>(
        editor.id ? `/agents/${editor.id}` : "/agents",
        editor.id ? "PUT" : "POST",
        {
          name: editor.name,
          instructions: editor.instructions,
          enabled: editor.enabled,
          model_id: editor.model_id,
        },
      );
      setEditor(agent);
      setFeedback("助手已保存，新任务使用新配置；已有任务保持提交时的配置。");
    });
  }
  return (
    <div className="agents-workspace">
      <section className="agents-list">
        <div className="knowledge-section-head">
          <h2>Agent 列表</h2>
          <button
            disabled={pending}
            onClick={() => {
              setEditor(blank);
              setTab("basic");
              setError("");
              setFeedback("");
            }}
          >
            新建助手
          </button>
        </div>
        {agents.map((agent) => (
          <div
            className={`agent-card ${editor.id === agent.id ? "selected" : ""}`}
            key={agent.id}
          >
            <button
              className="agent-card-select"
              disabled={pending}
              onClick={() => {
                setEditor(agent);
                setTab("basic");
                setError("");
                setFeedback("");
              }}
            >
              <strong>{agent.name}</strong>
              <small>
                {agent.archived
                  ? "已归档"
                  : agent.enabled
                    ? "接收新任务"
                    : "已停用"}{" "}
                ·{" "}
                {models.find((model) => model.id === agent.model_id)?.name ??
                  "模型不可用"}
              </small>
            </button>
            <button disabled={pending} onClick={() => onSelect(agent.id)}>
              {agent.archived ? "查看历史" : "进入对话"}
            </button>
          </div>
        ))}
        <p className="muted">
          每个 Agent 独立选择模型、Skills、MCP
          工具和知识库。默认助手保留，可以停用。
        </p>
      </section>
      <section className="agent-editor">
        <h2>{editor.id ? editor.name : "新建助手"}</h2>
        <nav className="editor-tabs" aria-label="Agent 配置">
          <button
            aria-pressed={tab === "basic"}
            onClick={() => setTab("basic")}
          >
            基本信息
          </button>
          <button
            disabled={!editor.id}
            aria-pressed={tab === "knowledge"}
            onClick={() => setTab("knowledge")}
          >
            知识库关联
          </button>
          <button
            disabled={!editor.id}
            aria-pressed={tab === "tools"}
            onClick={() => setTab("tools")}
          >
            工具授权
          </button>
        </nav>
        {tab === "knowledge" ? (
          <KnowledgeBindings
            key={editor.id}
            request={request}
            agentID={editor.id}
            agentName={editor.name}
            readOnly={editor.archived}
          />
        ) : tab === "tools" ? (
          <CapabilitySettings
            key={editor.id}
            request={request}
            agentID={editor.id}
            agentName={editor.name}
            readOnly={editor.archived}
          />
        ) : (
          <>
            <form onSubmit={saveAgent}>
              <fieldset disabled={pending || editor.archived}>
                <label>
                  助手名称
                  <input
                    required
                    maxLength={100}
                    value={editor.name}
                    onChange={(event) =>
                      setEditor({ ...editor, name: event.target.value })
                    }
                  />
                </label>
                <label>
                  使用模型
                  <select
                    required
                    aria-label="使用模型"
                    value={editor.model_id}
                    onChange={(event) =>
                      setEditor({
                        ...editor,
                        model_id: Number(event.target.value),
                      })
                    }
                  >
                    {models
                      .filter(
                        (model) =>
                          !model.archived || model.id === editor.model_id,
                      )
                      .map((model) => (
                        <option key={model.id} value={model.id}>
                          {model.name} · {model.base_url}
                          {model.archived ? "（已归档）" : ""}
                        </option>
                      ))}
                  </select>
                </label>
                <label htmlFor="agent-role">角色指令</label>
                <textarea
                  id="agent-role"
                  rows={6}
                  maxLength={20000}
                  value={editor.instructions}
                  onChange={(event) =>
                    setEditor({ ...editor, instructions: event.target.value })
                  }
                />
                <label className="checkbox-label">
                  <input
                    type="checkbox"
                    checked={editor.enabled}
                    onChange={(event) =>
                      setEditor({ ...editor, enabled: event.target.checked })
                    }
                  />
                  允许接收新任务
                </label>
                <p className="muted">
                  停用后拒绝新消息，已有任务继续执行；可在对应会话单独取消。
                </p>
                {!models.some((model) => !model.archived) && (
                  <p className="permission-notice">
                    请先在 <a href="#/settings/models">模型连接</a>
                    中保存一个可用连接，再创建 Agent。
                  </p>
                )}
                <button
                  className="primary"
                  disabled={!models.some((model) => !model.archived)}
                >
                  {editor.id ? "保存助手" : "创建助手"}
                </button>
              </fieldset>
            </form>
            {editor.archived ? (
              <p className="permission-notice">
                此助手已归档，只能查看历史。已有会话不会改绑到其他助手。
              </p>
            ) : (
              editor.id &&
              editor.id !== "default" && (
                <details className="agent-archive">
                  <summary>
                    <Icon
                      name="caret-right"
                      size={14}
                      className="disclosure-icon"
                    />
                    归档助手
                  </summary>
                  <p>
                    须先保存停用状态并解除渠道绑定。归档保留会话和任务历史，无法恢复或继续提交新消息。
                  </p>
                  <button
                    disabled={pending}
                    onClick={() =>
                      void action(async () => {
                        await request(`/agents/${editor.id}`, "DELETE");
                        setEditor({
                          ...editor,
                          archived: true,
                          enabled: false,
                        });
                        setFeedback("助手已归档，历史仍可查看。");
                      })
                    }
                  >
                    确认归档助手
                  </button>
                </details>
              )
            )}
          </>
        )}
      </section>
      {error && (
        <p className="error agents-feedback" role="alert">
          {error}
        </p>
      )}
      {feedback && (
        <p className="success agents-feedback" role="status">
          {feedback}
        </p>
      )}
    </div>
  );
}
