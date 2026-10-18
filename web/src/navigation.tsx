import { Icon } from "./icons";
import { useEffect, useState } from "react";

export const configSections = [
  {
    id: "agents",
    group: "Agent 与知识",
    title: "Agent",
    description: "定义助手角色，为每个 Agent 选择模型与授权能力。",
    icon: "agent",
  },
  {
    id: "knowledge",
    group: "Agent 与知识",
    title: "知识库",
    description: "整理文档、维护索引，测试检索与查看来源。",
    icon: "knowledge",
  },
  {
    id: "models",
    group: "模型与工具",
    title: "模型连接",
    description: "连接模型服务，管理密钥并测试可用性。",
    icon: "models",
  },
  {
    id: "tools",
    group: "模型与工具",
    title: "插件接入",
    description: "上传和管理 Skills，配置 MCP 服务，再到 Agent 中挂载和授权。",
    icon: "plugins",
  },
  {
    id: "extensions",
    group: "模型与工具",
    title: "扩展",
    description: "安装、启停和回滚扩展包，查看健康状态与事件。",
    icon: "plugins",
  },
  {
    id: "channels",
    group: "接入与集成",
    title: "渠道接入",
    description: "选择聊天渠道适配器，连接消息平台并绑定 Agent。",
    icon: "channels",
  },
  {
    id: "api",
    group: "接入与集成",
    title: "集成接口",
    description: "为业务系统创建 API 密钥，调用 Agent 与知识库。",
    icon: "models",
  },
  {
    id: "api-docs",
    group: "接入与集成",
    title: "接口文档",
    description: "查看认证、任务、知识库、Webhook 与 MCP 对接说明。",
    icon: "file",
  },
  {
    id: "system",
    group: "系统",
    title: "运行与维护",
    description: "调整执行限制，检查服务状态和磁盘用量。",
    icon: "settings",
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
  lastConfig,
  navigate,
}: Pick<
  ReturnType<typeof useNavigation>,
  "page" | "lastConfig" | "navigate"
>) {
  const configTarget = page === "chat" ? lastConfig : page;
  return (
    <nav className="module-switch" aria-label="主模块">
      <button
        className={`module-link ${page === "chat" ? "active" : ""}`}
        aria-current={page === "chat" ? "page" : undefined}
        onClick={() => navigate("chat")}
      >
        <Icon name="chat" size={16} />
        Agent 对话
      </button>
      <button
        className={`module-link ${page !== "chat" ? "active" : ""}`}
        aria-current={page !== "chat" ? "page" : undefined}
        onClick={() => navigate(configTarget)}
      >
        <Icon name="settings" size={16} />
        配置
      </button>
    </nav>
  );
}
