import { Icon } from "./icons";
import { useEffect, useState } from "react";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type ConfigField = {
  key: string;
  label: string;
  secret: boolean;
  required: boolean;
};
type Plugin = {
  id: string;
  name: string;
  version: string;
  type: string;
  description: string;
  extensions: string[];
  config_fields: ConfigField[];
  values: Record<string, string>;
  has_secret: boolean;
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
  const [uploading, setUploading] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, Record<string, string>>>({});
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
    setInstalled(
      (catalog.installed ?? []).map((plugin) => ({
        ...plugin,
        extensions: plugin.extensions ?? [],
        config_fields: plugin.config_fields ?? [],
        values: plugin.values ?? {},
      })),
    );
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

  async function upload(file: File) {
    setUploading(true);
    setError("");
    setFeedback("");
    try {
      const body = new FormData();
      body.append("file", file);
      await request("/plugins/upload", "POST", body);
      await load();
      setFeedback("扩展包已安装，可以配置并启用。");
    } catch (error) {
      setError(error instanceof Error ? error.message : "扩展包上传失败。");
    } finally {
      setUploading(false);
    }
  }

  async function saveConfig(plugin: Plugin) {
    const draft = drafts[plugin.id] ?? {};
    const values: Record<string, string> = {};
    const secrets: Record<string, string> = {};
    for (const field of plugin.config_fields) {
      const value = draft[field.key] ?? "";
      if (field.secret) {
        if (value) secrets[field.key] = value;
      } else {
        values[field.key] = value;
      }
    }
    await act(
      plugin.id,
      () => request(`/plugins/${plugin.id}/config`, "PUT", { values, secrets }),
      "扩展配置已保存。",
    );
    setDrafts((current) => ({ ...current, [plugin.id]: {} }));
  }

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
      <div className="extension-upload">
        <label>
          {uploading ? "正在安装…" : "上传扩展包（.zip）"}
          <input
            type="file"
            accept=".zip,application/zip"
            disabled={uploading}
            onChange={(event) => {
              const file = event.target.files?.[0];
              if (file) void upload(file);
              event.target.value = "";
            }}
          />
        </label>
        <span>或把包放到数据目录：</span>
      </div>
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
                    <strong title={plugin.name}>{plugin.name}</strong>
                    <small>
                      {plugin.id} · v{plugin.version} · {plugin.type}
                    </small>
                  </div>
                  <span className={`badge ${plugin.healthy ? "success" : ""}`}>
                    {stateNames[plugin.state] ?? plugin.state}
                  </span>
                </header>
                <p>{plugin.message}</p>
                {(plugin.extensions?.length ?? 0) > 0 && (
                  <p className="extension-meta">
                    处理格式：{plugin.extensions.join(" ")}
                  </p>
                )}
                {(plugin.config_fields?.length ?? 0) > 0 && (
                  <div className="extension-config">
                    {(plugin.config_fields ?? []).map((field) => (
                      <label key={field.key}>
                        {field.label}
                        {field.required ? " *" : ""}
                        <input
                          type={field.secret ? "password" : "text"}
                          placeholder={
                            field.secret
                              ? plugin.has_secret
                                ? "已保存，留空保留"
                                : "必填密钥"
                              : plugin.values?.[field.key] ?? ""
                          }
                          value={drafts[plugin.id]?.[field.key] ?? ""}
                          onChange={(event) =>
                            setDrafts((current) => ({
                              ...current,
                              [plugin.id]: {
                                ...(current[plugin.id] ?? {}),
                                [field.key]: event.target.value,
                              },
                            }))
                          }
                        />
                      </label>
                    ))}
                    <button
                      disabled={pending === plugin.id}
                      onClick={() => void saveConfig(plugin)}
                    >
                      保存配置
                    </button>
                  </div>
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
                        () => request(`/plugins/${plugin.id}/health`, "POST"),
                        "健康检查已执行。",
                      )
                    }
                  >
                    健康检查
                  </button>
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
                    <strong title={candidate.name}>{candidate.name}</strong>
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
                    <strong title={item.name}>{item.name}</strong>
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
