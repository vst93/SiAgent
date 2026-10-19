import {
  AgentPolicySettings,
  defaultAgentPolicy,
  type AgentPolicy,
} from "./agent-policy";
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
  input: string[];
};
export type Model = {
  options: ModelOptions;
  provider: string;
  id: number;
  base_url: string;
  name: string;
  has_key: boolean;
  version: number;
  archived: boolean;
};
export type Agent = {
  policy: AgentPolicy;
  advanced_tools_available?: boolean;
  id: string;
  name: string;
  instructions: string;
  enabled: boolean;
  fallback_model_ids: number[];
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
    policy: defaultAgentPolicy,
    id: "",
    name: "",
    instructions: "你是一位简洁、可靠的助手。请使用用户的语言回答。",
    enabled: true,
    model_id: models.find((model) => !model.archived)?.id ?? 1,
    fallback_model_ids: [],
    archived: false,
  };
  const [editor, setEditor] = useState<Agent>(agents[0] ?? blank);
  const [tab, setTab] = useState<
    "basic" | "session" | "context" | "output" | "knowledge" | "tools"
  >("basic");
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [pending, setPending] = useState(false);
  const [advancedConfirmed, setAdvancedConfirmed] = useState(false);
  const [fallbackPick, setFallbackPick] = useState("");
  const savedAgent = agents.find((agent) => agent.id === editor.id);
  const advancedNeedsConfirm =
    editor.policy.advanced_tools &&
    !savedAgent?.policy.advanced_tools &&
    !advancedConfirmed;
  function selectTab(next: typeof tab) {
    setTab(next);
    setFeedback("");
    setError("");
  }
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
          policy: editor.policy,
          name: editor.name,
          instructions: editor.instructions,
          enabled: editor.enabled,
          model_id: editor.model_id,
          fallback_model_ids: editor.fallback_model_ids,
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
            <button
              className="agent-card-action"
              disabled={pending}
              onClick={() => onSelect(agent.id)}
            >
              <Icon name={agent.archived ? "history" : "chat"} size={14} />
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
            onClick={() => selectTab("basic")}
          >
            基本信息
          </button>
          {(
            [
              ["session", "会话限制"],
              ["context", "上下文"],
              ["output", "回复输出"],
            ] as const
          ).map(([key, label]) => (
            <button
              key={key}
              aria-pressed={tab === key}
              onClick={() => selectTab(key)}
            >
              {label}
            </button>
          ))}
          <button
            disabled={!editor.id}
            aria-pressed={tab === "knowledge"}
            onClick={() => selectTab("knowledge")}
          >
            知识库关联
          </button>
          <button
            disabled={!editor.id}
            aria-pressed={tab === "tools"}
            onClick={() => selectTab("tools")}
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
                {tab === "basic" ? (
                  <>
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
                            fallback_model_ids: editor.fallback_model_ids.filter(
                              (id) => id !== Number(event.target.value),
                            ),
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
                    <div className="agent-fallback">
                      <span className="agent-fallback-label">兜底模型</span>
                      <p className="muted">
                        主模型在产生回答或调用工具前失败时，按顺序尝试。
                      </p>
                      <div className="agent-fallback-list">
                        {(editor.fallback_model_ids ?? []).map((id, index) => {
                          const item = models.find((model) => model.id === id);
                          return (
                            <div className="agent-fallback-item" key={id}>
                              <span>{index + 1}. {item?.name ?? `#${id}`}</span>
                              <button
                                type="button"
                                disabled={index === 0}
                                aria-label={`上移 ${item?.name ?? id}`}
                                onClick={() => {
                                  const next = [...editor.fallback_model_ids];
                                  [next[index - 1], next[index]] = [next[index], next[index - 1]];
                                  setEditor({ ...editor, fallback_model_ids: next });
                                }}
                              >
                                上移
                              </button>
                              <button
                                type="button"
                                disabled={index === editor.fallback_model_ids.length - 1}
                                aria-label={`下移 ${item?.name ?? id}`}
                                onClick={() => {
                                  const next = [...editor.fallback_model_ids];
                                  [next[index + 1], next[index]] = [next[index], next[index + 1]];
                                  setEditor({ ...editor, fallback_model_ids: next });
                                }}
                              >
                                下移
                              </button>
                              <button
                                type="button"
                                aria-label={`移除 ${item?.name ?? id}`}
                                onClick={() =>
                                  setEditor({
                                    ...editor,
                                    fallback_model_ids: editor.fallback_model_ids.filter(
                                      (value) => value !== id,
                                    ),
                                  })
                                }
                              >
                                移除
                              </button>
                            </div>
                          );
                        })}
                        {!(editor.fallback_model_ids ?? []).length && (
                          <p className="muted">尚未配置兜底模型。</p>
                        )}
                      </div>
                      <div className="agent-fallback-add">
                        <select
                          aria-label="添加兜底模型"
                          value={fallbackPick}
                          onChange={(event) => setFallbackPick(event.target.value)}
                        >
                          <option value="">选择模型</option>
                          {models
                            .filter(
                              (model) =>
                                !model.archived &&
                                model.id !== editor.model_id &&
                                !editor.fallback_model_ids.includes(model.id),
                            )
                            .map((model) => (
                              <option key={model.id} value={model.id}>
                                {model.name}
                              </option>
                            ))}
                        </select>
                        <button
                          type="button"
                          disabled={!fallbackPick || (editor.fallback_model_ids ?? []).length >= 5}
                          onClick={() => {
                            if (!fallbackPick) return;
                            setEditor({
                              ...editor,
                              fallback_model_ids: [
                                ...editor.fallback_model_ids,
                                Number(fallbackPick),
                              ],
                            });
                            setFallbackPick("");
                          }}
                        >
                          添加
                        </button>
                      </div>
                    </div>
                    <label htmlFor="agent-role">角色指令</label>
                    <textarea
                      id="agent-role"
                      rows={6}
                      maxLength={20000}
                      value={editor.instructions}
                      onChange={(event) =>
                        setEditor({
                          ...editor,
                          instructions: event.target.value,
                        })
                      }
                    />
                    <label>
                      时区
                      <input
                        required
                        list="agent-timezones"
                        maxLength={128}
                        value={editor.policy.timezone}
                        onChange={(event) =>
                          setEditor({ ...editor, policy: { ...editor.policy, timezone: event.target.value } })
                        }
                        placeholder="Asia/Shanghai"
                      />
                    </label>
                    <datalist id="agent-timezones">
                      <option value="Asia/Shanghai">北京时间</option>
                      <option value="Asia/Hong_Kong">香港时间</option>
                      <option value="Asia/Tokyo">东京时间</option>
                      <option value="America/New_York">纽约时间</option>
                      <option value="Europe/London">伦敦时间</option>
                      <option value="UTC">协调世界时</option>
                    </datalist>
                    <p className="muted">
                      自动向助手提供每轮消息的接收时间，用于理解“今天”“明天”等相对日期。
                    </p>
                    <label className="checkbox-label">
                      <input
                        type="checkbox"
                        checked={editor.enabled}
                        onChange={(event) =>
                          setEditor({
                            ...editor,
                            enabled: event.target.checked,
                          })
                        }
                      />
                      允许接收新任务
                    </label>
                    <p className="muted">
                      停用后拒绝新消息，已有任务继续执行；可在对应会话单独取消。
                    </p>
                    {editor.advanced_tools_available === false ? (
                      <p className="permission-notice">
                        当前部署已通过 SIAGENT_DISABLE_ADVANCED_TOOLS 禁用高级工具，无法为任何助手开启。
                      </p>
                    ) : (
                      <>
                        <label className="checkbox-label">
                          <input
                            type="checkbox"
                            checked={editor.policy.advanced_tools}
                            onChange={(event) => {
                              setAdvancedConfirmed(false);
                              setEditor({
                                ...editor,
                                policy: {
                                  ...editor.policy,
                                  advanced_tools: event.target.checked,
                                },
                              });
                            }}
                          />
                          允许此 Agent 使用本地工作区工具（read / write / edit /
                          bash）
                        </label>
                        {editor.policy.advanced_tools && (
                          <>
                            <p className="permission-notice">
                              这些工具可以读写服务器文件并执行命令。工作目录约定不是操作系统沙箱；对外客服、纯知识库问答建议保持关闭。开启与关闭都会写入审计记录。
                            </p>
                            <label className="checkbox-label">
                              <input
                                type="checkbox"
                                checked={advancedConfirmed}
                                onChange={(event) =>
                                  setAdvancedConfirmed(event.target.checked)
                                }
                              />
                              我了解风险，仅对可信 Agent 开启
                            </label>
                          </>
                        )}
                      </>
                    )}
                  </>
                ) : (
                  <AgentPolicySettings
                    section={tab}
                    value={editor.policy}
                    onChange={(policy) => setEditor({ ...editor, policy })}
                  />
                )}
                {!models.some((model) => !model.archived) && (
                  <p className="permission-notice">
                    请先在 <a href="#/settings/models">模型连接</a>
                    中保存一个可用连接，再创建 Agent。
                  </p>
                )}
                <button
                  className="primary"
                  disabled={
                    !models.some((model) => !model.archived) ||
                    advancedNeedsConfirm
                  }
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
