import type { AgentPolicy } from "./agent-policy";
import { ToolCallDetails } from "./capabilities";
import { Icon } from "./icons";
import { MessageMarkdown } from "./message-markdown";
import { FormEvent, useEffect, useRef, useState } from "react";
import type { Agent, Model } from "./agents";
import type { KnowledgeHit } from "./knowledge";
import { ConversationMaintenance, RunDiagnostics } from "./runtime-controls";
import { appURL } from "./urls";
export type Conversation = {
  id: string;
  title: string;
  created: number;
  agent_id: string;
  channel_id?: string;
  channel_kind?: string;
  channel_sender?: string;
  channel_group?: string;
};
function channelKind(conversation?: Conversation) {
  if (conversation?.channel_kind === "group") return "群聊";
  if (conversation?.channel_kind === "single") return "私聊";
  return "渠道对话";
}
function channelLabel(conversation?: Conversation) {
  const detail =
    conversation?.channel_kind === "group"
      ? conversation.channel_group
      : conversation?.channel_sender;
  return detail ? `${channelKind(conversation)} · ${detail}` : channelKind(conversation);
}
// copyText prefers the async clipboard and falls back to a hidden textarea so
// the action still works on plain-HTTP deployments where the API is blocked.
async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Fall through to the legacy path.
  }
  try {
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.append(area);
    area.select();
    const ok = document.execCommand("copy");
    area.remove();
    return ok;
  } catch {
    return false;
  }
}

