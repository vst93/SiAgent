import { Icon } from "./icons";
import { useEffect, useRef } from "react";
import { AgentWorkspace, type Agent, type Model } from "./agents";
import { ModelsWorkspace } from "./models";
import { KnowledgeWorkspace } from "./knowledge";
import { PluginsWorkspace } from "./plugins";
import { ExtensionsWorkspace } from "./extensions";
import { ChannelsWorkspace } from "./channels";
import { ExecutionSettings } from "./runtime-controls";
import { ApiKeysWorkspace } from "./api-keys";
import { ApiDocsWorkspace } from "./api-docs";
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
        <nav ref={navRef} aria-label="配置导航">
          {configSections.map((item, index) => (
            <div key={item.id}>
              {(index === 0 || configSections[index - 1].group !== item.group) && (
                <p className="nav-group-label">{item.group}</p>
              )}
              <a
                href={`#/settings/${item.id}`}
                onClick={(event) => {
                  event.preventDefault();
                  navigate(`settings/${item.id}`);
                }}
                className={`configuration-link ${section === item.id ? "selected" : ""}`}
                aria-current={section === item.id ? "page" : undefined}
              >
                <Icon name={item.icon} size={18} />
                {item.title}
              </a>
            </div>
          ))}
        </nav>
      </aside>
      <main className="configuration-main">
        <header className="configuration-heading">
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
          {section === "extensions" && <ExtensionsWorkspace request={request} />}
          {section === "channels" && (
            <ChannelsWorkspace
              request={request}
              agents={agents}
              onConversation={onConversation}
            />
          )}
          {section === "system" && <ExecutionSettings request={request} />}
          {section === "api" && (
            <ApiKeysWorkspace request={request} agents={agents} />
          )}
          {section === "api-docs" && <ApiDocsWorkspace />}
        </div>
      </main>
    </section>
  );
}
