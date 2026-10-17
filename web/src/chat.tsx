import type { AgentPolicy } from "./agent-policy";
import { ToolCallDetails } from "./capabilities";
import { Icon } from "./icons";
import { MessageMarkdown } from "./message-markdown";
import { FormEvent, useEffect, useRef, useState } from "react";
import type { Agent, Model } from "./agents";
import type { KnowledgeHit } from "./knowledge";
import { ConversationMaintenance, RunDiagnostics } from "./runtime-controls";
export type Conversation = {
  id: string;
  title: string;
  created: number;
  agent_id: string;
  channel_id?: string;
};
export type Run = {
  output?: AgentPolicy["output"];
  thinking?: string;
  tools?: { id: string; name: string; display_name?: string; status: string }[];
  id: string;
  status: string;
  error: string;
  created: number;
};
export type Snapshot = {
  sources: (KnowledgeHit & { run_id: string })[];
  conversation: Conversation;
  messages: {
    id: number;
    run_id: string;
    role: string;
    content: string;
    created: number;
  }[];
  runs: Run[];
};
const statusNames: Record<string, string> = {
  queued: "排队中",
  running: "正在回答",
  cancelling: "正在取消",
  completed: "已完成",
  failed: "未完成",
  cancelled: "已取消",
  interrupted: "已中断",
};
const toolStatusNames: Record<string, string> = {
  running: "执行中",
  completed: "已完成",
  failed: "失败",
  interrupted: "结果不明，请核对",
};
const active = (run: Run) =>
  run.status === "running" || run.status === "cancelling";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
