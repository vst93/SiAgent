import { Icon } from "./icons";
import { type Agent, type Model } from "./agents";
import { KnowledgeSource } from "./knowledge";
import {
  ChatWorkspace,
  type Conversation,
  type Snapshot,
  type Run,
} from "./chat";
import { Configuration } from "./configuration";
import {
  ModuleSwitch,
  pageName,
  useNavigation,
  type ConfigSection,
} from "./navigation";
import { FormEvent, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./style.css";

type Auth = {
  initialized: boolean;
  authenticated: boolean;
  csrf: string;
  requires_setup_token: boolean;
};
let csrf = "";
async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const multipart = body instanceof FormData;
  const response = await fetch(`/api${path}`, {
    method,
    headers: {
      ...(body !== undefined && !multipart
        ? { "Content-Type": "application/json" }
        : {}),
      ...(method !== "GET" ? { "X-CSRF-Token": csrf } : {}),
    },
    body:
      body === undefined ? undefined : multipart ? body : JSON.stringify(body),
  });
  const data = await response.json();
  if (!response.ok) {
    if (response.status === 401 && csrf)
      window.dispatchEvent(new Event("siagent:session-expired"));
    throw new Error(data.error ?? "操作未完成，请刷新后重试。");
  }
  return data as T;
}
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "无法连接服务，请检查网络后重试。";

function Mark() {
  return (
    <span className="mark" aria-hidden="true">
      S<span>·</span>
    </span>
  );
}
function AuthScreen({
  auth,
  onDone,
}: {
  auth: Auth;
  onDone: () => Promise<void>;
}) {
  const [password, setPassword] = useState("");
  const [token, setToken] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await api(
        auth.initialized ? "/auth/login" : "/auth/setup",
        "POST",
        auth.initialized ? { password } : { password, token },
      );
      setPassword("");
      setToken("");
      await onDone();
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <div className="auth-page">
      <div className="brand">
        <Mark /> <span>SiAgent</span>
      </div>
      <div className="auth-panel">
        <p className="eyebrow">你的 AI 工作台</p>
        <h1>{auth.initialized ? "欢迎回来" : "先为工作台设置密码"}</h1>
        <p className="muted">
          {auth.initialized
            ? "登录后继续与助手对话，或管理模型连接。"
            : "这个实例只属于一个管理员。设置密码后，连接模型即可开始对话。"}
        </p>
        {!auth.initialized && (
          <ol className="setup-path">
            <li className="current">设置密码</li>
            <li>连接模型</li>
            <li>开始对话</li>
          </ol>
        )}
        <form onSubmit={submit}>
          {!auth.initialized && auth.requires_setup_token && (
            <label>
              首次启动令牌
              <input
                autoComplete="off"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                required
              />
              <small>从启动 SiAgent 的终端复制，仅首次设置时使用。</small>
            </label>
          )}
          <label>
            管理员密码
            <input
              type="password"
              autoFocus
              autoComplete={
                auth.initialized ? "current-password" : "new-password"
              }
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              minLength={auth.initialized ? 1 : 12}
              maxLength={72}
            />
            {!auth.initialized && (
              <small>
                至少 12 字节，请使用独立的长密码；汉字会占用多个字节。
              </small>
            )}
          </label>
          {error && (
            <p className="error" role="alert">
              {error}
            </p>
          )}
          <button className="primary wide" disabled={pending}>
            {pending
              ? "正在处理…"
              : auth.initialized
                ? "登录"
                : "设置密码并继续"}
            <Icon name="arrow-right" />
          </button>
        </form>
      </div>
      <p className="auth-footer">自托管 · 数据保存在你的实例中</p>
    </div>
  );
}

