import { Icon } from "./icons";
import { useEffect, useState } from "react";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Plugin = {
  id: string;
  name: string;
  version: string;
  type: string;
  description: string;
  extensions: string[];
  enabled: boolean;
  healthy: boolean;
  state: string;
  message: string;
  installed_at: number;
  updated_at: number;
};
type Candidate = {
  id: string;
  name: string;
  version: string;
  type: string;
  installed: boolean;
  current: boolean;
  extensions: string;
};
type Builtin = {
  id: string;
  name: string;
  type: string;
  state: string;
  managed_by: string;
  note: string;
};
type Event = {
  id: number;
  plugin_id: string;
  kind: string;
  message: string;
  created: number;
};

const stateNames: Record<string, string> = {
  ready: "已启用",
  disabled: "未启用",
  missing: "文件缺失",
};

export function ExtensionsWorkspace({ request }: { request: Api }) {
  const [installed, setInstalled] = useState<Plugin[]>([]);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [builtin, setBuiltin] = useState<Builtin[]>([]);
  const [events, setEvents] = useState<Event[]>([]);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState("");
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");

  async function load() {
    const [catalog, log] = await Promise.all([
      request<{
        installed: Plugin[];
        candidates: Candidate[];
        builtin: Builtin[];
      }>("/plugins"),
      request<Event[]>("/plugins/events?limit=12"),
    ]);
    setInstalled(catalog.installed ?? []);
    setCandidates(catalog.candidates ?? []);
    setBuiltin(catalog.builtin ?? []);
    setEvents(log ?? []);
  }
  useEffect(() => {
    void load()
      .catch((error) =>
        setError(error instanceof Error ? error.message : "扩展读取失败。"),
      )
      .finally(() => setLoading(false));
  }, []);

  async function act(id: string, work: () => Promise<unknown>, done: string) {
    setPending(id);
    setError("");
    setFeedback("");
    try {
      await work();
      await load();
      setFeedback(done);
    } catch (error) {
      setError(error instanceof Error ? error.message : "操作未完成。");
    } finally {
      setPending("");
    }
  }

  return (
    <div className="extensions-workspace">
      <p className="kb-help">
        扩展包放在数据目录的 <code>plugin-packages/&lt;id&gt;/&lt;版本&gt;/</code>
        ，每个包包含 <code>plugin.json</code> 与可执行入口。安装只登记版本，不下载或执行安装脚本；扩展以服务账户运行，属于可信代码。
      </p>
      {error && (
        <p className="kb-feedback error" role="alert">
          {error}
        </p>
      )}
      {feedback && (
        <p className="kb-feedback success" role="status">
          {feedback}
        </p>
      )}
      {loading ? (
        <p className="kb-loading" role="status">
          正在读取扩展…
        </p>
      ) : (
        <>
          <section className="extensions-section">
            <h2>已安装</h2>
            {!installed.length && (
              <p className="muted">
                还没有安装扩展。把扩展包放入数据目录后刷新本页。
              </p>
            )}
            {installed.map((plugin) => (
              <article className="extension-card" key={plugin.id}>
                <header>
                  <div>
                    <strong>{plugin.name}</strong>
                    <small>
                      {plugin.id} · v{plugin.version} · {plugin.type}
                    </small>
                  </div>
                  <span className={`badge ${plugin.healthy ? "success" : ""}`}>
                    {stateNames[plugin.state] ?? plugin.state}
                  </span>
                </header>
                <p>{plugin.message}</p>
                {!!plugin.extensions.length && (
                  <p className="extension-meta">
                    处理格式：{plugin.extensions.join(" ")}
                  </p>
                )}
                <div className="extension-actions">
                  {plugin.enabled ? (
                    <button
                      disabled={pending === plugin.id}
                      onClick={() =>
                        void act(
                          plugin.id,
                          () =>
                            request(`/plugins/${plugin.id}/disable`, "POST"),
                          "扩展已停用。",
                        )
                      }
                    >
                      停用
                    </button>
                  ) : (
                    <button
                      className="primary"
                      disabled={pending === plugin.id}
                      onClick={() =>
                        void act(
                          plugin.id,
                          () => request(`/plugins/${plugin.id}/enable`, "POST"),
                          "扩展已启用。",
                        )
                      }
                    >
                      启用
                    </button>
                  )}
                  <button
                    disabled={pending === plugin.id}
                    onClick={() =>
                      void act(
                        plugin.id,
                        () => request(`/plugins/${plugin.id}/rollback`, "POST"),
                        "已回滚到上一个版本。",
                      )
                    }
                  >
                    回滚
                  </button>
                  <button
                    className="danger-button"
                    disabled={pending === plugin.id}
                    onClick={() =>
                      void act(
                        plugin.id,
                        () => request(`/plugins/${plugin.id}`, "DELETE"),
                        "已卸载，磁盘文件保留。",
                      )
                    }
                  >
                    卸载
                  </button>
                </div>
              </article>
            ))}
          </section>

          <section className="extensions-section">
            <h2>可安装版本</h2>
            {!candidates.length && (
              <p className="muted">数据目录中没有发现新的扩展包。</p>
            )}
            {candidates.map((candidate) => (
              <article className="extension-card" key={`${candidate.id}@${candidate.version}`}>
                <header>
                  <div>
                    <strong>{candidate.name}</strong>
                    <small>
                      {candidate.id} · v{candidate.version} · {candidate.type}
                    </small>
                  </div>
                  {candidate.current && <span className="badge success">当前</span>}
                </header>
                {candidate.extensions && (
                  <p className="extension-meta">处理格式：{candidate.extensions}</p>
                )}
                <div className="extension-actions">
                  <button
                    disabled={pending === candidate.id || candidate.current}
                    onClick={() =>
                      void act(
                        candidate.id,
                        () =>
                          request(`/plugins/${candidate.id}/install`, "POST", {
                            version: candidate.version,
                          }),
                        "扩展已安装。",
                      )
                    }
                  >
                    {candidate.current ? "已安装" : "安装此版本"}
                  </button>
                </div>
              </article>
            ))}
          </section>

          <section className="extensions-section">
            <h2>随系统分发</h2>
            {builtin.map((item) => (
              <article className="extension-card" key={item.id}>
                <header>
                  <div>
                    <strong>{item.name}</strong>
                    <small>
                      {item.id} · {item.type}
                    </small>
                  </div>
                  <span className="badge">{item.state}</span>
                </header>
                <p>{item.note}</p>
                {item.managed_by === "channels" && (
                  <div className="extension-actions">
                    <a className="extension-link" href="#/settings/channels">
                      前往渠道接入
                    </a>
                  </div>
                )}
              </article>
            ))}
          </section>

          <section className="extensions-section">
            <h2>最近事件</h2>
            {!events.length && <p className="muted">还没有扩展事件。</p>}
            {!!events.length && (
              <ul className="extension-events">
                {events.map((event) => (
                  <li key={event.id}>
                    <span>{event.kind}</span>
                    <strong>{event.plugin_id}</strong>
                    <span>{event.message}</span>
                    <time>
                      {new Date(event.created).toLocaleString("zh-CN", {
                        hourCycle: "h23",
                      })}
                    </time>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </>
      )}
      <p className="kb-scope-note">
        扩展健康与日志只记录生命周期事件，不复制文件内容或密钥。
      </p>
    </div>
  );
}
