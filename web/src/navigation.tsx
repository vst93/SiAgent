import { Icon } from "./icons";
import { useEffect, useState } from "react";

// Two levels, and only two. A page either belongs to the current Agent space
// (its own knowledge, skills, model choice, channels) or to the platform
// (connections, usage, integrations, maintenance). The Agent is the scope, not
// a page, so it is never listed among the pages.
export type Scope = "space" | "platform";

export const configSections = [
  {
    id: "agents",
    scope: "space",
    group: "空间",
    title: "Agent 设置",
    description:
      "配置当前 Agent 的模型、指令与策略；在列表中切换或新建其他 Agent 空间。",
    icon: "agent",
  },
  {
    id: "knowledge",
    scope: "space",
    group: "空间",
    title: "知识库",
    description: "当前 Agent 可检索的资料范围。",
    icon: "knowledge",
  },
  {
    id: "tools",
    scope: "space",
    group: "空间",
    title: "技能与工具",
    description: "当前 Agent 的 Skills 与 MCP 工具授权。",
    icon: "plugins",
  },
  {
    id: "channels",
    scope: "space",
    group: "空间",
    title: "渠道接入",
    description: "把当前 Agent 接到消息平台。",
    icon: "channels",
  },
  {
    id: "models",
    scope: "platform",
    group: "平台",
    title: "模型连接",
    description: "维护可被任意 Agent 选用的模型连接与凭据。",
    icon: "models",
  },
  {
    id: "usage",
    scope: "platform",
    group: "平台",
    title: "用量统计",
    description: "查看模型调用的 token 消耗与趋势；未返回用量的调用按 0 记录。",
    icon: "chart",
  },
  {
    id: "extensions",
    scope: "platform",
    group: "平台",
    title: "扩展插件",
    description: "安装、启停和回滚平台扩展包，查看健康状态与事件。",
    icon: "package",
  },
  {
    id: "api",
    scope: "platform",
    group: "平台",
    title: "集成接口",
    description: "为业务系统创建 API 密钥，调用 Agent 与知识库。",
    icon: "key",
  },
  {
    id: "api-docs",
    scope: "platform",
    group: "平台",
    title: "接口文档",
    description: "查看认证、任务、知识库、Webhook 与 MCP 对接说明。",
    icon: "file",
  },
  {
    id: "system",
    scope: "platform",
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
  return { page, navigate, defaultPage, pin, preferenceError };
}
export function pageName(page: Page) {
  return page === "chat"
    ? "Agent 对话"
    : `配置 · ${configSections.find((section) => page === `settings/${section.id}`)?.title ?? "模型连接"}`;
}