function channelBadge(conversation: Conversation) {
  if (conversation.channel_kind === "single" && conversation.channel_sender)
    return `私聊 · ${conversation.channel_sender}`;
  if (conversation.channel_kind === "group") return "群聊";
  return "渠道";
}
export type Run = {
  output?: AgentPolicy["output"];
  thinking?: string;
  evidence_state?: string;
  citation_valid?: number;
  tools?: { id: string; name: string; display_name?: string; status: string }[];
  id: string;
  status: string;
  error: string;
  created: number;
};
export type Attachment = {
  id: string;
  name: string;
  mime: string;
  kind: "image" | "file";
  size: number;
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
    sender?: string;
    attachments?: Attachment[];
  }[];
  runs: Run[];
};
function RunEvidence({ request, runID }: { request: Api; runID: string }) {
  const [rows, setRows] = useState<
    { claim_index: number; source_ids: string[] }[] | null
  >(null);
  const [error, setError] = useState("");
  async function load() {
    setError("");
    try {
      setRows(
        await request<{ claim_index: number; source_ids: string[] }[]>(
          `/runs/${runID}/evidence`,
        ),
      );
    } catch (error) {
      setError(error instanceof Error ? error.message : "证据读取失败。");
    }
  }
  return (
    <details className="run-evidence" onToggle={(event) => {
      if ((event.currentTarget as HTMLDetailsElement).open && rows === null) void load();
    }}>
      <summary>
        <Icon name="caret-right" size={14} className="disclosure-icon" />
        答案-证据
      </summary>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {rows && !rows.length && <p className="muted">本轮没有可映射的引用。</p>}
      {!!rows?.length && (
        <ul>
          {rows.map((row) => (
            <li key={row.claim_index}>
              <span>结论 {row.claim_index + 1}</span>
              <span>{row.source_ids.length} 个来源</span>
            </li>
          ))}
        </ul>
      )}
    </details>
  );
}

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
  attachments,
  uploading,
  onUpload,
  onRemoveAttachment,
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
  attachments: Attachment[];
  uploading: boolean;
  onUpload: (files: FileList) => Promise<void>;
  onRemoveAttachment: (id: string) => void;
}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  const [historyCollapsed, setHistoryCollapsed] = useState(() => {
    try {
      return localStorage.getItem("siagent.history-collapsed") === "true";
    } catch {
      return false;
    }
  });
  const [showJump, setShowJump] = useState(false);
  const [mobile, setMobile] = useState(() =>
    window.matchMedia("(max-width: 980px)").matches,
  );
  useEffect(() => {
    const query = window.matchMedia("(max-width: 980px)");
    const handler = () => setMobile(query.matches);
    query.addEventListener("change", handler);
    return () => query.removeEventListener("change", handler);
  }, []);
  useEffect(() => {
    try {
      localStorage.setItem(
        "siagent.history-collapsed",
        String(historyCollapsed),
      );
    } catch {
      // The layout still changes for this session.
    }
  }, [historyCollapsed]);
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if (event.key === "Escape") setHistoryOpen(false);
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);
  const scrollRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const nearBottom = useRef(true);
  const [copied, setCopied] = useState("");
  const [copyFailed, setCopyFailed] = useState("");
  const copyReset = useRef<number | undefined>(undefined);
  useEffect(
    () => () => {
      if (copyReset.current) window.clearTimeout(copyReset.current);
    },
    [],
  );
  // announce copies briefly, then clear the marker so the action can be reused
  async function copy(key: string, text: string, failed: string) {
    setCopyFailed("");
    const ok = await copyText(text);
    if (!ok) {
      setCopyFailed(failed);
      return;
    }
    setCopied(key);
    if (copyReset.current) window.clearTimeout(copyReset.current);
    copyReset.current = window.setTimeout(() => setCopied(""), 1800);
  }
  function transcript() {
    if (!snapshot) return "";
    const lines: string[] = [];
    const title = snapshot.conversation.title || "对话";
    lines.push(`# ${title}`);
    for (const message of snapshot.messages) {
      const who =
        message.role === "user"
          ? message.sender || snapshot.conversation.channel_sender || "用户"
          : "助手";
      lines.push("", `## ${who}`, "", message.content || "(空)");
    }
    return lines.join("\n");
  }
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
    setShowJump(false);
  }, [selected]);
  useEffect(() => {
    if (nearBottom.current && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
      setShowJump(false);
    }
  }, [snapshot]);
  const actions = (
    <>
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
        <span>新对话</span>
      </button>
      {!!snapshot?.messages.length && (
        <button
          type="button"
          className="chat-copy-transcript"
          title="复制整段对话为 Markdown"
          onClick={() => void copy("transcript", transcript(), "复制失败，请手动选择文本。")}
        >
          <Icon name={copied === "transcript" ? "check" : "copy"} size={16} />
          <span>{copied === "transcript" ? "已复制" : "复制对话"}</span>
        </button>
      )}
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
      <button
        type="button"
        className="desktop-history-toggle"
        aria-expanded={!historyCollapsed}
        aria-controls="chat-history"
        aria-label={historyCollapsed ? "展开对话记录" : "收起对话记录"}
        title={historyCollapsed ? "展开对话记录" : "收起对话记录"}
        onClick={() => setHistoryCollapsed((value) => !value)}
      >
        <Icon name="sidebar" size={16} />
      </button>
    </>
  );
  return (
    <section
      className={`chat-workspace ${historyCollapsed ? "history-collapsed" : ""}`}
      aria-label="Agent 对话工作区"
    >
      <header className="chat-header">
        <div className="chat-heading">
          <h1 title={conversation?.title ?? undefined}>
            {conversation?.title ?? "开始一段对话"}
          </h1>
          <div className="chat-meta-row">
            <p
              className="chat-subtitle"
              title={`${agent?.name ?? "尚未选择 Agent"}${model ? ` · ${model.name}` : ""}`}
            >
              {agent?.name ?? "尚未选择 Agent"}
              {model ? ` · ${model.name}` : ""}
            </p>
            {channelConversation && (
              <span
                className="channel-view-label"
                title={`${channelLabel(conversation)} · 只读`}
              >
                <Icon name="channels" size={14} />
                <span>{channelKind(conversation)} · 只读</span>
              </span>
            )}
            {!!snapshot?.messages.length && (
              <span
                className="chat-stats"
                title="本会话的提问数与消息总数"
              >
                {snapshot.messages.filter((m) => m.role === "user").length} 轮
                · {snapshot.messages.length} 条消息
              </span>
            )}
          </div>
        </div>
        <div className="chat-header-actions">{actions}</div>
      </header>
      <aside
        id="chat-history"
        className={`chat-history-panel ${historyOpen ? "is-open" : ""}`}
        aria-label="对话记录"
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
              {item.channel_id && (
                <small
                  className={`channel-tag ${item.channel_kind === "group" ? "group" : ""}`}
                  title={channelLabel(item)}
                >
                  {channelBadge(item)}
                </small>
              )}
            </button>
          ))}
          {!conversations.length && (
            <p className="nav-empty">开始对话后，记录会保存在这里。</p>
          )}
        </nav>
        <div className="chat-sidebar-status">
          <Icon name="history" size={14} />
          <span>会话保存在此实例</span>
        </div>
      </aside>
      <div className="chat-main">
        {connectionLost && (
          <p className="workspace-alert muted" role="status">
            连接暂时中断，正在重新连接。已保存的内容仍然保留。
          </p>
        )}
        {copyFailed && (
          <p className="workspace-alert muted" role="alert">
            {copyFailed}
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
            <div className="chat-stream">
              <div
                className="message-scroll"
                ref={scrollRef}
              onScroll={() => {
                const element = scrollRef.current;
                if (!element) return;
                const distance =
                  element.scrollHeight -
                  element.scrollTop -
                  element.clientHeight;
                nearBottom.current = distance < 100;
                setShowJump(distance > 240);
              }}
            >
              {!snapshot?.messages.length ? (
                <div className="empty-view chat-empty">
                  <span className="assistant-symbol" aria-hidden="true">
                    S·
                  </span>
                  <h2>从一个问题开始</h2>
                  <p className="muted">
                    写下你的问题，或告诉助手你想完成什么。
                    <br />
                    对话会保存在这个实例中。
                  </p>
                  <div className="chat-suggestions">
                    {[
                      "总结知识库内容",
                      "拆解这段需求",
                      "检查当前配置",
                    ].map((suggestion) => (
                      <button
                        type="button"
                        key={suggestion}
                        onClick={() => {
                          setInput(suggestion);
                          inputRef.current?.focus();
                        }}
                      >
                        {suggestion}
                      </button>
                    ))}
                  </div>
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
                                ? channelConversation
                                  ? message.sender ||
                                    conversation?.channel_sender ||
                                    "渠道用户"
                                  : "你"
                                : "助手"}
                            </strong>
                            <time
                              dateTime={messageTime.toISOString()}
                              title={messageTime.toLocaleString("zh-CN", {
                                timeZoneName: "short",
                              })}
                            >
                              {messageTime.toLocaleString("zh-CN", {
                                month: "2-digit",
                                day: "2-digit",
                                hour: "2-digit",
                                minute: "2-digit",
                                hourCycle: "h23",
                              })}
                            </time>
                            {!!message.content && (
                              <span className="message-actions">
                                <button
                                  type="button"
                                  className="message-action"
                                  aria-label={
                                    copied === `message-${message.id}`
                                      ? "已复制这条消息"
                                      : "复制这条消息"
                                  }
                                  title="复制这条消息"
                                  onClick={() =>
                                    void copy(
                                      `message-${message.id}`,
                                      message.content,
                                      "复制失败，请手动选择文本。",
                                    )
                                  }
                                >
                                  <Icon
                                    name={
                                      copied === `message-${message.id}`
                                        ? "check"
                                        : "copy"
                                    }
                                    size={14}
                                  />
                                </button>
                              </span>
                            )}
                          </div>
                          {!!message.attachments?.length && (
                            <div className="message-attachments">
                              {message.attachments.map((attachment) =>
                                attachment.kind === "image" ? (
                                  <a
                                    key={attachment.id}
                                    href={appURL(`/api/attachments/${attachment.id}`)}
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    className="message-image"
                                  >
                                    <img
                                      src={appURL(`/api/attachments/${attachment.id}`)}
                                      alt={attachment.name}
                                      loading="lazy"
                                    />
                                  </a>
                                ) : (
                                  <a
                                    key={attachment.id}
                                    href={appURL(`/api/attachments/${attachment.id}`)}
                                    className="message-file"
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    title={attachment.name}
                                  >
                                    <Icon name="file" size={14} />
                                    {attachment.name}
                                  </a>
                                ),
                              )}
                            </div>
                          )}
                          {message.role === "assistant" && run?.thinking && (
                            <details className="thinking-output">
                              <summary>
                                <Icon
                                  name="caret-right"
                                  size={14}
                                  className="disclosure-icon"
                                />
                                思考摘要
                                <button
                                  type="button"
                                  className="message-action"
                                  aria-label="复制思考摘要"
                                  title="复制思考摘要"
                                  onClick={(event) => {
                                    event.preventDefault();
                                    void copy(
                                      `thinking-${message.id}`,
                                      run.thinking ?? "",
                                      "复制失败，请手动选择文本。",
                                    );
                                  }}
                                >
                                  <Icon
                                    name={
                                      copied === `thinking-${message.id}`
                                        ? "check"
                                        : "copy"
                                    }
                                    size={14}
                                  />
                                </button>
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
                              {run.evidence_state === "none" && (
                                <p className="context-warning" role="status">
                                  本次回答没有检索到知识库依据，请核对内容或补充资料。
                                </p>
                              )}
                              {run.citation_valid === 0 && (
                                <p className="context-warning" role="status">
                                  回答引用了本次未返回的来源编号，请核对引用。
                                </p>
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
                              <RunEvidence request={request} runID={run.id} />
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
              {showJump && (
                <button
                  type="button"
                  className="jump-to-latest"
                  onClick={() => {
                    const element = scrollRef.current;
                    if (!element) return;
                    const reduced = window.matchMedia(
                      "(prefers-reduced-motion: reduce)",
                    ).matches;
                    element.scrollTo({
                      top: element.scrollHeight,
                      behavior: reduced ? "auto" : "smooth",
                    });
                    nearBottom.current = true;
                    setShowJump(false);
                  }}
                >
                  <Icon name="arrow-down" size={14} />
                  回到最新
                </button>
              )}
            </div>
            {channelConversation ? (
              <div className="channel-conversation-notice">
                <p>请在原聊天平台发送新消息。</p>
                {(currentRun || waitingCount > 0) && (
                  <p className="muted" role="status">
                    {currentRun ? statusNames[currentRun.status] : "等待处理"}
                    {waitingCount > 0 ? ` · 排队 ${waitingCount} 条` : ""}
                  </p>
                )}
              </div>
            ) : (
              <div className="composer-area">
                {!!attachments.length && (
                  <div className="composer-attachments" aria-label="待发送附件">
                    {attachments.map((attachment) =>
                      attachment.kind === "image" ? (
                        <span className="composer-attachment image" key={attachment.id}>
                          <img
                            src={appURL(`/api/attachments/${attachment.id}`)}
                            alt={attachment.name}
                          />
                          <button
                            type="button"
                            aria-label={`移除 ${attachment.name}`}
                            onClick={() => onRemoveAttachment(attachment.id)}
                          >
                            <Icon name="close" size={14} />
                          </button>
                        </span>
                      ) : (
                        <span className="composer-attachment" key={attachment.id}>
                          <Icon name="file" size={14} />
                          <span title={attachment.name}>{attachment.name}</span>
                          <button
                            type="button"
                            aria-label={`移除 ${attachment.name}`}
                            onClick={() => onRemoveAttachment(attachment.id)}
                          >
                            <Icon name="close" size={14} />
                          </button>
                        </span>
                      ),
                    )}
                  </div>
                )}
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
                    <label className="attach-button" title="添加图片或文件">
                      <Icon name="plus" size={16} />
                      <span>附件</span>
                      <input
                        type="file"
                        multiple
                        disabled={blocked || uploading}
                        accept={
                          model?.options.input?.includes("image")
                            ? ".png,.jpg,.jpeg,.webp,.gif,.txt,.md,.markdown,.csv,.json,.pdf,.docx"
                            : ".txt,.md,.markdown,.csv,.json,.pdf,.docx"
                        }
                        onChange={(event) => {
                          if (event.target.files?.length)
                            void onUpload(event.target.files);
                          event.target.value = "";
                        }}
                      />
                    </label>
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
                        disabled={blocked || uploading || (!input.trim() && !attachments.length)}
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
