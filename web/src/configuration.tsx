import { useEffect, useRef } from "react";
import { AgentWorkspace, type Agent, type Model } from "./agents";
import { ModelsWorkspace } from "./models";
import { KnowledgeWorkspace } from "./knowledge";
import { PluginsWorkspace } from "./plugins";
import { WecomWorkspace } from "./wecom";
import { ExecutionSettings } from "./runtime-controls";
import { configSections, type ConfigSection, type Page } from "./navigation";
type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
export function Configuration({
  section,
  navigate,
  request,
  agents,
  models,
  onSaved,
  onSelectAgent,
  onConversation,
  onStart,
}: {
  section: ConfigSection;
  navigate: (page: Page) => void;
  request: Api;
  agents: Agent[];
  models: Model[];
  onSaved: () => Promise<void>;
  onSelectAgent: (id: string) => void;
  onConversation: (id: string) => Promise<void>;
  onStart: () => void;
}) {
  const current = configSections.find((item) => item.id === section)!;
  const navRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const nav = navRef.current;
    if (nav && nav.scrollWidth > nav.clientWidth)
      nav
        .querySelector<HTMLElement>('[aria-current="page"]')
        ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [section]);
  return (
    <section className="configuration-workspace" aria-label="配置工作区">
      <aside className="configuration-sidebar">
        <div className="configuration-intro">
          <p className="eyebrow">工作区配置</p>
          <h2>让 Agent 为你工作</h2>
          <p>连接模型与资源，再通过对话或渠道使用。</p>
        </div>
        <nav ref={navRef} aria-label="配置导航">
          {configSections.map((item, index) => (
            <div key={item.id}>
              {index === 0 && <p className="nav-group-label">Agent 与能力</p>}
              {index === 4 && <p className="nav-group-label">服务与维护</p>}
              <a
                href={`#/settings/${item.id}`}
                onClick={(event) => {
                  event.preventDefault();
                  navigate(`settings/${item.id}`);
                }}
                className={`configuration-link ${section === item.id ? "selected" : ""}`}
                aria-current={section === item.id ? "page" : undefined}
              >
                <span aria-hidden="true">{item.icon}</span>
                {item.title}
              </a>
            </div>
          ))}
        </nav>
        <p className="configuration-tip">
          常用哪个页面，就点顶部的 ☆ 将它设为默认打开。
        </p>
      </aside>
      <main className="configuration-main">
        <header className="configuration-heading">
          <p className="eyebrow">配置 / {current.title}</p>
          <h1>{current.title}</h1>
          <p>{current.description}</p>
        </header>
        <div className="configuration-content" key={section}>
          {section === "models" && (
            <ModelsWorkspace
              request={request}
              models={models}
              onSaved={onSaved}
              onStart={onStart}
            />
          )}
          {section === "agents" && (
            <AgentWorkspace
              request={request}
              agents={agents}
              models={models}
              onSaved={onSaved}
              onSelect={onSelectAgent}
            />
          )}
          {section === "knowledge" && <KnowledgeWorkspace request={request} />}
          {section === "tools" && <PluginsWorkspace request={request} />}
          {section === "channels" && (
            <WecomWorkspace
              request={request}
              agents={agents}
              onConversation={onConversation}
            />
          )}
          {section === "system" && <ExecutionSettings request={request} />}
        </div>
      </main>
    </section>
  );
}
