import { Icon } from "./icons";
import { type Agent, type Model } from "./agents";
import { KnowledgeSource } from "./knowledge";
import {
  ChatWorkspace,
  type Attachment,
  type Conversation,
  type Snapshot,
  type Run,
} from "./chat";
import { Configuration } from "./configuration";
import { CommandPalette, type CommandItem } from "./command-palette";
import {
  configSections,
  pageName,
  useNavigation,
  type ConfigSection,
} from "./navigation";
import { FormEvent, Fragment, useEffect, useRef, useState } from "react";
import { appURL } from "./urls";
import {
  applyLanguage,
  rememberLanguage,
  storedLanguage,
  type Language,
} from "./i18n";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import "./style.css";
import {
  applyTheme,
  nextThemeMode,
  rememberTheme,
  storedThemeMode,
  themeForMode,
  themeModeLabel,
  type Theme,
  type ThemeMode,
} from "./theme";

let currentThemeMode: ThemeMode = storedThemeMode();
applyTheme(themeForMode(currentThemeMode));
// Automatic mode re-evaluates at the day/night boundary without a reload.
window.setInterval(() => {
  if (currentThemeMode === "auto") {
    const theme = themeForMode(currentThemeMode);
    if (document.documentElement.dataset.theme !== theme) applyTheme(theme);
  }
}, 60 * 1000);
const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
const commandKey = isMac ? "⌘K" : "Ctrl K";

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
  const response = await fetch(appURL(`/api${path}`), {
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
  const raw = await response.text();
  const data = raw ? JSON.parse(raw) : {};
  if (!response.ok) {
    if (response.status === 401 && csrf)
      window.dispatchEvent(new Event("siagent:session-expired"));
    throw new Error(data.error ?? "操作未完成，请刷新后重试。");
  }
  return data as T;
}
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "无法连接服务，请检查网络后重试。";

function LanguageToggle({
  language,
  onChange,
}: {
  language: Language;
  onChange: (language: Language) => void;
}) {
  const next = language === "zh" ? "en" : "zh";
  return (
    <button
      className="language-toggle"
      data-i18n-skip
      aria-label={next === "en" ? "Switch to English" : "Switch to Chinese"}
      title={next === "en" ? "Switch to English" : "Switch to Chinese"}
      onClick={() => onChange(next)}
    >
      {next === "en" ? "EN" : "中文"}
    </button>
  );
}

function ThemeToggle({
  mode,
  theme,
  onChange,
  className = "",
}: {
  mode: ThemeMode;
  theme: Theme;
  onChange: (mode: ThemeMode) => void;
  className?: string;
}) {
  const next = nextThemeMode(mode);
  const action =
    next === "auto"
      ? "切换到自动（跟随时间）"
      : next === "dark"
        ? "切换到深色模式"
        : "切换到浅色模式";
  return (
    <button
      type="button"
      className={`theme-toggle ${className} ${
        mode === "auto" ? "auto" : ""
      }`.trim()}
      aria-label={`${themeModeLabel(mode)}，${action}`}
      title={`${themeModeLabel(mode)}，${action}`}
      data-mode={mode}
      onClick={() => onChange(next)}
    >
      <Icon
        name={mode === "auto" ? "auto" : theme === "light" ? "moon" : "sun"}
        size={16}
      />
    </button>
  );
}

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
  language,
  onLanguage,
  theme,
  themeMode,
  onTheme,
}: {
  auth: Auth;
  onDone: () => Promise<void>;
  language: Language;
  onLanguage: (language: Language) => void;
  theme: Theme;
  themeMode: ThemeMode;
  onTheme: (mode: ThemeMode) => void;
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
      <header className="auth-topbar">
        <div className="brand">
          <Mark /> <span>SiAgent</span>
        </div>
        <div className="auth-topbar-actions">
          <LanguageToggle language={language} onChange={onLanguage} />
          <ThemeToggle
            mode={themeMode}
            theme={theme}
            onChange={onTheme}
          />
        </div>
      </header>
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
  const [menuOpen, setMenuOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const [railCollapsed, setRailCollapsed] = useState(() => {
    try {
      return localStorage.getItem("siagent.rail-collapsed") === "true";
    } catch {
      return false;
    }
  });
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const menuCloseRef = useRef<HTMLButtonElement>(null);
  const menuWasOpen = useRef(false);
  const [language, setLanguage] = useState<Language>(storedLanguage);
  const [themeMode, setThemeMode] = useState<ThemeMode>(storedThemeMode);
  const [theme, setTheme] = useState<Theme>(() =>
    themeForMode(storedThemeMode()),
  );
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
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const [pending, setPending] = useState(false);
  useEffect(() => {
    rememberLanguage(language);
    return applyLanguage(language);
  }, [language]);
  // One place resolves the mode into a concrete theme, remembers the choice and
  // keeps the automatic boundary check in sync.
  useEffect(() => {
    currentThemeMode = themeMode;
    const resolved = themeForMode(themeMode);
    setTheme(resolved);
    applyTheme(resolved);
    rememberTheme(themeMode);
    if (themeMode !== "auto") return;
    const timer = window.setInterval(() => {
      const next = themeForMode(themeMode);
      setTheme(next);
      applyTheme(next);
    }, 60 * 1000);
    return () => window.clearInterval(timer);
  }, [themeMode]);
  useEffect(() => {
    try {
      localStorage.setItem(
        "siagent.rail-collapsed",
        String(railCollapsed),
      );
    } catch {
      // The rail still changes for this session.
    }
  }, [railCollapsed]);
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setCommandOpen((value) => !value);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);
  useEffect(() => {
    if (!menuOpen) return;
    const handler = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMenuOpen(false);
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [menuOpen]);
  useEffect(() => {
    if (menuOpen) {
      menuWasOpen.current = true;
      window.setTimeout(() => menuCloseRef.current?.focus(), 0);
    } else if (menuWasOpen.current) {
      menuWasOpen.current = false;
      menuButtonRef.current?.focus();
    }
  }, [menuOpen]);
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
      fetch(appURL("/health/ready"), { signal: controller.signal })
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
    let subscribed = true;
    const source = new EventSource(
      appURL(`/api/conversations/${encodeURIComponent(selected)}/events`),
    );
    source.addEventListener("snapshot", (event) => {
      const value = JSON.parse(
        (event as MessageEvent<string>).data,
      ) as Snapshot;
      if (!subscribed || value.conversation.id !== selected) return;
      setSnapshot(value);
      setConnectionLost(false);
      setConversations((items) =>
        items.map((item) =>
          item.id === value.conversation.id ? value.conversation : item,
        ),
      );
    });
    source.onerror = () => {
      if (!subscribed) return;
      setConnectionLost(true);
      void refreshAuth().catch(() => {});
    };
    return () => {
      subscribed = false;
      source.close();
    };
  }, [selected, auth?.authenticated]);
  async function createConversation() {
    const conversation = await api<Conversation>("/conversations", "POST", {
      agent_id: agentID,
    });
    setConversations((items) => [conversation, ...items]);
    setSelected(conversation.id);
    return conversation.id;
  }
  async function uploadFiles(files: FileList) {
    if (uploading || pending) return;
    setUploading(true);
    setError("");
    try {
      const conversationID = selected || (await createConversation());
      const uploaded: Attachment[] = [];
      for (const file of Array.from(files)) {
        const body = new FormData();
        body.append("file", file);
        uploaded.push(
          await api<Attachment>(
            `/conversations/${conversationID}/attachments`,
            "POST",
            body,
          ),
        );
      }
      setAttachments((current) => [...current, ...uploaded]);
    } catch (error) {
      setError(errorText(error));
    } finally {
      setUploading(false);
    }
  }
  function removeAttachment(id: string) {
    setAttachments((current) =>
      current.filter((attachment) => attachment.id !== id),
    );
    void api(`/attachments/${id}`, "DELETE").catch(() => {});
  }
  async function send(event: FormEvent) {
    event.preventDefault();
    if ((!input.trim() && !attachments.length) || pending || uploading) return;
    if (
      selected &&
      (snapshot?.conversation.id !== selected || snapshot.conversation.channel_id)
    ) return;
    const submitted = input;
    setPending(true);
    setError("");
    try {
      const conversationID = selected || (await createConversation());
      const run = await api<Run>(
        `/conversations/${conversationID}/runs`,
        "POST",
        {
          input: submitted,
          attachment_ids: attachments.map((attachment) => attachment.id),
        },
      );
      setSnapshot((value) =>
        value?.conversation.id === conversationID &&
        !value.runs.some((item) => item.id === run.id)
          ? { ...value, runs: [...value.runs, run] }
          : value,
      );
      setInput((current) => (current === submitted ? "" : current));
      setAttachments([]);
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
    setAttachments([]);
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
      setAttachments([]);
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
          <div className="auth-language">
            <LanguageToggle language={language} onChange={setLanguage} />
            <ThemeToggle
              mode={themeMode}
              theme={theme}
              onChange={setThemeMode}
            />
          </div>
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
    return (
      <>
        <AuthScreen
          auth={auth}
          onDone={refreshAuth}
          language={language}
          onLanguage={setLanguage}
          theme={theme}
          themeMode={themeMode}
          onTheme={setThemeMode}
        />
      </>
    );
  if (sourceID) return <KnowledgeSource request={api} sourceID={sourceID} />;
  const isDefaultPage = navigation.defaultPage === navigation.page;
  const commandItems: CommandItem[] = [
    {
      id: "chat",
      label: "Agent 对话",
      hint: "工作区",
      icon: "chat",
      run: () => navigation.navigate("chat"),
    },
    ...configSections.map((item) => ({
      id: `settings/${item.id}`,
      label: item.title,
      hint: item.group,
      icon: item.icon,
      run: () => navigation.navigate(`settings/${item.id}`),
    })),
    {
      id: "new-chat",
      label: "新对话",
      hint: "操作",
      icon: "plus",
      run: () => {
        navigation.navigate("chat");
        void newConversation();
      },
    },
    {
      id: "pin",
      label: isDefaultPage ? "当前页已是默认页" : "将当前页设为默认页",
      hint: "导航",
      icon: "star",
      run: () => navigation.pin(navigation.page),
    },
    {
      id: "theme",
      label: themeModeLabel(themeMode),
      hint: "外观",
      icon:
        themeMode === "auto" ? "auto" : theme === "light" ? "moon" : "sun",
      run: () => setThemeMode(nextThemeMode(themeMode)),
    },
    {
      id: "logout",
      label: "退出登录",
      hint: "账户",
      icon: "logout",
      run: () => void logout(),
    },
  ];
  return (
    <div className={`app-shell ${railCollapsed ? "rail-collapsed" : ""}`}>
      <header className="mobile-topbar">
        <button
          ref={menuButtonRef}
          className="mobile-menu-button"
          aria-label="打开导航"
          aria-expanded={menuOpen}
          onClick={() => setMenuOpen(true)}
        >
          <Icon name="menu" size={18} />
        </button>
        <span className="mobile-title">{pageName(navigation.page)}</span>
        <button
          type="button"
          className="mobile-command-button"
          aria-label="搜索或跳转"
          onClick={() => setCommandOpen(true)}
        >
          <Icon name="search" size={16} />
        </button>
        <ThemeToggle
          mode={themeMode}
          theme={theme}
          onChange={setThemeMode}
          className="mobile-theme-toggle"
        />
      </header>
      <aside className="app-rail" aria-label="应用侧栏">
        <div className="rail-head">
          <a
            className="rail-brand"
            href={`#/${navigation.defaultPage}`}
            aria-label="打开默认页面"
            title="SiAgent"
          >
            <Mark />
            <span>SiAgent</span>
            <span className="rail-version">自托管</span>
          </a>
          <button
            type="button"
            className="rail-collapse"
            aria-label={railCollapsed ? "展开菜单" : "收起菜单"}
            aria-expanded={!railCollapsed}
            title={railCollapsed ? "展开菜单" : "收起菜单"}
            onClick={() => setRailCollapsed((value) => !value)}
          >
            <Icon name="sidebar" size={16} />
          </button>
        </div>
        <button
          type="button"
          className="rail-command"
          aria-keyshortcuts="Control+K Meta+K"
          title="搜索或跳转"
          onClick={() => setCommandOpen(true)}
        >
          <Icon name="search" size={16} />
          <span>搜索或跳转</span>
          <kbd>{commandKey}</kbd>
        </button>
        <nav className="rail-nav" aria-label="主导航">
          <p className="rail-group">工作区</p>
          <button
            className={`rail-link ${navigation.page === "chat" ? "selected" : ""}`}
            aria-current={navigation.page === "chat" ? "page" : undefined}
            title="Agent 对话"
            onClick={() => navigation.navigate("chat")}
          >
            <Icon name="chat" size={16} />
            <span>Agent 对话</span>
          </button>
          {configSections.map((item, index) => (
            <Fragment key={item.id}>
              {(index === 0 ||
                configSections[index - 1].group !== item.group) && (
                <p className="rail-group">{item.group}</p>
              )}
              <button
                className={`rail-link ${
                  navigation.page === `settings/${item.id}` ? "selected" : ""
                }`}
                aria-current={
                  navigation.page === `settings/${item.id}`
                    ? "page"
                    : undefined
                }
                title={item.title}
                onClick={() => navigation.navigate(`settings/${item.id}`)}
              >
                <Icon name={item.icon} size={16} />
                <span>{item.title}</span>
              </button>
            </Fragment>
          ))}
        </nav>
        <div className="rail-footer">
          <div className="rail-status">
            <span className={ready ? "status-dot" : "status-dot offline"} />
            <span>{ready ? "服务已就绪" : "服务正在恢复"}</span>
          </div>
          <div className="rail-actions">
            <button
              className="rail-icon-button"
              aria-pressed={isDefaultPage}
              aria-label={isDefaultPage ? "当前页已是默认页" : "将当前页设为默认页"}
              title={`当前默认：${pageName(navigation.defaultPage)}（只保存在当前浏览器）`}
              onClick={() => navigation.pin(navigation.page)}
            >
              <Icon name="star" size={16} filled={isDefaultPage} />
            </button>
            <LanguageToggle language={language} onChange={setLanguage} />
            <ThemeToggle
              mode={themeMode}
              theme={theme}
              onChange={setThemeMode}
              className="rail-icon-button"
            />
            <button
              className="rail-icon-button"
              aria-label="退出登录"
              title="退出登录"
              onClick={() => void logout()}
            >
              <Icon name="logout" size={16} />
            </button>
          </div>
        </div>
      </aside>
      <div
        className={`mobile-menu-backdrop ${menuOpen ? "open" : ""}`}
        onClick={() => setMenuOpen(false)}
      />
      <aside
        className={`mobile-menu ${menuOpen ? "open" : ""}`}
        aria-label="导航菜单"
        aria-hidden={!menuOpen}
        inert={!menuOpen}
      >
        <header>
          <span className="brand">
            <Mark />
            SiAgent
          </span>
          <button
            ref={menuCloseRef}
            className="icon-button"
            aria-label="关闭导航"
            onClick={() => setMenuOpen(false)}
          >
            <Icon name="close" />
          </button>
        </header>
        <nav aria-label="移动导航">
          <p className="mobile-menu-label">工作区</p>
          <button
            className={navigation.page === "chat" ? "selected" : ""}
            onClick={() => {
              navigation.navigate("chat");
              setMenuOpen(false);
            }}
          >
            <Icon name="chat" size={18} /> Agent 对话
          </button>
          {configSections.map((item, index) => (
            <Fragment key={item.id}>
              {(index === 0 ||
                configSections[index - 1].group !== item.group) && (
                <p className="mobile-menu-label">{item.group}</p>
              )}
              <button
                className={
                  navigation.page === `settings/${item.id}` ? "selected" : ""
                }
                onClick={() => {
                  navigation.navigate(`settings/${item.id}`);
                  setMenuOpen(false);
                }}
              >
                <Icon name={item.icon} size={18} /> {item.title}
              </button>
            </Fragment>
          ))}
        </nav>
        <div className="mobile-menu-footer">
          <div className="mobile-menu-actions">
            <LanguageToggle language={language} onChange={setLanguage} />
            <ThemeToggle
              mode={themeMode}
              theme={theme}
              onChange={setThemeMode}
              className="mobile-theme-toggle"
            />
          </div>
          <p>
            默认页面：{pageName(navigation.defaultPage)}
            <button
              onClick={() => {
                navigation.pin(navigation.page);
                setMenuOpen(false);
              }}
            >
              设为默认页
            </button>
          </p>
          <button className="mobile-logout" onClick={() => void logout()}>
            <Icon name="logout" size={16} /> 退出登录
          </button>
        </div>
      </aside>
      <div className="app-content">
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
        <div className="app-workspace">
          {navigation.page === "chat" ? (
            <ChatWorkspace
              agent={agent}
              model={model}
              agents={agents}
              agentID={agentID}
              conversations={visibleConversations}
              selected={selected}
              snapshot={snapshot?.conversation.id === selected ? snapshot : null}
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
                setAttachments([]);
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
                setAttachments([]);
                setMaintenanceFeedback(
                  "会话已移出列表，清理请求已保存。模型上下文和历史将在后台清理。",
                );
              }}
              onConfigure={() => navigation.navigate("settings/models")}
              request={api}
              connectionLost={connectionLost}
              maintenanceFeedback={maintenanceFeedback}
              attachments={attachments}
              uploading={uploading}
              onUpload={uploadFiles}
              onRemoveAttachment={removeAttachment}
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
              language={language}
            />
          )}
        </div>
      </div>
      <CommandPalette
        open={commandOpen}
        items={commandItems}
        onClose={() => setCommandOpen(false)}
      />
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
