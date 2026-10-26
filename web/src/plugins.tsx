import { Icon } from "./icons";
import {
  FormEvent,
  InputHTMLAttributes,
  useEffect,
  useMemo,
  useState,
} from "react";
import { CapabilitySettings, type Skill } from "./capabilities";
import { MessageMarkdown } from "./message-markdown";
import { LinkedAgents } from "./linked-agents";
import { KnowledgeDialog as SettingsDialog } from "./knowledge-dialog";
import { appURL } from "./urls";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作未完成，请稍后重试。";
const size = (bytes: number) =>
  bytes < 1024
    ? `${bytes} B`
    : bytes >= 1048576
      ? `${(bytes / 1048576).toFixed(1)} MiB`
      : `${(bytes / 1024).toFixed(1)} KiB`;

// Downloads are authenticated by the session cookie; the response is streamed
// as a ZIP, so it cannot go through the JSON request helper.
async function downloadArchive(path: string, fallback: string) {
  const response = await fetch(appURL(`/api${path}`));
  if (!response.ok) {
    if (response.status === 401)
      window.dispatchEvent(new Event("siagent:session-expired"));
    let message = "导出未完成，请稍后重试。";
    try {
      const data = (await response.json()) as { error?: string };
      if (data.error) message = data.error;
    } catch {
      // Keep the generic message for non-JSON failures.
    }
    throw new Error(message);
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(disposition)?.[1];
  const plain = /filename="([^"]+)"/i.exec(disposition)?.[1];
  const filename = encoded
    ? decodeURIComponent(encoded)
    : (plain ?? fallback);
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

export function PluginsWorkspace({ request }: { request: Api }) {
  const [tab, setTab] = useState<"skills" | "mcp">("skills");
  const [mcpVisited, setMCPVisited] = useState(false);
  function select(value: typeof tab) {
    setTab(value);
    if (value === "mcp") setMCPVisited(true);
  }
  return (
    <section className="plugins-workspace">
      <div
        className="plugin-tabs"
        role="tablist"
        aria-label="插件类型"
        onKeyDown={(event) => {
          if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key))
            return;
          event.preventDefault();
          const next =
            event.key === "Home"
              ? "skills"
              : event.key === "End"
                ? "mcp"
                : tab === "skills"
                  ? "mcp"
                  : "skills";
          select(next);
          event.currentTarget
            .querySelector<HTMLButtonElement>(`#plugin-tab-${next}`)
            ?.focus();
        }}
      >
        <button
          role="tab"
          id="plugin-tab-skills"
          aria-selected={tab === "skills"}
          aria-controls="plugin-skills"
          tabIndex={tab === "skills" ? 0 : -1}
          onClick={() => select("skills")}
        >
          Skills <span>技能文件</span>
        </button>
        <button
          role="tab"
          id="plugin-tab-mcp"
          aria-selected={tab === "mcp"}
          aria-controls="plugin-mcp"
          tabIndex={tab === "mcp" ? 0 : -1}
          onClick={() => select("mcp")}
        >
          MCP <span>工具服务</span>
        </button>
      </div>
      <div
        role="tabpanel"
        id="plugin-skills"
        aria-labelledby="plugin-tab-skills"
        hidden={tab !== "skills"}
      >
        <SkillCatalog request={request} />
      </div>
      <div
        role="tabpanel"
        id="plugin-mcp"
        className="plugin-mcp"
        aria-labelledby="plugin-tab-mcp"
        hidden={tab !== "mcp"}
      >
        {mcpVisited && <CapabilitySettings request={request} catalog />}
      </div>
    </section>
  );
}

