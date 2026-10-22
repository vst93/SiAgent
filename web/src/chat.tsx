import type { AgentPolicy } from "./agent-policy";
import { ToolCallDetails } from "./capabilities";
import { Icon } from "./icons";
import { MessageMarkdown } from "./message-markdown";
import {
  FormEvent,
  ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
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
  channel_name?: string;
  group_name?: string;
  sender_name?: string;
  last_activity: number;
  turn_count: number;
  scope?: "group" | "member";
  last_sender?: string;
  last_sender_name?: string;
  label_source?: "admin" | "member";
};

// conversationGroupKey identifies who a conversation belongs to. The WeCom
// protocol returns opaque IDs only, so a group is the channel plus the group ID
// (or the member ID for a private chat).
export function conversationGroupKey(item: Conversation) {
  if (!item.channel_id) return "web";
  if (item.channel_kind === "group")
    return `${item.channel_id}\u001fgroup\u001f${item.channel_group ?? ""}`;
  return `${item.channel_id}\u001fmember\u001f${item.channel_sender ?? ""}`;
}

// contactTarget is the vendor ID a label is stored against.
export function contactTarget(item: Conversation) {
  return item.channel_kind === "group"
    ? item.channel_group ?? ""
    : item.channel_sender ?? "";
}
// relativeTime keeps the session list scannable: an absolute date on every row
// would crowd out the title.
function relativeTime(value: number) {
  const diff = Date.now() - value;
  const minutes = Math.round(diff / 60000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days} 天前`;
  return new Date(value).toLocaleDateString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
  });
}

function channelKind(conversation?: Conversation) {
  if (conversation?.channel_kind === "group") return "群聊";
  if (conversation?.channel_kind === "single") return "私聊";
  return "渠道对话";
}

// channelPath spells out the full identity of a channel conversation, because a
// group, a member and a session are three different things and the header must
// not blur them.
function channelPath(conversation?: Conversation) {
  if (!conversation?.channel_id) return "本机对话";
  const parts = [conversation.channel_name || "渠道"];
  if (conversation.channel_kind === "group") {
    parts.push("群聊");
    parts.push(conversation.group_name || conversation.channel_group || "未命名群");
    if (conversation.scope === "group") parts.push("全群共享上下文");
    else
      parts.push(
        `成员 ${conversation.sender_name || conversation.channel_sender || "未命名"}`,
      );
  } else {
    parts.push("私聊");
    parts.push(conversation.sender_name || conversation.channel_sender || "未命名成员");
  }
  return parts.join(" · ");
}

function channelBadge(conversation: Conversation) {
  if (conversation.channel_kind === "group")
    return `群聊 · ${conversation.group_name || conversation.channel_group || "未命名群"}`;
  if (conversation.channel_kind === "single")
    return `私聊 · ${conversation.sender_name || conversation.channel_sender || "未命名成员"}`;
  return "渠道";
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

// The sidebar shows one row per contact — a group, or a member inside a group,
// or a private chat partner — and expands only the selected contact's sessions.
// A deep tree made the four concepts (channel, chat kind, contact, session) hard
// to scan; this keeps at most two visible levels.
//
// Group scope decides the contact: "按成员隔离" makes each member their own
// contact, "全群共享" makes the group the contact. A conversation without an
// explicit scope is treated as per-member, matching the default policy, so an
// item can never fall out of the list.
// How the sidebar aggregates conversations. Every conversation carries two
// dimensions — which group it happened in, and which person wrote — so the same
// data can be read either way:
//
//   按群聚合: 群 → 人 → 每次对话
//   按人聚合: 人 → 群 → 每次对话
//
// Private chats and local Web conversations are real dimensions too, so they
// become virtual groups (私聊 / 本机对话) instead of a separate section. That way
// one rule describes the whole list and nothing needs a special case.
export type Aggregation = "group" | "person";

// A conversation with both of its dimensions resolved.
type Atom = {
  conversation: Conversation;
  groupKey: string;
  groupLabel: string;
  groupKind: "group" | "private" | "web";
  groupID?: string;
  groupLabelSource?: "admin" | "member";
  /** A shared group has one context for everyone, so it has no single author. */
  shared: boolean;
  personKey: string;
  personLabel: string;
  personID?: string;
  personLabelSource?: "admin" | "member";
  channelID: string;
};

// One node of the sidebar tree. A node either has children (one more level to
// expand) or holds the conversations of that branch.
export type TreeNode = {
  key: string;
  label: string;
  sublabel: string;
  icon: "users" | "user" | "chat";
  lastActivity: number;
  children?: TreeNode[];
  sessions?: Conversation[];
  /** Set when this node names a contact the administrator can relabel. */
  rename?: {
    channelID: string;
    kind: "group" | "sender";
    target: string;
    label: string;
    source?: "admin" | "member";
  };
};

const EMPTY_PERSON = "\u0000group";

// conversationAtoms resolves both dimensions of every conversation.
function conversationAtoms(conversations: Conversation[]): Atom[] {
  const atoms: Atom[] = [];
  for (const item of conversations) {
    const channelID = item.channel_id ?? "";
    if (!channelID) {
      atoms.push({
        conversation: item,
        groupKey: "web",
        groupLabel: "本机对话",
        groupKind: "web",
        shared: false,
        // The administrator is the author of a local conversation.
        personKey: "self",
        personLabel: "我",
        channelID: "",
      });
      continue;
    }
    if (item.channel_kind !== "group") {
      const sender = item.channel_sender ?? "";
      atoms.push({
        conversation: item,
        groupKey: "private",
        groupLabel: "私聊",
        groupKind: "private",
        shared: false,
        personKey: `${channelID}\u001f${sender}`,
        personLabel: item.sender_name || sender || "未命名成员",
        personID: sender,
        personLabelSource: item.label_source,
        channelID,
      });
      continue;
    }
    const group = item.channel_group ?? "";
    const groupLabel = item.group_name || group || "未命名群";
    const shared = item.scope === "group";
    const sender = item.channel_sender ?? "";
    atoms.push({
      conversation: item,
      groupKey: `${channelID}\u001f${group}`,
      groupLabel,
      groupKind: "group",
      groupID: group,
      groupLabelSource: shared ? item.label_source : undefined,
      shared,
      personKey: shared ? `${channelID}\u001f${group}\u001f${EMPTY_PERSON}` : `${channelID}\u001f${sender}`,
      personLabel: shared
        ? "全群共享"
        : item.sender_name || sender || "未命名成员",
      personID: shared ? undefined : sender,
      personLabelSource: shared ? undefined : item.label_source,
      channelID,
    });
  }
  return atoms;
}

// activity scopes one branch to its newest conversation, so a parent never looks
// older than the child the reader is about to open.
function activity(sessions: Conversation[]) {
  return sessions.reduce(
    (newest, item) => Math.max(newest, item.last_activity || item.created),
    0,
  );
}

function sortSessions(sessions: Conversation[]) {
  return [...sessions].sort(
    (a, b) => (b.last_activity || b.created) - (a.last_activity || a.created),
  );
}

function turnsOf(sessions: Conversation[]) {
  return sessions.reduce((total, item) => total + (item.turn_count || 0), 0);
}

// buildGroupTree reads the list as 群 → 人 → 会话.
function buildGroupTree(atoms: Atom[]): TreeNode[] {
  const groups = new Map<string, Atom[]>();
  for (const atom of atoms) {
    const list = groups.get(atom.groupKey);
    if (list) list.push(atom);
    else groups.set(atom.groupKey, [atom]);
  }
  const nodes: TreeNode[] = [];
  for (const [key, members] of groups) {
    const first = members[0];
    const all = sortSessions(members.map((atom) => atom.conversation));
    const rename =
      first.groupKind === "group" && first.groupID
        ? {
            channelID: first.channelID,
            kind: "group" as const,
            target: first.groupID,
            label: first.groupLabel,
            source: first.groupLabelSource,
          }
        : undefined;
    // A local conversation has no author to group by, so it lists its sessions
    // directly instead of showing a single "我" row.
    if (first.groupKind === "web") {
      nodes.push({
        key,
        label: first.groupLabel,
        sublabel: `${all.length} 会话`,
        icon: "chat",
        lastActivity: activity(all),
        sessions: all,
      });
      continue;
    }
    // A shared group keeps one context for the whole group, so its conversations
    // have no single author. Listing them straight under the group avoids an
    // artificial "全群共享" member row; a group that mixes shared and per-member
    // conversations keeps the row, because there the split is real information.
    const onlyShared = members.every((atom) => atom.shared);
    if (onlyShared) {
      nodes.push({
        key,
        label: first.groupLabel,
        sublabel: `全群共享 · ${all.length} 会话`,
        icon: "users",
        lastActivity: activity(all),
        sessions: all,
        rename,
      });
      continue;
    }
    const people = new Map<string, Atom[]>();
    for (const atom of members) {
      const list = people.get(atom.personKey);
      if (list) list.push(atom);
      else people.set(atom.personKey, [atom]);
    }
    const children: TreeNode[] = [...people.entries()].map(
      ([personKey, authored]) => {
        const head = authored[0];
        const sessions = sortSessions(
          authored.map((atom) => atom.conversation),
        );
        return {
          key: `${key}\u001f${personKey}`,
          label: head.personLabel,
          sublabel: `${turnsOf(sessions)} 问`,
          icon: "user" as const,
          lastActivity: activity(sessions),
          sessions,
          rename:
            !head.shared && head.personID
              ? {
                  channelID: head.channelID,
                  kind: "sender" as const,
                  target: head.personID,
                  label: head.personLabel,
                  source: head.personLabelSource,
                }
              : undefined,
        };
      },
    );
    children.sort((a, b) => b.lastActivity - a.lastActivity);
    nodes.push({
      key,
      label: first.groupLabel,
      sublabel: `${all.length} 会话`,
      icon: first.groupKind === "private" ? "user" : "users",
      lastActivity: activity(all),
      children,
      rename,
    });
  }
  nodes.sort((a, b) => b.lastActivity - a.lastActivity);
  return nodes;
}

// buildPersonTree reads the same list as 人 → 群 → 会话.
function buildPersonTree(atoms: Atom[]): TreeNode[] {
  const people = new Map<string, Atom[]>();
  for (const atom of atoms) {
    const list = people.get(atom.personKey);
    if (list) list.push(atom);
    else people.set(atom.personKey, [atom]);
  }
  const nodes: TreeNode[] = [];
  for (const [key, inPlaces] of people) {
    const first = inPlaces[0];
    const all = sortSessions(inPlaces.map((atom) => atom.conversation));
    const places = new Map<string, Atom[]>();
    for (const atom of inPlaces) {
      const list = places.get(atom.groupKey);
      if (list) list.push(atom);
      else places.set(atom.groupKey, [atom]);
    }
    const children: TreeNode[] = [...places.entries()].map(
      ([groupKey, inGroup]) => {
        const head = inGroup[0];
        const sessions = sortSessions(
          inGroup.map((atom) => atom.conversation),
        );
        return {
          key: `${key}\u001f${groupKey}`,
          label: head.groupLabel,
          sublabel: `${sessions.length} 会话`,
          icon:
            head.groupKind === "group"
              ? ("users" as const)
              : head.groupKind === "private"
                ? ("user" as const)
                : ("chat" as const),
          lastActivity: activity(sessions),
          sessions,
          rename:
            head.groupKind === "group" && head.groupID
              ? {
                  channelID: head.channelID,
                  kind: "group" as const,
                  target: head.groupID,
                  label: head.groupLabel,
                  source: head.groupLabelSource,
                }
              : undefined,
        };
      },
    );
    children.sort((a, b) => b.lastActivity - a.lastActivity);
    nodes.push({
      key,
      label: first.personLabel,
      sublabel: `${all.length} 会话`,
      icon: first.personKey === "self" ? "chat" : "user",
      lastActivity: activity(all),
      children,
      rename:
        first.personID && !first.shared
          ? {
              channelID: first.channelID,
              kind: "sender" as const,
              target: first.personID,
              label: first.personLabel,
              source: first.personLabelSource,
            }
          : undefined,
    });
  }
  nodes.sort((a, b) => b.lastActivity - a.lastActivity);
  return nodes;
}

export function conversationTree(
  conversations: Conversation[],
  aggregation: Aggregation,
): TreeNode[] {
  const atoms = conversationAtoms(conversations);
  return aggregation === "group"
    ? buildGroupTree(atoms)
    : buildPersonTree(atoms);
}

// pathTo walks the tree and returns the keys leading to one conversation, so the
// reader always sees the branch that holds what they selected.
export function pathTo(nodes: TreeNode[], conversationID: string): string[] {
  for (const node of nodes) {
    if (node.sessions?.some((item) => item.id === conversationID))
      return [node.key];
    if (node.children) {
      const deeper = pathTo(node.children, conversationID);
      if (deeper.length > 0) return [node.key, ...deeper];
    }
  }
  return [];
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
export type Message = {
  id: number;
  run_id: string;
  role: string;
  content: string;
  created: number;
  sender?: string;
  attachments?: Attachment[];
};

export type Snapshot = {
  sources: (KnowledgeHit & { run_id: string })[];
  conversation: Conversation;
  messages: Message[];
  runs: Run[];
  has_more?: boolean;
  oldest_id?: number;
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
  onLabelsChanged,
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
  onLabelsChanged: () => void;
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
  // Earlier pages are merged into the live snapshot. The SSE stream only carries
  // the newest page, so a reader who scrolls up accumulates history here instead
  // of forcing the server to resend everything on every tick.
  const [older, setOlder] = useState<{
    messages: Message[];
    runs: Run[];
    sources: Snapshot["sources"];
    hasMore: boolean;
  }>({ messages: [], runs: [], sources: [], hasMore: false });
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [labelEditing, setLabelEditing] = useState("");
  const [labelValue, setLabelValue] = useState("");
  const [labelSaving, setLabelSaving] = useState(false);
  const [labelError, setLabelError] = useState("");
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
  // A new conversation starts with an empty history buffer.
  useEffect(() => {
    setOlder({ messages: [], runs: [], sources: [], hasMore: false });
    setLabelEditing("");
    setLabelError("");
  }, [selected]);
  const merged = useMemo(() => {
    const messages = new Map<number, Message>();
    for (const message of older.messages) messages.set(message.id, message);
    for (const message of snapshot?.messages ?? []) messages.set(message.id, message);
    const runs = new Map<string, Run>();
    for (const run of older.runs) runs.set(run.id, run);
    for (const run of snapshot?.runs ?? []) runs.set(run.id, run);
    const sources = [...older.sources, ...(snapshot?.sources ?? [])];
    return {
      messages: [...messages.values()].sort((a, b) => a.id - b.id),
      runs: [...runs.values()],
      sources,
    };
  }, [older, snapshot]);
  // hasMore is true while the server has messages older than everything loaded.
  const hasMore = older.hasMore || Boolean(snapshot?.has_more);
  async function loadOlder() {
    if (!snapshot || loadingOlder) return;
    const oldest = merged.messages[0]?.id ?? snapshot.oldest_id;
    if (!oldest) return;
    setLoadingOlder(true);
    try {
      const page = await request<Snapshot>(
        `/conversations/${encodeURIComponent(snapshot.conversation.id)}?before=${oldest}&limit=40`,
      );
      const element = scrollRef.current;
      const previousHeight = element?.scrollHeight ?? 0;
      setOlder((current) => ({
        messages: [...page.messages, ...current.messages],
        runs: [...page.runs, ...current.runs],
        sources: [...page.sources, ...current.sources],
        hasMore: Boolean(page.has_more),
      }));
      // Keep the reading position: the prepended page must not jump the view.
      requestAnimationFrame(() => {
        if (!element) return;
        element.scrollTop += element.scrollHeight - previousHeight;
      });
    } catch (error) {
      setCopyFailed(error instanceof Error ? error.message : "更早的消息加载失败。");
    } finally {
      setLoadingOlder(false);
    }
  }
  // Two readings of the same conversations. "group" answers "what is happening
  // in each group", "person" answers "what has this person asked"; the reader
  // picks whichever question they have instead of switching between kind filters.
  const [aggregation, setAggregation] = useState<Aggregation>("group");
const [search, setSearch] = useState("");
  // Filter conversations by search query before building the tree.
  const filtered = useMemo(() => {
    if (!search.trim()) return conversations;
    const q = search.trim().toLowerCase();
    return conversations.filter((item) => {
      const title = (item.title || "").toLowerCase();
      const sender = (item.sender_name || item.channel_sender || "").toLowerCase();
      const group = (item.group_name || item.channel_group || "").toLowerCase();
      return title.includes(q) || sender.includes(q) || group.includes(q);
    });
  }, [conversations, search]);

  const nodes = useMemo(
    () => conversationTree(filtered, aggregation),
    [filtered, aggregation],
  );
  // The branch holding the selected conversation always stays open, so selecting
  // a row can never make it disappear from the list.
  const path = useMemo(() => pathTo(nodes, selected), [nodes, selected]);
  const [expandedKeys, setExpandedKeys] = useState<Set<string>>(() => new Set());
  useEffect(() => {
    setExpandedKeys((current) => {
      const next = new Set(current);
      for (const key of path) next.add(key);
      return next;
    });
  }, [path]);
  function toggleNode(key: string) {
    setExpandedKeys((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }
  function focusSelected() {
    setExpandedKeys(new Set(path));
  }
  function sessionRow(item: Conversation, siblings: Conversation[]) {
    const when = item.last_activity || item.created;
    // Sessions are newest first, so the newest of N is the Nth.
    const ordinal =
      siblings.length - siblings.findIndex((each) => each.id === item.id);
    return (
      <button
        className={selected === item.id ? "conversation selected" : "conversation"}
        aria-current={selected === item.id ? "page" : undefined}
        key={item.id}
        title={item.title}
        onClick={() => {
          selectConversation(item.id);
          setHistoryOpen(false);
        }}
      >
        <span className="conversation-title">{item.title}</span>
        <small className="conversation-meta">
          {siblings.length > 1 && (
            <span className="session-ordinal">第 {ordinal} 次</span>
          )}
          <span>{item.turn_count} 问</span>
          <time dateTime={new Date(when).toISOString()}>
            {relativeTime(when)}
          </time>
        </small>
      </button>
    );
  }
  function renameForm(node: TreeNode) {
    const rename = node.rename;
    if (!rename) return null;
    return (
      <form
        className="conversation-group-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (labelSaving) return;
          setLabelSaving(true);
          setLabelError("");
          void request("/channel-contacts", "PUT", {
            channel_id: rename.channelID,
            kind: rename.kind,
            target: rename.target,
            name: labelValue.trim(),
          })
            .then(() => {
              setLabelEditing("");
              onLabelsChanged();
            })
            .catch((error) =>
              setLabelError(error instanceof Error ? error.message : "标记失败。"),
            )
            .finally(() => setLabelSaving(false));
        }}
      >
        <input
          autoFocus
          aria-label="群或成员名称"
          placeholder="例如：产品讨论群 / 张三"
          maxLength={64}
          value={labelValue}
          onChange={(event) => setLabelValue(event.target.value)}
        />
        <button type="submit" disabled={labelSaving}>
          保存
        </button>
        <button
          type="button"
          onClick={() => {
            setLabelEditing("");
            setLabelError("");
          }}
        >
          取消
        </button>
        {labelError && <p className="error">{labelError}</p>}
        <small>
          企微只提供 ID，不提供群名或用户名。成员可用 /user、/group
          自行标记；这里保存的名称由管理员维护，会覆盖成员设置。
        </small>
      </form>
    );
  }
  // While searching, every branch that survived the filter is opened: the result
  // set is small by definition, and leaving it collapsed would hide the matches.
  const searching = search.trim() !== "";
  function renderNodes(list: TreeNode[], depth: number): ReactNode {
    return list.map((node) => {
      const open = searching || expandedKeys.has(node.key);
      const hasDeeper = Boolean(node.children?.length || node.sessions?.length);
      const rename = node.rename;
      const active = path.includes(node.key);
      return (
        <div className={`tree-node depth-${depth}`} key={node.key}>
          <div
            className={`tree-row ${open ? "is-open" : ""} ${
              active ? "is-active" : ""
            }`}
          >
            <button
              type="button"
              className="tree-toggle"
              aria-expanded={hasDeeper ? open : undefined}
              aria-label={
                hasDeeper ? `${open ? "收起" : "展开"} ${node.label}` : node.label
              }
              onClick={() => (hasDeeper ? toggleNode(node.key) : undefined)}
            >
              <span className="tree-caret" aria-hidden="true">
                {hasDeeper && (
                  <Icon name={open ? "caret-down" : "caret-right"} size={14} />
                )}
              </span>
              <Icon name={node.icon} size={16} className="chat-glyph" />
              <span className="tree-copy">
                <span className="tree-label" title={node.label}>
                  {node.label}
                </span>
                <span className="tree-sublabel">
                  {/* One span per segment so a badge like 全群共享 can carry its
                      own translation instead of being read as part of a count. */}
                  {node.sublabel.split(" · ").map((part, index) => (
                    <span key={part}>
                      {index > 0 ? " · " : ""}
                      {part}
                    </span>
                  ))}
                </span>
              </span>
              <time
                className="tree-time"
                dateTime={new Date(node.lastActivity).toISOString()}
              >
                {relativeTime(node.lastActivity)}
              </time>
            </button>
            {rename && (
              <button
                type="button"
                className="conversation-group-rename"
                aria-label={`标记 ${node.label} 的名称`}
                title={`标记 ${node.label} 的名称`}
                onClick={() => {
                  setLabelError("");
                  setLabelEditing(node.key);
                  setLabelValue(
                    node.label.startsWith("未命名") ? "" : node.label,
                  );
                }}
              >
                <Icon name="edit" size={14} />
              </button>
            )}
          </div>
          {labelEditing === node.key && renameForm(node)}
          {open && node.children ? renderNodes(node.children, depth + 1) : null}
          {open && node.sessions ? (
            <div className={`tree-sessions depth-${depth + 1}`}>
              {node.sessions.map((item) => sessionRow(item, node.sessions!))}
            </div>
          ) : null}
        </div>
      );
    });
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
                title={`${channelPath(conversation)} · 只读`}
              >
                <Icon name="channels" size={14} />
                <span className="channel-view-path">{channelPath(conversation)}</span>
                <span className="channel-view-readonly">只读</span>
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
          <span className="chat-history-count">{conversations.length}</span>
          <button
            type="button"
            className="chat-history-focus"
            onClick={focusSelected}
            title="收起其他分支，只保留当前会话"
          >
            <Icon name="caret-right" size={14} />
          </button>
        </div>
        <div className="history-search">
          <input
            type="text"
            placeholder="搜索群名、人名或会话标题"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          {search && (
            <button type="button" onClick={() => setSearch("")} aria-label="清除搜索">
              <Icon name="close" size={14} />
            </button>
          )}
        </div>
        <div className="aggregation-switch" role="group" aria-label="聚合方式">
          {(
            [
              ["group", "按群聚合"],
              ["person", "按人聚合"],
            ] as const
          ).map(([key, label]) => (
            <button
              key={key}
              type="button"
              aria-pressed={aggregation === key}
              className={aggregation === key ? "selected" : ""}
              onClick={() => setAggregation(key)}
            >
              {label}
            </button>
          ))}
        </div>
        <nav aria-label="对话记录" className="conversation-list">
          {renderNodes(nodes, 0)}
          {!conversations.length && (
            <p className="nav-empty">
              {search.trim()
                ? "没有匹配的群、成员或会话。"
                : "开始对话后，记录会保存在这里。"}
            </p>
          )}
        </nav>
        {!channelConversation && (
          <div className="chat-history-actions">
            <button
              type="button"
              className="chat-copy-transcript"
              disabled={!snapshot?.messages.length}
              onClick={() =>
                void copy("transcript", transcript(), "复制失败，请手动选择文本。")
              }
            >
              <Icon name={copied === "transcript" ? "check" : "copy"} size={14} />
              {copied === "transcript" ? "已复制" : "复制对话"}
            </button>
          </div>
        )}
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
                if (element.scrollTop < 80 && hasMore) void loadOlder();
              }}
            >
              {!merged.messages.length ? (
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
                  {hasMore && (
                    <div className="load-older">
                      <button
                        type="button"
                        disabled={loadingOlder}
                        onClick={() => void loadOlder()}
                      >
                        {loadingOlder ? "正在加载更早的消息…" : "加载更早的消息"}
                      </button>
                    </div>
                  )}
                  {merged.messages.map((message) => {
                    const messageTime = new Date(message.created);
                    const run = merged.runs.find(
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
                            merged.sources.some(
                              (source) => source.run_id === message.run_id,
                            ) && (
                              <nav
                                className="answer-sources"
                                aria-label="本轮检索来源"
                              >
                                <strong>本轮检索来源</strong>
                                {merged.sources
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
