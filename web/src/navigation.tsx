import { useEffect, useState } from "react";

export const configSections = [
  {
    id: "models",
    title: "模型连接",
    description: "连接模型服务，管理密钥并测试可用性。",
    icon: "◈",
  },
  {
    id: "agents",
    title: "Agent",
    description: "定义助手角色，为每个 Agent 选择模型与授权能力。",
    icon: "◎",
  },
  {
    id: "knowledge",
    title: "知识库",
    description: "整理文档、维护索引，测试检索与查看来源。",
    icon: "▤",
  },
  {
    id: "tools",
    title: "插件接入",
    description: "上传和管理 Skills，配置 MCP 服务，再到 Agent 中挂载和授权。",
    icon: "⊞",
  },
  {
    id: "channels",
    title: "渠道接入",
    description: "配置企业微信内部应用，将消息交给指定 Agent。",
    icon: "⇄",
  },
  {
    id: "system",
    title: "运行与维护",
    description: "调整执行限制，检查服务状态和磁盘用量。",
    icon: "⚙",
  },
] as const;
export type ConfigSection = (typeof configSections)[number]["id"];
export type Page = "chat" | `settings/${ConfigSection}`;
const preferenceKey = "siagent.default-page";
const validPage = (value: string | null): value is Page =>
  value === "chat" ||
  configSections.some((section) => value === `settings/${section.id}`);
const hashPage = () => {
  const value = window.location.hash.replace(/^#\/?/, "");
  return validPage(value) ? value : null;
};
function storedDefault(): Page {
  try {
    const value = localStorage.getItem(preferenceKey);
    return validPage(value) ? value : "chat";
  } catch {
    return "chat";
  }
}
export function useNavigation() {
  const [page, setPage] = useState<Page>(() => hashPage() ?? storedDefault());
  const [defaultPage, setDefaultPage] = useState<Page>(storedDefault);
  const [preferenceError, setPreferenceError] = useState("");
  const [lastConfig, setLastConfig] = useState<Page>(() =>
    page === "chat" ? "settings/models" : page,
  );
  useEffect(() => {
    const changed = () => setPage(hashPage() ?? storedDefault());
    const synced = (event: StorageEvent) => {
      if (event.key === preferenceKey || event.key === null)
        setDefaultPage(storedDefault());
    };
    window.addEventListener("hashchange", changed);
    window.addEventListener("storage", synced);
    return () => {
      window.removeEventListener("hashchange", changed);
      window.removeEventListener("storage", synced);
    };
  }, []);
  useEffect(() => {
    if (page !== "chat") setLastConfig(page);
  }, [page]);
  function navigate(next: Page) {
    setPage(next);
    if (hashPage() !== next) window.location.hash = `/${next}`;
  }
  function pin(next: Page) {
    try {
      localStorage.setItem(preferenceKey, next);
      setDefaultPage(next);
      setPreferenceError("");
    } catch {
      setPreferenceError(
        "浏览器未允许保存默认页面。仍可切换页面，但关闭后无法记住此选择。",
      );
    }
  }
  return { page, navigate, defaultPage, pin, preferenceError, lastConfig };
}
export function pageName(page: Page) {
  return page === "chat"
    ? "Agent 对话"
    : `配置 · ${configSections.find((section) => page === `settings/${section.id}`)?.title ?? "模型连接"}`;
}
export function ModuleSwitch({
  page,
  defaultPage,
  lastConfig,
  navigate,
  pin,
}: Pick<
  ReturnType<typeof useNavigation>,
  "page" | "defaultPage" | "lastConfig" | "navigate" | "pin"
>) {
  const configTarget = page === "chat" ? lastConfig : page;
  return (
    <nav className="module-switch" aria-label="主模块">
      <div className={`module-option ${page === "chat" ? "active" : ""}`}>
        <button
          className="module-link"
          aria-current={page === "chat" ? "page" : undefined}
          onClick={() => navigate("chat")}
        >
          Agent 对话
        </button>
        <button
          className="module-pin"
          aria-label="设 Agent 对话为默认页面"
          aria-pressed={defaultPage === "chat"}
          title="默认打开 Agent 对话"
          onClick={() => pin("chat")}
        >
          {defaultPage === "chat" ? "★" : "☆"}
        </button>
      </div>
      <div className={`module-option ${page !== "chat" ? "active" : ""}`}>
        <button
          className="module-link"
          aria-current={page !== "chat" ? "page" : undefined}
          onClick={() => navigate(configTarget)}
        >
          配置
        </button>
        <button
          className="module-pin"
          aria-label="设当前配置页为默认页面"
          aria-pressed={defaultPage === configTarget}
          title={`默认打开${pageName(configTarget)}`}
          onClick={() => pin(configTarget)}
        >
          {defaultPage === configTarget ? "★" : "☆"}
        </button>
      </div>
    </nav>
  );
}
