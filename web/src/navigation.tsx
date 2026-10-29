import { Icon } from "./icons";
import { useEffect, useState } from "react";

export const configSections = [
  {
    id: "agents",
    group: "助手配置",
    title: "Agent",
    description:
      "创建助手并选择模型；知识库与技能工具在左侧同级页面准备，再回到这里关联授权。",
    icon: "agent",
  },
  {
    id: "knowledge",
    group: "助手配置",
    title: "知识库",
    description: "整理文档、维护索引；到 Agent 的“知识库关联”中选择使用范围。",
    icon: "knowledge",
  },
  {
    id: "tools",
    group: "助手配置",
    title: "技能与工具",
    description:
      "上传和管理 Skills、配置 MCP 服务；到 Agent 的“工具授权”中逐项授权。",
    icon: "plugins",
  },
  {
    id: "models",
    group: "模型与渠道",
    title: "模型连接",
    description: "连接模型服务，管理密钥并测试可用性；Agent 从这里选择模型。",
    icon: "models",
  },
  {
    id: "usage",
    group: "模型与渠道",
    title: "用量统计",
    description: "查看模型调用的 token 消耗与趋势；未返回用量的调用按 0 记录。",
    icon: "chart",
  },
  {
    id: "channels",
    group: "模型与渠道",
    title: "渠道接入",
    description: "连接消息平台并绑定 Agent，让助手在群聊或私聊中回复。",
    icon: "channels",
  },
  {
    id: "extensions",
    group: "扩展与集成",
    title: "扩展插件",
    description: "安装、启停和回滚平台扩展包，查看健康状态与事件。",
    icon: "package",
  },
  {
    id: "api",
    group: "扩展与集成",
    title: "集成接口",
    description: "为业务系统创建 API 密钥，调用 Agent 与知识库。",
    icon: "key",
  },
  {
    id: "api-docs",
    group: "扩展与集成",
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