type UploadFile = { file: File; path: string };
type UploadResult = Skill | { skills: Skill[]; replaced: number };
function SkillUpload({
  request,
  onClose,
  onUploaded,
}: {
  request: Api;
  onClose: () => void;
  onUploaded: (skills: Skill[]) => void;
}) {
  const [mode, setMode] = useState<"package" | "folder">("package");
  const [files, setFiles] = useState<UploadFile[]>([]);
  const [trusted, setTrusted] = useState(false);
  const [overwrite, setOverwrite] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const bytes = files.reduce((sum, item) => sum + item.file.size, 0);
  const folderAttributes = {
    webkitdirectory: "",
    directory: "",
  } as InputHTMLAttributes<HTMLInputElement>;
  function choose(values: FileList | null) {
    const entries = Array.from(values ?? []).map((file) => ({
      file,
      path:
        mode === "folder"
          ? file.webkitRelativePath.split("/").slice(1).join("/")
          : file.name,
    }));
    setFiles(entries);
    setTrusted(false);
    setError("");
  }
  async function upload(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!files.length) return;
    // A folder may hold one skill or a bundle of top-level skill folders.
    const singleSkill = files.some((item) => item.path === "SKILL.md");
    const overLimit = singleSkill
      ? files.length > 200 || bytes > 8 * 1048576
      : files.length > 2000 || bytes > 80 * 1048576;
    if (
      mode === "folder" &&
      (overLimit || files.some((item) => item.file.size > 2 * 1048576))
    ) {
      setError(
        singleSkill
          ? "单个技能最多 200 个文件，单文件 2 MiB、总计 8 MiB，请整理后重新选择。"
          : "技能包集合最多 2000 个文件，单文件 2 MiB、总计 80 MiB，请整理后重新选择。",
      );
      return;
    }
    if (mode === "folder" && !singleSkill) {
      const topLevel = new Set(
        files.map((item) => item.path.split("/")[0]).filter(Boolean),
      );
      const bundled = [...topLevel].every((folder) =>
        files.some((item) => item.path === `${folder}/SKILL.md`),
      );
      if (!topLevel.size || !bundled) {
        setError(
          "请选择根部包含 SKILL.md 的技能文件夹，或包含多个技能文件夹的上级文件夹。",
        );
        return;
      }
    }
    if (mode === "package" && bytes > 10 * 1048576) {
      setError("技能包最多 10 MiB，解包后总计最多 80 MiB。");
      return;
    }
    setPending(true);
    try {
      const body = new FormData();
      body.append("trusted", String(trusted));
      body.append("overwrite", String(overwrite));
      for (const item of files)
        body.append(mode === "folder" ? "files" : "file", item.file, item.path);
      const result = await request<UploadResult>("/skills", "POST", body);
      onUploaded("skills" in result ? result.skills : [result]);
    } catch (error) {
      setError(errorText(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <SettingsDialog title="上传 Skill" pending={pending} onClose={onClose}>
      <form className="skill-upload-form" onSubmit={upload}>
        <fieldset disabled={pending}>
          <label>
            上传方式
            <select
              aria-label="上传方式"
              value={mode}
              onChange={(event) => {
                setMode(event.target.value as typeof mode);
                setFiles([]);
                setError("");
              }}
            >
              <option value="package">ZIP 技能包 / SKILL.md</option>
              <option value="folder">本机文件夹</option>
            </select>
          </label>
          <div className="skill-upload-picker">
            <label>
              {mode === "folder" ? "选择技能文件夹" : "选择技能包"}
              <input
                key={mode}
                type="file"
                required
                {...(mode === "folder"
                  ? folderAttributes
                  : { accept: ".zip,.md" })}
                multiple={mode === "folder"}
                onChange={(event) => choose(event.target.files)}
              />
            </label>
          </div>
          {!!files.length && (
            <div className="skill-upload-selection">
              <strong>
                {mode === "folder"
                  ? `${files.length} 个文件`
                  : files[0].file.name}{" "}
                · {size(bytes)}
              </strong>
              {mode === "folder" && (
                <ul>
                  {files.map((item) => (
                    <li key={item.path}>
                      {item.path}
                      <span>{size(item.file.size)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
          <p className="muted">
            根部须有 SKILL.md；ZIP
            也可包含单一顶层文件夹或多个技能文件夹。上传后由系统管理目录，可保留
            references、scripts 等子目录。
          </p>
          <details className="skill-upload-guide">
            <summary>
              <Icon name="caret-right" size={14} className="disclosure-icon" />
              格式示例与上传限制
            </summary>
            <pre>
              {
                "---\nname: order-guide\ndescription: 订单处理指引。\n---\n先核对订单号，再使用已授权的工具。"
              }
            </pre>
            <p>
              单个技能最多 200 个文件、400 个目录项，单文件 2 MiB、总计 8 MiB；
              多个技能打包时总计最多 80 MiB。SKILL.md 最多 64,000
              字节。不接受符号链接或特殊文件。
            </p>
          </details>
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={overwrite}
              onChange={(event) => setOverwrite(event.target.checked)}
            />
            覆盖同名技能（保留原有 Agent 挂载关系）
          </label>
          <label className="checkbox-label">
            <input
              type="checkbox"
              required
              checked={trusted}
              onChange={(event) => setTrusted(event.target.checked)}
            />
            我已确认该技能来自可信来源
          </label>
          <p className="muted">
            上传不执行脚本，也不安装依赖。到 Agent
            配置中挂载后，技能指令和按需读取的资源才能提供给该 Agent。
          </p>
        </fieldset>
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <footer>
          <button
            type="button"
            data-dialog-cancel
            disabled={pending}
            onClick={onClose}
          >
            取消
          </button>
          <button
            className="primary"
            disabled={pending || !files.length || !trusted}
          >
            {pending ? "正在上传…" : "上传并保存"}
          </button>
        </footer>
      </form>
    </SettingsDialog>
  );
}

type FileNode = {
  name: string;
  path: string;
  file?: Skill["files"][number];
  children: Map<string, FileNode>;
};
function fileTree(files: Skill["files"]) {
  const root: FileNode = { name: "", path: "", children: new Map() };
  for (const file of files) {
    let node = root;
    for (const name of file.path.split("/")) {
      if (!node.children.has(name))
        node.children.set(name, {
          name,
          path: node.path ? `${node.path}/${name}` : name,
          children: new Map(),
        });
      node = node.children.get(name)!;
    }
    node.file = file;
  }
  return root;
}
function SkillTree({
  node,
  selected,
  onSelect,
}: {
  node: FileNode;
  selected: string;
  onSelect: (path: string) => void;
}) {
  return (
    <ul>
      {Array.from(node.children.values())
        .sort(
          (a, b) =>
            Number(b.path === "SKILL.md") - Number(a.path === "SKILL.md") ||
            Number(!!a.file) - Number(!!b.file) ||
            a.name.localeCompare(b.name),
        )
        .map((child) => (
          <li key={child.path}>
            {child.file ? (
              <button
                title={child.path}
                aria-pressed={selected === child.path}
                onClick={() => onSelect(child.path)}
              >
                <Icon name="file" size={16} />
                {child.name}
              </button>
            ) : (
              <details open>
                <summary>
                  <Icon
                    name="caret-right"
                    size={14}
                    className="disclosure-icon"
                  />
                  <Icon name="folder" size={16} /> {child.name}
                </summary>
                <SkillTree
                  node={child}
                  selected={selected}
                  onSelect={onSelect}
                />
              </details>
            )}
          </li>
        ))}
    </ul>
  );
}
function SkillDetail({
  request,
  skill,
  onArchive,
  onExport,
  onUpdated,
}: {
  request: Api;
  skill: Skill;
  onArchive: () => void;
  onExport: () => void;
  onUpdated: (skill: Skill) => void;
}) {
  const [path, setPath] = useState("SKILL.md");
  const [content, setContent] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [mode, setMode] = useState<"preview" | "edit">("preview");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saveError, setSaveError] = useState("");
  const resource = skill.files.find((file) => file.path === path);
  const tree = useMemo(() => fileTree(skill.files), [skill.files]);
  const markdown = /\.md$/i.test(path);
  useEffect(() => {
    let active = true;
    setContent(null);
    setDraft("");
    setMode("preview");
    setError("");
    setSaveError("");
    if (resource && resource.size <= 128000)
      void request<{ content: string }>(
        `/skills/${skill.id}/files/${path.split("/").map(encodeURIComponent).join("/")}`,
      )
        .then((result) => {
          if (active) {
            setContent(result.content);
            setDraft(result.content);
          }
        })
        .catch((error) => {
          if (active) setError(errorText(error));
        });
    return () => {
      active = false;
    };
  }, [skill.id, path, resource?.size]);
  async function save() {
    setSaving(true);
    setSaveError("");
    try {
      const updated = await request<Skill>(
        `/skills/${skill.id}/files/${path.split("/").map(encodeURIComponent).join("/")}`,
        "PUT",
        { content: draft },
      );
      onUpdated(updated);
      setContent(draft);
      setMode("preview");
    } catch (error) {
      setSaveError(errorText(error));
    } finally {
      setSaving(false);
    }
  }
  const preview = path === "SKILL.md"
    ? (content ?? "").replace(/^---\n[\s\S]*?\n---\n/, "")
    : content;
  return (
    <section className="skill-detail" aria-label="技能详情">
      <header className="skill-detail-head">
        <div className="skill-title">
          <h2 title={skill.name}>{skill.name}</h2>
          <p className="skill-meta">
            <span>{skill.files.length} 个文件</span>
            <span>{size(skill.files.reduce((sum, file) => sum + file.size, 0))}</span>
            <span>{skill.compatibility ? "已声明运行要求" : "未声明外部依赖"}</span>
          </p>
        </div>
        <div className="skill-head-actions">
          <button onClick={onExport}>导出 ZIP</button>
          <button className="danger-ghost" onClick={onArchive}>
            删除技能
          </button>
        </div>
      </header>
      <p className="skill-description">{skill.description}</p>
      <LinkedAgents request={request} kind="skills" id={skill.id} />
      <details className="skill-compatibility">
        <summary>
          <Icon name="caret-right" size={14} className="disclosure-icon" />
          运行要求与依赖
        </summary>
        <p>
          {skill.compatibility ||
            "未声明运行环境依赖，可在文件列表中检查脚本和依赖清单。"}
        </p>
        <p>
          脚本作为资源保存，不自动执行；需要调用外部工具时须单独配置 MCP
          并授权。
        </p>
      </details>
      <div className="skill-resource-browser">
        <nav className="skill-file-tree" aria-label="技能文件">
          <header className="skill-pane-head">
            <span>文件</span>
            <span>{skill.files.length}</span>
          </header>
          <div className="skill-file-tree-scroll">
            <SkillTree node={tree} selected={path} onSelect={setPath} />
          </div>
        </nav>
        <section className="skill-file-preview">
          <header className="skill-pane-head">
            <div className="skill-file-path">
              <Icon name="file" size={16} />
              <strong title={path}>{path}</strong>
              {resource && <span>{size(resource.size)}</span>}
            </div>
            <div
              className="skill-file-actions"
              role="group"
              aria-label="文件视图"
            >
              <button
                type="button"
                aria-pressed={mode === "preview"}
                onClick={() => setMode("preview")}
              >
                预览
              </button>
              <button
                type="button"
                aria-pressed={mode === "edit"}
                disabled={content === null || !resource || resource.size > 128000}
                onClick={() => setMode("edit")}
              >
                编辑
              </button>
            </div>
          </header>
          <div className="skill-file-body">
            {error ? (
              <p className="error skill-file-message" role="alert">
                {error}
              </p>
            ) : resource && resource.size > 128000 ? (
              <p className="muted skill-file-message">
                文件已保存，超过 128 KB 文本预览上限。
              </p>
            ) : content === null ? (
              <p className="muted skill-file-message" role="status">
                正在读取文件…
              </p>
            ) : mode === "edit" ? (
              <div className="skill-file-editor">
                <textarea
                  aria-label={`编辑 ${path}`}
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  spellCheck={false}
                  maxLength={path === "SKILL.md" ? 64000 : 128000}
                  disabled={saving}
                />
                {saveError && (
                  <p className="error skill-file-message" role="alert">
                    {saveError}
                  </p>
                )}
                <div className="skill-editor-actions">
                  <span className="muted">
                    {draft.length.toLocaleString()} 字符
                  </span>
                  <button
                    type="button"
                    disabled={saving}
                    onClick={() => {
                      setDraft(content);
                      setSaveError("");
                      setMode("preview");
                    }}
                  >
                    取消
                  </button>
                  <button
                    type="button"
                    className="primary"
                    disabled={saving || draft === content}
                    onClick={() => void save()}
                  >
                    {saving ? "正在保存…" : "保存文件"}
                  </button>
                </div>
              </div>
            ) : markdown ? (
              <div className="skill-markdown-preview">
                <MessageMarkdown content={preview || ""} />
              </div>
            ) : (
              <pre>{content}</pre>
            )}
          </div>
        </section>
      </div>
    </section>
  );
}

type SkillSort = "name" | "recent" | "files" | "size";

function SkillCatalog({ request }: { request: Api }) {
  const [skills, setSkills] = useState<Skill[]>([]);
  const [selected, setSelected] = useState("");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SkillSort>(() => {
    try {
      const stored = localStorage.getItem("siagent.skill-sort");
      if (
        stored === "name" ||
        stored === "files" ||
        stored === "size" ||
        stored === "recent"
      )
        return stored;
    } catch {
      // Fall through to the default order.
    }
    return "name";
  });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState("");
  const [upload, setUpload] = useState(false);
  const [archiving, setArchiving] = useState<Skill | null>(null);
  const [pending, setPending] = useState(false);
  const [archiveError, setArchiveError] = useState("");
  const [reload, setReload] = useState(0);
  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    void request<Skill[]>("/skills")
      .then((values) => {
        if (active) {
          setSkills(values);
          setSelected((id) =>
            values.some((skill) => skill.id === id)
              ? id
              : (values[0]?.id ?? ""),
          );
        }
      })
      .catch((error) => {
        if (active) setError(errorText(error));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [reload]);
  useEffect(() => {
    try {
      localStorage.setItem("siagent.skill-sort", sort);
    } catch {
      // The order still applies for this session.
    }
  }, [sort]);
  const sizeOf = (skill: Skill) =>
    skill.files.reduce((sum, file) => sum + file.size, 0);
  // The API returns skills newest first. Sorting is a view concern only, so the
  // selected skill stays selected when the order changes.
  const visible = skills
    .filter((skill) =>
      `${skill.name} ${skill.description}`
        .toLowerCase()
        .includes(query.toLowerCase()),
    )
    .sort((a, b) => {
      if (sort === "files") return b.files.length - a.files.length;
      if (sort === "size") return sizeOf(b) - sizeOf(a);
      // "recent" keeps the server order (newest first); "name" is A-Z.
      return sort === "recent" ? 0 : a.name.localeCompare(b.name, "zh-CN");
    });
  const current = visible.find((skill) => skill.id === selected) ?? visible[0];
  async function exportArchive(
    path: string,
    fallback: string,
    message: string,
  ) {
    setExporting(true);
    setExportError("");
    setFeedback("");
    try {
      await downloadArchive(path, fallback);
      setFeedback(message);
    } catch (error) {
      setExportError(errorText(error));
    } finally {
      setExporting(false);
    }
  }
  return (
    <div className="skill-catalog">
      <header className="plugin-section-heading">
        <div>
          <h2>
            技能库 <span>{skills.length}</span>
          </h2>
          <p>
            上传并管理技能文件，再到 <a href="#/settings/agents">Agent 配置</a>
            中挂载。
          </p>
        </div>
        <div className="plugin-heading-actions">
          <button
            type="button"
            disabled={loading || !skills.length || exporting}
            onClick={() =>
              void exportArchive(
                "/skills/export",
                "siagent-skills.zip",
                "已导出全部技能。",
              )
            }
          >
            <Icon name="arrow-down" size={16} />
            {exporting ? "正在导出…" : "导出全部"}
          </button>
          <button
            className="primary"
            disabled={loading || exporting}
            onClick={() => setUpload(true)}
          >
            <Icon name="plus" size={16} /> 上传 Skill
          </button>
        </div>
      </header>
      {feedback && (
        <p className="success plugin-feedback" role="status">
          {feedback}
        </p>
      )}
      {exportError && (
        <p className="error plugin-feedback" role="alert">
          {exportError}
        </p>
      )}
      {error && (
        <p className="error plugin-feedback" role="alert">
          {error}
          <button onClick={() => setReload((value) => value + 1)}>
            重新读取
          </button>
        </p>
      )}
      {loading ? (
        <p className="muted" role="status">
          正在读取技能库…
        </p>
      ) : skills.length ? (
        <div className="skill-catalog-layout">
          <aside className="skill-catalog-list">
            <div className="pane-heading">
              <h2>
                技能列表 <span>{skills.length}</span>
              </h2>
            </div>
            <div className="skill-list-filter">
              <input
                type="search"
                aria-label="搜索技能"
                placeholder="搜索名称或说明"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
              <label className="skill-list-sort">
                <span className="sr-only">技能排序方式</span>
                <select
                  aria-label="技能排序方式"
                  value={sort}
                  onChange={(event) =>
                    setSort(event.target.value as SkillSort)
                  }
                >
                  <option value="name">名称 A→Z</option>
                  <option value="recent">最近上传</option>
                  <option value="files">文件数最多</option>
                  <option value="size">体积最大</option>
                </select>
              </label>
            </div>
            <nav className="skill-list" aria-label="技能列表">
              {visible.map((skill) => (
                <button
                  key={skill.id}
                  className="skill-list-item"
                  aria-current={current?.id === skill.id ? "true" : undefined}
                  onClick={() => setSelected(skill.id)}
                >
                  <span className="skill-list-text">
                    <strong title={skill.name}>{skill.name}</strong>
                    <span title={skill.description}>{skill.description}</span>
                  </span>
                  <span className="skill-list-meta">
                    {skill.files.length}
                    <span className="skill-list-meta-label">文件</span>
                  </span>
                </button>
              ))}
            </nav>
            {!visible.length && <p className="muted">没有匹配的技能。</p>}
          </aside>
          {current ? (
            <SkillDetail
              key={current.id}
              request={request}
              skill={current}
              onArchive={() => {
                setArchiving(current);
                setArchiveError("");
              }}
              onExport={() =>
                void exportArchive(
                  `/skills/${current.id}/export`,
                  `${current.name}.zip`,
                  `“${current.name}”已导出。`,
                )
              }
              onUpdated={(updated) =>
                setSkills((values) =>
                  values.map((value) =>
                    value.id === updated.id ? updated : value,
                  ),
                )
              }
            />
          ) : (
            <p className="muted">请调整搜索条件。</p>
          )}
        </div>
      ) : (
        !error && (
          <div className="plugin-empty">
            <Icon name="plugins" size={28} />
            <h3>上传第一个 Skill</h3>
            <p>
              选择 ZIP 技能包、SKILL.md
              或本机文件夹。系统会创建独立的技能目录，保留包内文件结构。
            </p>
            <p>上传后到 Agent 配置中选择需要挂载的技能。</p>
          </div>
        )
      )}
      {upload && (
        <SkillUpload
          request={request}
          onClose={() => setUpload(false)}
          onUploaded={(uploaded) => {
            const existing = new Set(skills.map((skill) => skill.name));
            const replaced = uploaded.filter((skill) =>
              existing.has(skill.name),
            ).length;
            setSkills((values) => {
              const next = [...values];
              for (const skill of uploaded) {
                const index = next.findIndex((item) => item.id === skill.id);
                if (index >= 0) next[index] = skill;
                else next.unshift(skill);
              }
              return next;
            });
            setSelected(uploaded[uploaded.length - 1]?.id ?? "");
            setQuery("");
            setUpload(false);
            if (uploaded.length === 1 && replaced === 0) {
              setFeedback(
                `“${uploaded[0].name}”已上传，可到 Agent 配置中挂载。`,
              );
            } else if (uploaded.length === 1) {
              setFeedback(
                `“${uploaded[0].name}”已覆盖同名技能，原有 Agent 挂载关系保留。`,
              );
            } else {
              setFeedback(
                `已导入 ${uploaded.length} 个技能${replaced ? `，覆盖 ${replaced} 个同名技能` : ""}，可到 Agent 配置中挂载。`,
              );
            }
          }}
        />
      )}
      {archiving && (
        <SettingsDialog
          title={`删除技能“${archiving.name}”？`}
          pending={pending}
          onClose={() => setArchiving(null)}
        >
          <p>
            将永久删除技能文件，并自动从所有关联 Agent
            中移除。已有任务使用的历史快照与运行记录保留；此操作不可撤销。
          </p>
          <LinkedAgents request={request} kind="skills" id={archiving.id} />
          {archiveError && (
            <p role="alert" className="error">
              {archiveError}
            </p>
          )}
          <footer>
            <button
              data-dialog-cancel
              disabled={pending}
              onClick={() => setArchiving(null)}
            >
              取消
            </button>
            <button
              className="danger-button"
              disabled={pending}
              onClick={async () => {
                setPending(true);
                setArchiveError("");
                try {
                  await request(`/skills/${archiving.id}`, "DELETE");
                  setSkills((values) =>
                    values.filter((skill) => skill.id !== archiving.id),
                  );
                  setArchiving(null);
                  setFeedback("技能已删除，关联 Agent 已自动解除；历史任务保留。");
                } catch (error) {
                  setArchiveError(errorText(error));
                } finally {
                  setPending(false);
                }
              }}
            >
              {pending ? "正在删除…" : "确认删除"}
            </button>
          </footer>
        </SettingsDialog>
      )}
    </div>
  );
}