const sourceID = new URLSearchParams(window.location.search).get("source");
function App() {
  const [maintenanceFeedback, setMaintenanceFeedback] = useState("");
  const navigation = useNavigation();
  const [auth, setAuth] = useState<Auth | null>(null);
  const [models, setModels] = useState<Model[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [agentID, setAgentID] = useState("default");
  const agent = agents.find((value) => value.id === agentID) ?? null;
  const model = models.find((value) => value.id === agent?.model_id) ?? null;
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [selected, setSelected] = useState("");
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [connectionLost, setConnectionLost] = useState(false);
  const [ready, setReady] = useState(false);
  const [input, setInput] = useState("");
  const [pending, setPending] = useState(false);
  async function refreshAuth() {
    const state = await api<Auth>("/auth/status");
    csrf = state.csrf;
    setAuth(state);
  }
  async function load() {
    setLoading(true);
    setError("");
    try {
      const [models, agents, list] = await Promise.all([
        api<Model[]>("/models"),
        api<Agent[]>("/agents"),
        api<Conversation[]>("/conversations"),
      ]);
      setModels(models);
      setAgents(agents);
      if (!models.length && !sourceID) navigation.navigate("settings/models");
      setAgentID(list[0]?.agent_id ?? "default");
      setConversations(list);
      setSelected((current) =>
        list.some((item) => item.id === current)
          ? current
          : (list[0]?.id ?? ""),
      );
    } catch (error) {
      setError(errorText(error));
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    const expired = () => {
      void refreshAuth().catch(() => {});
    };
    window.addEventListener("siagent:session-expired", expired);
    return () => window.removeEventListener("siagent:session-expired", expired);
  }, []);
  useEffect(() => {
    void refreshAuth().catch((error) => {
      setError(errorText(error));
      setLoading(false);
    });
  }, []);
  useEffect(() => {
    if (auth?.authenticated) void load();
    else {
      setLoading(false);
      setModels([]);
      setAgents([]);
      setAgentID("default");
      setSelected("");
      setSnapshot(null);
      setConversations([]);
      setInput("");
      setMaintenanceFeedback("");
    }
  }, [auth?.authenticated]);
  useEffect(() => {
    const controller = new AbortController();
    const check = () =>
      fetch("/health/ready", { signal: controller.signal })
        .then((response) => setReady(response.ok))
        .catch(() => {
          if (!controller.signal.aborted) setReady(false);
        });
    void check();
    const timer = setInterval(check, 5000);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, []);
  useEffect(() => {
    setSnapshot(null);
    setConnectionLost(false);
    if (!selected || !auth?.authenticated) return;
    const source = new EventSource(`/api/conversations/${selected}/events`);
    source.addEventListener("snapshot", (event) => {
      const value = JSON.parse(
        (event as MessageEvent<string>).data,
      ) as Snapshot;
      setSnapshot(value);
      setConnectionLost(false);
      setConversations((items) =>
        items.map((item) =>
          item.id === value.conversation.id ? value.conversation : item,
        ),
      );
    });
    source.onerror = () => {
      setConnectionLost(true);
      void refreshAuth().catch(() => {});
    };
    return () => source.close();
  }, [selected, auth?.authenticated]);
  async function createConversation() {
    const conversation = await api<Conversation>("/conversations", "POST", {
      agent_id: agentID,
    });
    setConversations((items) => [conversation, ...items]);
    setSelected(conversation.id);
    return conversation.id;
  }
  async function send(event: FormEvent) {
    event.preventDefault();
    if (!input.trim() || pending) return;
    const submitted = input;
    setPending(true);
    setError("");
    try {
      const conversationID = selected || (await createConversation());
      const run = await api<Run>(
        `/conversations/${conversationID}/runs`,
        "POST",
        { input: submitted },
      );
      setSnapshot((value) =>
        value?.conversation.id === conversationID &&
        !value.runs.some((item) => item.id === run.id)
          ? { ...value, runs: [...value.runs, run] }
          : value,
      );
      setInput((current) => (current === submitted ? "" : current));
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }
  async function cancel(runID: string) {
    setError("");
    try {
      await api(`/runs/${runID}/cancel`, "POST");
    } catch (error) {
      setError(errorText(error));
    }
  }
  async function logout() {
    try {
      await api("/auth/logout", "POST");
      await refreshAuth();
    } catch (error) {
      setError(errorText(error));
    }
  }
  async function refreshCatalog() {
    const [models, agents] = await Promise.all([
      api<Model[]>("/models"),
      api<Agent[]>("/agents"),
    ]);
    setModels(models);
    setAgents(agents);
  }
  function selectAgent(id: string) {
    setAgentID(id);
    setSelected(conversations.find((item) => item.agent_id === id)?.id ?? "");
    setInput("");
    setError("");
    navigation.navigate("chat");
  }
  async function openChannelConversation(id: string) {
    const list = await api<Conversation[]>("/conversations");
    const item = list.find((value) => value.id === id);
    if (!item) throw new Error("会话不存在，请刷新后重试。");
    setConversations(list);
    setAgentID(item.agent_id);
    setSelected(id);
    setInput("");
    navigation.navigate("chat");
  }
  const visibleConversations = conversations.filter(
    (item) => item.agent_id === agentID,
  );
  async function newConversation() {
    if (pending) return;
    setPending(true);
    setError("");
    setMaintenanceFeedback("");
    try {
      await createConversation();
      setInput("");
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }

  if (!auth)
    return (
      <div className="auth-page">
        <div className="brand">
          <Mark />
          SiAgent
        </div>
        <div className="auth-panel">
          {error ? (
            <>
              <p role="alert" className="error">
                {error}
              </p>
              <button
                onClick={() =>
                  void refreshAuth().catch((error) =>
                    setError(errorText(error)),
                  )
                }
              >
                重新连接
              </button>
            </>
          ) : (
            <p role="status">正在连接工作台…</p>
          )}
        </div>
      </div>
    );
  if (!auth.authenticated)
    return <AuthScreen auth={auth} onDone={refreshAuth} />;
  if (sourceID) return <KnowledgeSource request={api} sourceID={sourceID} />;
  return (
    <div className="app-shell">
      <header className="app-header">
        <a className="brand" href="#/chat" aria-label="SiAgent 对话首页">
          <Mark />
          <span>SiAgent</span>
        </a>
        <ModuleSwitch {...navigation} />
        <div className="app-account">
          <span
            className="default-page-label"
            title="默认页面只保存在当前浏览器"
          >
            默认：{pageName(navigation.defaultPage)}
          </span>
          <button
            className="logout"
            aria-label="退出登录"
            onClick={() => void logout()}
          >
            <Icon name="logout" size={16} />
            <span>退出登录</span>
          </button>
        </div>
      </header>
      {navigation.preferenceError && (
        <p className="workspace-alert" role="alert">
          {navigation.preferenceError}
        </p>
      )}
      {error && (
        <p className="workspace-alert" role="alert">
          <span>{error}</span>
          <button
            className="icon-button"
            aria-label="关闭提示"
            onClick={() => setError("")}
          >
            <Icon name="close" />
          </button>
        </p>
      )}
      <div className="app-content">
        {navigation.page === "chat" ? (
          <ChatWorkspace
            agent={agent}
            model={model}
            agents={agents}
            agentID={agentID}
            conversations={visibleConversations}
            selected={selected}
            snapshot={snapshot}
            loading={loading}
            ready={ready}
            pending={pending}
            input={input}
            setInput={setInput}
            send={send}
            cancel={cancel}
            selectAgent={selectAgent}
            selectConversation={(id) => {
              setSelected(id);
              setMaintenanceFeedback("");
            }}
            newConversation={newConversation}
            onDeleted={() => {
              setConversations((items) =>
                items.filter((item) => item.id !== snapshot?.conversation.id),
              );
              setSelected("");
              setSnapshot(null);
              setInput("");
              setMaintenanceFeedback(
                "会话已移出列表，清理请求已保存。模型上下文和历史将在后台清理。",
              );
            }}
            onConfigure={() => navigation.navigate("settings/models")}
            request={api}
            connectionLost={connectionLost}
            maintenanceFeedback={maintenanceFeedback}
          />
        ) : loading ? (
          <div className="empty-view" role="status">
            正在读取配置…
          </div>
        ) : (
          <Configuration
            section={navigation.page.slice(9) as ConfigSection}
            navigate={navigation.navigate}
            request={api}
            models={models}
            agents={agents}
            onSaved={refreshCatalog}
            onStart={() => selectAgent("default")}
            onSelectAgent={selectAgent}
            onConversation={openChannelConversation}
          />
        )}
      </div>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
