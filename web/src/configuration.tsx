import { Icon } from "./icons";
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
import type { Language } from "./i18n";
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
  language,
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
  language: Language;
}) {
  const current = configSections.find((item) => item.id === section)!;
  return (
    <section className="configuration-workspace" aria-label="配置工作区">
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
          {section === "api-docs" && <ApiDocsWorkspace language={language} />}
        </div>
      </main>
    </section>
  );
}