export function ChatWorkspace({
  agent,
  model,
  agents,
  agentID,
  conversations,
  selected,
  snapshot,
  loading,
  ready,
  pending,
  input,
  setInput,
  send,
  cancel,
  selectAgent,
  selectConversation,
  newConversation,
  onDeleted,
  onConfigure,
  request,
  connectionLost,
  maintenanceFeedback,
}: {
  agent: Agent | null;
  model: Model | null;
  agents: Agent[];
  agentID: string;
  conversations: Conversation[];
  selected: string;
  snapshot: Snapshot | null;
  loading: boolean;
  ready: boolean;
  pending: boolean;
  input: string;
  setInput: (value: string) => void;
  send: (event: FormEvent) => Promise<void>;
  cancel: (id: string) => Promise<void>;
  selectAgent: (id: string) => void;
  selectConversation: (id: string) => void;
  newConversation: () => void;
  onDeleted: () => void;
  onConfigure: () => void;
  request: Api;
  connectionLost: boolean;
  maintenanceFeedback: string;
}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const nearBottom = useRef(true);
  const conversation =
    snapshot?.conversation ?? conversations.find((item) => item.id === selected);
  const channelConversation = Boolean(conversation?.channel_id);
  const fetchingConversation = Boolean(selected && !snapshot);
  const currentRun = snapshot?.runs.find(active);
  const waitingCount =
    snapshot?.runs.filter((run) => run.status === "queued").length ?? 0;
  const blocked =
    pending ||
    !ready ||
    agent?.policy.session.types === "group" ||
    channelConversation ||
    agent?.enabled === false ||
    agent?.archived === true ||
    (Boolean(selected) && !snapshot);
  useEffect(() => {
    const element = inputRef.current;
    if (element) {
      element.style.height = "auto";
      element.style.height = `${Math.min(element.scrollHeight, 160)}px`;
    }
  }, [input, loading, model?.id]);
  useEffect(() => {
    nearBottom.current = true;
  }, [selected]);
  useEffect(() => {
    if (nearBottom.current && scrollRef.current)
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
  }, [snapshot]);
  return (
    <section className="chat-workspace" aria-label="Agent 对话工作区">
      <header className="chat-header">
        <div className="chat-heading">
          <h1>{conversation?.title ?? "开始一段对话"}</h1>
          {channelConversation && (
            <span className="channel-view-label">
              <Icon name="channels" size={14} />渠道对话 · 只读
            </span>
          )}
        </div>
        <div className="chat-header-actions">
          <label className="chat-agent-select">
            <span className="sr-only">当前 Agent</span>
            <select
              disabled={loading || pending}
              aria-label="选择助手"
              value={agentID}
              onChange={(event) => selectAgent(event.target.value)}
            >
              {!agents.length && (
                <option value="default">尚未创建 Agent</option>
              )}
              {agents.map((value) => (
                <option value={value.id} key={value.id}>
                  {value.name}
                  {value.archived
                    ? "（已归档）"
                    : !value.enabled
                      ? "（已停用）"
                      : ""}
                </option>
              ))}
            </select>
          </label>
          <button
            className="new-chat"
            onClick={newConversation}
            disabled={
              !model ||
              loading ||
              pending ||
              !agent?.enabled ||
              agent.archived ||
              agent.policy.session.types === "group"
            }
          >
            <Icon name="plus" size={16} />
            新对话
          </button>
          {snapshot && (
            <ConversationMaintenance
              key={snapshot.conversation.id}
              request={request}
              id={snapshot.conversation.id}
              onDeleted={onDeleted}
            />
          )}
          <button
            className="history-toggle"
            aria-expanded={historyOpen}
            aria-controls="chat-history"
            onClick={() => setHistoryOpen(!historyOpen)}
          >
            <Icon name="history" size={16} />
            <span>对话记录</span>
          </button>
        </div>
      </header>
      <aside
        id="chat-history"
        className={`chat-history-panel ${historyOpen ? "is-open" : ""}`}
      >
        <div className="chat-history-heading">
          <span>对话记录</span>
          <span>{conversations.length}</span>
        </div>
        <nav aria-label="对话记录" className="conversation-list">
          {conversations.map((item) => (
            <button
              className={
                selected === item.id ? "conversation selected" : "conversation"
              }
              aria-current={selected === item.id ? "page" : undefined}
              key={item.id}
              title={item.title}
              onClick={() => {
                selectConversation(item.id);
                setHistoryOpen(false);
              }}
            >
              <Icon name="chat" size={16} className="chat-glyph" />
              <span>{item.title}</span>
              {item.channel_id && <small className="channel-tag">渠道</small>}
            </button>
          ))}
          {!conversations.length && (
            <p className="nav-empty">开始对话后，记录会保存在这里。</p>
          )}
        </nav>
        <div className="chat-sidebar-status">
          <span className={ready ? "status-dot" : "status-dot offline"} />
          <span>{ready ? "服务已就绪" : "服务正在恢复"}</span>
        </div>
      </aside>
      <div className="chat-main">
        {connectionLost && (
          <p className="workspace-alert muted" role="status">
            连接暂时中断，正在重新连接。已保存的内容仍然保留。
          </p>
        )}
        {maintenanceFeedback && (
          <p className="context-warning" role="status">
            {maintenanceFeedback}
          </p>
        )}
        {loading || fetchingConversation ? (
          <div className="empty-view" role="status">
            正在读取对话…
          </div>
        ) : !model ? (
          <div className="empty-view">
            <span className="assistant-symbol" aria-hidden="true">
              S·
            </span>
            <h2>先连接一个模型</h2>
            <p className="muted">
              在配置中连接模型，默认 Agent 就可以开始与你对话。
            </p>
            <button className="primary" onClick={onConfigure}>
              前往模型配置
            </button>
          </div>
        ) : (
          <>
            <div
              className="message-scroll"
              ref={scrollRef}
              onScroll={() => {
                const element = scrollRef.current;
                if (element)
                  nearBottom.current =
                    element.scrollHeight -
                      element.scrollTop -
                      element.clientHeight <
                    100;
              }}
            >
              {!snapshot?.messages.length ? (
                <div className="empty-view">
                  <span className="assistant-symbol" aria-hidden="true">
                    S·
                  </span>
                  <p className="eyebrow">{agent?.name}</p>
                  <h2>从一个问题开始</h2>
                  <p className="muted">
                    写下你的问题，或告诉助手你想完成什么。
                    <br />
                    对话会保存在这个实例中。
                  </p>
                  {agent?.enabled === false && (
                    <p className="error">
                      {agent.archived
                        ? "助手已归档，仅可查看历史。"
                        : "助手已停用，请在设置中重新启用。"}
                    </p>
                  )}
                </div>
              ) : (
                <div className="messages">
                  {snapshot.messages.map((message) => {
                    const messageTime = new Date(message.created);
                    const run = snapshot.runs.find(
                      (run) => run.id === message.run_id,
                    );
                    return (
                      <article
                        key={message.id}
                        className={`message ${message.role}`}
                      >
                        <div className="message-avatar" aria-hidden="true">
                          {message.role === "user" ? (
                            channelConversation ? <Icon name="channels" size={16} /> : "你"
                          ) : "S·"}
                        </div>
                        <div className="message-body">
                          <div className="message-meta">
                            <strong>
                              {message.role === "user"
                                ? channelConversation ? "渠道用户" : "你"
                                : "助手"}
                            </strong>
                            <time
                              dateTime={messageTime.toISOString()}
                              title={messageTime.toLocaleString("zh-CN", {
                                timeZoneName: "short",
                              })}
                            >
                              {messageTime.toLocaleString(
                                "zh-CN",
                                {
                                  year: "numeric", month: "2-digit", day: "2-digit",
                                  hour: "2-digit", minute: "2-digit", hourCycle: "h23",
                                },
                              )}
                            </time>
                          </div>
                          {message.role === "assistant" && run?.thinking && (
                            <details className="thinking-output">
                              <summary>
                                <Icon
                                  name="caret-right"
                                  size={14}
                                  className="disclosure-icon"
                                />
                                思考摘要
                              </summary>
                              <MessageMarkdown content={run.thinking} />
                            </details>
                          )}
                          <div className="message-text">
                            <MessageMarkdown
                              content={
                                message.content ||
                                (run?.status === "queued"
                                  ? "已排队，等待前面的任务完成。"
                                  : run && active(run)
                                    ? "正在准备回答…"
                                    : "本次运行未产生文本内容。")
                              }
                            />
                          </div>
                          {message.role === "assistant" &&
                            (snapshot.sources ?? []).some(
                              (source) => source.run_id === message.run_id,
                            ) && (
                              <nav
                                className="answer-sources"
                                aria-label="本轮检索来源"
                              >
                                <strong>本轮检索来源</strong>
                                {(snapshot.sources ?? [])
                                  .filter(
                                    (source) =>
                                      source.run_id === message.run_id,
                                  )
                                  .map((source) => (
                                    <a
                                      key={source.source_id}
                                      href={source.source_url}
                                      target="_blank"
                                      rel="noopener noreferrer"
                                    >
                                      {source.title} · v{source.version}
                                      {source.location?.page
                                        ? ` · 第 ${source.location?.page} 页`
                                        : source.location?.paragraph
                                          ? ` · 第 ${source.location.paragraph} 段`
                                          : ""}{" "}
                                      <Icon name="arrow-up-right" size={14} />
                                    </a>
                                  ))}
                              </nav>
                            )}
                          {message.role === "assistant" && run && (
                            <div className="run-result">
                              <span
                                className={
                                  active(run)
                                    ? "run-status working"
                                    : "run-status"
                                }
                              >
                                {statusNames[run.status] ?? run.status}
                              </span>
                              {(run.status === "queued" || active(run)) && (
                                <button
                                  type="button"
                                  className="run-cancel"
                                  disabled={run.status === "cancelling"}
                                  onClick={() => void cancel(run.id)}
                                >
                                  {run.status === "queued"
                                    ? "取消排队"
                                    : "取消运行"}
                                </button>
                              )}
                              {run.error && (
                                <p className="error">{run.error}</p>
                              )}
                              {run.tools && run.tools.length > 0 && (
                                <details className="run-tool-output">
                                  <summary>
                                    <Icon
                                      name="caret-right"
                                      size={14}
                                      className="disclosure-icon"
                                    />
                                    工具调用（{run.tools.length}）
                                  </summary>
                                  <ul className="tool-list">
                                    {run.tools.map((tool) => (
                                      <li key={tool.id}>
                                        <div>
                                          <strong>
                                            {tool.display_name || tool.name}
                                          </strong>
                                          <span>
                                            {toolStatusNames[tool.status] ??
                                              tool.status}
                                          </span>
                                          {run.output?.tools === "detailed" && (
                                            <ToolCallDetails
                                              request={request}
                                              runID={run.id}
                                              callID={tool.id}
                                            />
                                          )}
                                        </div>
                                      </li>
                                    ))}
                                  </ul>
                                </details>
                              )}
                              <RunDiagnostics run={run} request={request} />
                              {run.status === "cancelled" && (
                                <p className="muted">
                                  运行已停止，已产生的内容保留在此。
                                </p>
                              )}
                            </div>
                          )}
                        </div>
                      </article>
                    );
                  })}
                </div>
              )}
            </div>
            {channelConversation ? (
              <div className="channel-conversation-notice">
                <p>
                  请在原聊天平台发送新消息。这里可查看对话记录和取消任务。
                </p>
                {(currentRun || waitingCount > 0) && (
                  <p className="muted" role="status">
                    {currentRun ? statusNames[currentRun.status] : "等待处理"}
                    {waitingCount > 0 ? ` · 排队 ${waitingCount} 条` : ""}
                  </p>
                )}
              </div>
            ) : (
              <div className="composer-area">
                <form
                  onSubmit={(event) => {
                    nearBottom.current = true;
                    void send(event);
                  }}
                  className="composer"
                >
                  <label className="sr-only" htmlFor="message-input">
                    发送给助手的消息
                  </label>
                  <textarea
                    id="message-input"
                    ref={inputRef}
                    rows={2}
                    value={input}
                    onChange={(e) => setInput(e.target.value)}
                    placeholder={
                      agent?.policy.session.types === "group"
                        ? "此助手仅接收群聊消息"
                        : agent?.enabled === false
                          ? "助手已停用"
                          : "给助手发消息…"
                    }
                    maxLength={100000}
                    disabled={
                      agent?.policy.session.types === "group" ||
                      agent?.enabled === false ||
                      agent?.archived ||
                      Boolean(snapshot?.conversation.channel_id)
                    }
                    onKeyDown={(e) => {
                      if (
                        e.key === "Enter" &&
                        !e.shiftKey &&
                        !e.nativeEvent.isComposing
                      ) {
                        e.preventDefault();
                        if (!blocked && input.trim())
                          e.currentTarget.form?.requestSubmit();
                      }
                    }}
                  />
                  <div className="composer-toolbar">
                    <span>
                      {currentRun
                        ? `${statusNames[currentRun.status]}${waitingCount ? ` · 等待 ${waitingCount} 条` : ""}`
                        : !ready
                          ? "对话服务正在恢复"
                          : waitingCount
                            ? `等待 ${waitingCount} 条，按顺序处理`
                            : "Enter 发送 · Shift + Enter 换行"}
                    </span>
                    <div className="composer-buttons">
                      {currentRun && (
                        <button
                          type="button"
                          disabled={currentRun.status === "cancelling"}
                          onClick={() => void cancel(currentRun.id)}
                        >
                          取消当前运行
                        </button>
                      )}
                      <button
                        className="primary send-button"
                        disabled={blocked || !input.trim()}
                      >
                        发送 <Icon name="arrow-up" size={16} />
                      </button>
                    </div>
                  </div>
                </form>
                {agent?.policy.session.types === "group" &&
                  !snapshot?.conversation.channel_id && (
                    <p className="context-warning">
                      此助手仅接收群聊消息。请在已绑定的群聊中使用，Web
                      中仍可查看历史和运行详情。
                    </p>
                  )}

                <p className="composer-note">
                  模型可能出错，请核对重要信息。对话发送至当前配置的模型服务。
                </p>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}
