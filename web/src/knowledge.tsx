import { Icon } from "./icons";
import { appURL } from "./urls";
import { FormEvent, useEffect, useRef, useState } from "react";
import { KnowledgeDialog } from "./knowledge-dialog";

type Api = <T>(path: string, method?: string, body?: unknown) => Promise<T>;
type Base = {
  id: string;
  name: string;
  description: string;
  documents: number;
};
type Document = {
  id: string;
  title: string;
  filename: string;
  active_version: number;
  version: number;
  state: string;
  error: string;
  size: number;
  characters: number;
  chunks: number;
  deleted: boolean;
  cleanup_pending: boolean;
};
export type KnowledgeHit = {
  source_id: string;
  knowledge_id: string;
  document_id: string;
  version: number;
  title: string;
  text: string;
  source_url: string;
  location?: {
    page?: number;
    paragraph?: number;
    start?: number;
    end?: number;
  };
};
export type TraceHit = {
  source_id: string;
  document_id: string;
  title: string;
  position: number;
  score: number;
  rank?: number;
  matched_terms?: string[];
};
export type RetrievalTrace = {
  query: string;
  query_type: string;
  terms: string[];
  match_mode: string;
  knowledge_ids: string[];
  stages: { name: string; detail?: string; hits: number; duration_ns: number }[];
  candidates: TraceHit[];
  selected: TraceHit[];
  total_candidates: number;
  duration_ns: number;
  rerank?: {
    mode: string;
    before: string[];
    after: string[];
    fallback?: string;
  };
};
type ContentGap = {
  id: number;
  knowledge_id: string;
  query: string;
  count: number;
  status: string;
  created: number;
  last_seen: number;
};
type EvalQuestion = {
  id: string;
  knowledge_id: string;
  question: string;
  expected_document_ids: string[];
  tags: string[];
  answerable: boolean;
  expected_answer?: string;
  created: number;
};
type BaseConfig = { expand: boolean; llm_expand: boolean; gate: boolean };
type VocabEntry = { term: string; expansion: string };
type EvalMetrics = {
  questions: number;
  recall_at_1: number;
  recall_at_3: number;
  recall_at_5: number;
  mrr: number;
  no_result_rate: number;
  abstention_accuracy: number;
  citation_precision?: number;
  faithfulness?: number;
  avg_latency_ms: number;
  baseline_recall_at_5?: number;
  baseline_mrr?: number;
};
type EvalCase = {
  question_id: string;
  question: string;
  expected_document_ids: string[];
  ranked_document_ids: string[];
  first_rank: number;
  pass: boolean;
};
type EvalRun = {
  id: string;
  knowledge_id: string;
  created: number;
  metrics: EvalMetrics;
  cases: EvalCase[];
};
type EvalRunSummary = {
  id: string;
  knowledge_id: string;
  created: number;
  metrics: EvalMetrics;
};
type Source = KnowledgeHit & {
  deleted: boolean;
  historical: boolean;
  document_text: string;
  offset: number;
  next_offset?: number;
  total_characters: number;
};
const message = (error: unknown) =>
  error instanceof Error ? error.message : "操作未完成，请稍后重试。";
const states: Record<string, string> = {
  queued: "等待处理",
  processing: "正在处理",
  ready: "可检索",
  failed: "处理失败",
  deleted: "已删除",
};
const size = (bytes: number) =>
  bytes >= 1048576
    ? `${(bytes / 1048576).toFixed(1)} MiB`
    : `${(bytes / 1024).toFixed(1)} KiB`;

function DeleteDialog({
  title,
  description,
  onClose,
  onDelete,
}: {
  title: string;
  description: string;
  onClose: () => void;
  onDelete: () => Promise<void>;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  return (
    <KnowledgeDialog title={title} pending={pending} onClose={onClose}>
      <p>{description}</p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <footer>
        <button data-dialog-cancel disabled={pending} onClick={onClose}>
          取消
        </button>
        <button
          className="danger-button"
          disabled={pending}
          onClick={async () => {
            setPending(true);
            setError("");
            try {
              await onDelete();
              onClose();
            } catch (error) {
              setError(message(error));
            } finally {
              setPending(false);
            }
          }}
        >
          {pending ? "正在删除…" : "确认删除"}
        </button>
      </footer>
    </KnowledgeDialog>
  );
}

function LibraryEditor({
  request,
  base,
  onClose,
  onSaved,
}: {
  request: Api;
  base: Base | null;
  onClose: () => void;
  onSaved: (base: Base) => void;
}) {
  const [name, setName] = useState(base?.name ?? "");
  const [description, setDescription] = useState(base?.description ?? "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  async function save(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      const result = await request<Base>(
        base ? `/knowledge/bases/${base.id}` : "/knowledge/bases",
        base ? "PUT" : "POST",
        { name, description },
      );
      onSaved({ ...result, documents: base?.documents ?? 0 });
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <KnowledgeDialog
      title={base ? "编辑知识库" : "新建知识库"}
      pending={pending}
      onClose={onClose}
    >
      <form onSubmit={save}>
        <fieldset disabled={pending}>
          <label>
            知识库名称
            <input
              required
              maxLength={80}
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="例如：产品手册、财务制度"
            />
          </label>
          <label>
            知识库说明
            <textarea
              rows={3}
              maxLength={1000}
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="简要说明资料内容与用途（选填）"
            />
          </label>
        </fieldset>
        {error && (
          <p role="alert" className="error">
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
          <button className="primary" disabled={pending}>
            {pending ? "正在保存…" : base ? "保存知识库" : "创建知识库"}
          </button>
        </footer>
      </form>
    </KnowledgeDialog>
  );
}

function UploadDialog({
  request,
  baseID,
  replacement,
  pdf,
  onClose,
  onUploaded,
}: {
  request: Api;
  baseID: string;
  replacement: Document | null;
  pdf: boolean | null;
  onClose: () => void;
  onUploaded: (document: Document) => void;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  async function upload(event: FormEvent) {
    event.preventDefault();
    if (!file) return;
    if (file.size > 20 * 1024 * 1024) {
      setError("单个文件不能超过 20 MiB，请拆分后上传。");
      return;
    }
    setPending(true);
    setError("");
    try {
      const data = new FormData();
      data.append("file", file);
      const result = await request<Document>(
        `/knowledge/bases/${baseID}/documents${replacement ? `/${replacement.id}` : ""}`,
        replacement ? "PUT" : "POST",
        data,
      );
      onUploaded(result);
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }
  return (
    <KnowledgeDialog
      title={replacement ? "替换文档" : "上传文档"}
      pending={pending}
      onClose={onClose}
    >
      <form onSubmit={upload}>
        {replacement && <p className="kb-target-name">{replacement.title}</p>}
        <div className="kb-file-picker">
          <label>
            选择文档
            <input
              required
              type="file"
              accept=".txt,.md,.markdown,.pdf,.docx"
              disabled={pending}
              onChange={(event) => {
                setFile(event.target.files?.[0] ?? null);
                setError("");
              }}
            />
          </label>
          {file && (
            <p>
              {file.name} · {size(file.size)}
            </p>
          )}
        </div>
        <p className="kb-help">
          UTF-8 TXT、Markdown、DOCX、文本 PDF，单文件最多 20 MiB。不支持扫描件、旧版
          .doc 与 OCR。
        </p>
        {replacement && (
          <p className="kb-help">
            新版本处理成功后切换；失败时仍使用已发布版本。
          </p>
        )}
        {pdf === false && (
          <p className="error">
            PDF 解析依赖未就绪；TXT、Markdown 与 DOCX 可正常上传。
          </p>
        )}
        <details className="kb-limits">
          <summary>
            <Icon name="caret-right" size={14} className="disclosure-icon" />
            查看处理限制
          </summary>
          <p>
            提取最多 100 万字符、5000 个片段；PDF 最多 1000
            页。文档在本地提取与建立索引，不会在上传时发送给模型。
          </p>
        </details>
        {error && (
          <p role="alert" className="error">
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
          <button className="primary" disabled={pending || !file}>
            {pending
              ? "正在上传…"
              : replacement
                ? "上传替换版本"
                : "上传并建立索引"}
          </button>
        </footer>
      </form>
    </KnowledgeDialog>
  );
}

export function KnowledgeWorkspace({ request }: { request: Api }) {
  const [bases, setBases] = useState<Base[]>([]);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [pdf, setPDF] = useState<boolean | null>(null);
  const [editor, setEditor] = useState<{ base: Base | null } | null>(null);
  const [deleting, setDeleting] = useState<Base | null>(null);
  const current = bases.find((base) => base.id === selected);
  const mounted = useRef(true);
  async function refresh() {
    const values = await request<Base[]>("/knowledge/bases");
    if (!mounted.current) return;
    setBases(values);
    setSelected((id) =>
      values.some((base) => base.id === id) ? id : (values[0]?.id ?? ""),
    );
  }
  useEffect(() => {
    mounted.current = true;
    void Promise.all([
      request<Base[]>("/knowledge/bases"),
      request<{ pdf_available: boolean }>("/knowledge/status"),
    ])
      .then(([values, status]) => {
        if (!mounted.current) return;
        setBases(values);
        setSelected(values[0]?.id ?? "");
        setPDF(status.pdf_available);
      })
      .catch((error) => {
        if (mounted.current) setError(message(error));
      })
      .finally(() => {
        if (mounted.current) setLoading(false);
      });
    return () => {
      mounted.current = false;
    };
  }, []);
  return (
    <div className="knowledge-workspace">
      {loading ? (
        <p className="kb-loading" role="status">
          正在读取知识库…
        </p>
      ) : (
        <>
          <div className="kb-library-toolbar">
            <label>
              <span className="sr-only">选择知识库</span>
              <select
                aria-label="选择知识库"
                value={selected}
                disabled={!bases.length}
                onChange={(event) => {
                  setSelected(event.target.value);
                  setFeedback("");
                  setError("");
                }}
              >
                {!bases.length && <option value="">尚未创建知识库</option>}
                {bases.map((base) => (
                  <option key={base.id} value={base.id}>
                    {base.name} · {base.documents} 份文档
                  </option>
                ))}
              </select>
            </label>
            <div className="kb-library-controls">
              {current && (
                <details
                  className="kb-actions kb-library-actions"
                  onBlur={(event) => {
                    if (!event.currentTarget.contains(event.relatedTarget))
                      event.currentTarget.open = false;
                  }}
                >
                  <summary>
                    <Icon
                      name="caret-right"
                      size={14}
                      className="disclosure-icon"
                    />
                    管理知识库
                  </summary>
                  <div>
                    <button
                      onClick={(event) => {
                        event.currentTarget.closest("details")!.open = false;
                        setEditor({ base: current });
                      }}
                    >
                      编辑知识库
                    </button>
                    <button
                      className="kb-danger"
                      onClick={(event) => {
                        event.currentTarget.closest("details")!.open = false;
                        setDeleting(current);
                      }}
                    >
                      删除知识库
                    </button>
                  </div>
                </details>
              )}
              <button
                className="primary"
                onClick={() => setEditor({ base: null })}
                disabled={!!error && !bases.length}
              >
                <Icon name="plus" size={16} /> 新建知识库
              </button>
            </div>
          </div>
          {error && (
            <div className="kb-feedback error" role="alert">
              {error}
              <button
                onClick={() => {
                  setError("");
                  setLoading(true);
                  void refresh()
                    .catch((error) => setError(message(error)))
                    .finally(() => setLoading(false));
                }}
              >
                重新读取知识库
              </button>
            </div>
          )}
          {feedback && (
            <p className="kb-feedback success" role="status">
              {feedback}
            </p>
          )}
          {current ? (
            <LibraryDocuments
              key={current.id}
              request={request}
              base={current}
              pdf={pdf}
              onChanged={() => {
                void refresh().catch((error) => {
                  if (mounted.current) setError(message(error));
                });
              }}
            />
          ) : (
            !error && (
              <div className="kb-empty">
                <Icon name="knowledge" size={28} className="kb-empty-icon" />
                <h2>还没有知识库</h2>
                <p>点击上方“新建知识库”，整理一组文档后即可测试检索。</p>
              </div>
            )
          )}
          <p className="kb-scope-note">
            文档在本地处理。Agent 的访问范围在{" "}
            <a href="#/settings/agents">Agent 配置</a>中单独授权。
          </p>
        </>
      )}
      {editor && (
        <LibraryEditor
          request={request}
          base={editor.base}
          onClose={() => setEditor(null)}
          onSaved={(base) => {
            setBases((values) =>
              values.some((value) => value.id === base.id)
                ? values.map((value) => (value.id === base.id ? base : value))
                : [...values, base],
            );
            setSelected(base.id);
            setEditor(null);
            setFeedback(
              editor.base ? "知识库已更新。" : "知识库已创建，可以上传文档。",
            );
          }}
        />
      )}
      {deleting && (
        <DeleteDialog
          title={`删除知识库“${deleting.name}”？`}
          description="删除后，全部文档退出检索，并解除 Agent 的关联。已有对话来源保留删除标记，此操作无法撤销。"
          onClose={() => setDeleting(null)}
          onDelete={async () => {
            await request(`/knowledge/bases/${deleting.id}`, "DELETE");
            const remaining = bases.filter((base) => base.id !== deleting.id);
            setBases(remaining);
            setSelected((id) =>
              id === deleting.id ? (remaining[0]?.id ?? "") : id,
            );
            setFeedback("知识库已删除，文档文件将清理，历史引用标识保留。");
          }}
        />
      )}
    </div>
  );
}

function LibraryDocuments({
  request,
  base,
  pdf,
  onChanged,
}: {
  request: Api;
  base: Base;
  pdf: boolean | null;
  onChanged: () => void;
}) {
  const [documents, setDocuments] = useState<Document[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const [tab, setTab] = useState<
    "documents" | "search" | "eval" | "gaps" | "conflicts"
  >("documents");
  const [filter, setFilter] = useState("");
  const [status, setStatus] = useState("all");
  const [upload, setUpload] = useState<{ replacement: Document | null } | null>(
    null,
  );
  const [deleting, setDeleting] = useState<Document | null>(null);
  const [detail, setDetail] = useState<string | null>(null);
  const [pending, setPending] = useState("");
  const [pollError, setPollError] = useState("");
  const mounted = useRef(true);
  const reads = useRef(0);
  const busy = documents.some(
    (doc) =>
      doc.state === "queued" ||
      doc.state === "processing" ||
      doc.cleanup_pending,
  );
  const processing = documents.filter((doc) =>
    ["queued", "processing"].includes(doc.state),
  ).length;
  const failed = documents.filter((doc) => doc.state === "failed").length;
  const visible = documents.filter(
    (doc) =>
      `${doc.title} ${doc.filename}`
        .toLowerCase()
        .includes(filter.toLowerCase()) &&
      (status === "all" ||
        (!doc.deleted &&
          (status === "processing"
            ? ["queued", "processing"].includes(doc.state)
            : doc.state === status))),
  );
  const selectedDoc = documents.find((doc) => doc.id === detail);
  async function refresh() {
    const sequence = ++reads.current;
    try {
      const values = await request<Document[]>(
        `/knowledge/bases/${base.id}/documents`,
      );
      if (mounted.current && sequence === reads.current) setDocuments(values);
    } catch (error) {
      if (mounted.current && sequence === reads.current) throw error;
    }
  }
  useEffect(() => {
    mounted.current = true;
    void refresh()
      .catch((error) => {
        if (mounted.current) setError(message(error));
      })
      .finally(() => {
        if (mounted.current) setLoading(false);
      });
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    if (!busy) return;
    let active = true;
    // Poll serially so a slow read cannot overwrite a newer status response.
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        await refresh();
        if (active) setPollError("");
      } catch (error) {
        if (active) setPollError(message(error));
      } finally {
        if (active) timer = setTimeout(poll, 1000);
      }
    };
    timer = setTimeout(poll, 1000);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [busy]);
  async function retry(doc: Document) {
    setPending(doc.id);
    setError("");
    setFeedback("");
    try {
      const value = await request<Document>(
        `/knowledge/documents/${doc.id}/retry`,
        "POST",
      );
      if (mounted.current) {
        reads.current++;
        setDocuments((values) =>
          values.map((item) => (item.id === value.id ? value : item)),
        );
        setFeedback(
          "已安排重新处理，成功后切换到新版本；失败时继续使用原版本。",
        );
      }
    } catch (error) {
      if (mounted.current) setError(message(error));
    } finally {
      if (mounted.current) setPending("");
    }
  }
  function changeTab(value: typeof tab) {
    setTab(value);
  }
  return (
    <section className="kb-library-content">
      {base.description && (
        <p className="kb-description" title={base.description}>
          {base.description}
        </p>
      )}
      <div className="kb-content-bar">
        <div
          className="kb-tabs"
          role="tablist"
          aria-label="知识库内容"
          onKeyDown={(event) => {
            if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key))
              return;
            event.preventDefault();
            const order = ["documents", "search", "eval", "gaps", "conflicts"] as const;
            const index = order.indexOf(tab);
            const value =
              event.key === "Home"
                ? "documents"
                : event.key === "End"
                  ? "conflicts"
                  : order[
                      event.key === "ArrowLeft"
                        ? (index + order.length - 1) % order.length
                        : (index + 1) % order.length
                    ];
            changeTab(value);
            event.currentTarget
              .querySelector<HTMLButtonElement>(`#kb-tab-${value}`)
              ?.focus();
          }}
        >
          <button
            role="tab"
            id="kb-tab-documents"
            aria-selected={tab === "documents"}
            aria-controls="kb-documents"
            tabIndex={tab === "documents" ? 0 : -1}
            onClick={() => changeTab("documents")}
          >
            文档管理 <span>{documents.length}</span>
          </button>
          <button
            role="tab"
            id="kb-tab-search"
            aria-selected={tab === "search"}
            aria-controls="kb-search"
            tabIndex={tab === "search" ? 0 : -1}
            onClick={() => changeTab("search")}
          >
            检索测试
          </button>
          <button
            role="tab"
            id="kb-tab-eval"
            aria-selected={tab === "eval"}
            aria-controls="kb-eval"
            tabIndex={tab === "eval" ? 0 : -1}
            onClick={() => changeTab("eval")}
          >
            评测
          </button>
          <button
            role="tab"
            id="kb-tab-gaps"
            aria-selected={tab === "gaps"}
            aria-controls="kb-gaps"
            tabIndex={tab === "gaps" ? 0 : -1}
            onClick={() => changeTab("gaps")}
          >
            内容缺口
          </button>
          <button
            role="tab"
            id="kb-tab-conflicts"
            aria-selected={tab === "conflicts"}
            aria-controls="kb-conflicts"
            tabIndex={tab === "conflicts" ? 0 : -1}
            onClick={() => changeTab("conflicts")}
          >
            冲突与过期
          </button>
        </div>
        <span className="kb-status-summary">
          {processing
            ? `${processing} 份处理中`
            : failed
              ? `${failed} 份处理失败`
              : "本地索引"}
        </span>
      </div>
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
      {pollError && (
        <p className="kb-feedback error" role="alert">
          {pollError}
        </p>
      )}
      <div
        role="tabpanel"
        id="kb-documents"
        aria-labelledby="kb-tab-documents"
        hidden={tab !== "documents"}
      >
        <div className="kb-document-toolbar">
          <input
            aria-label="搜索文档"
            type="search"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            placeholder="搜索文档名称"
          />
          <select
            aria-label="筛选文档状态"
            value={status}
            onChange={(event) => setStatus(event.target.value)}
          >
            <option value="all">全部状态</option>
            <option value="ready">可检索</option>
            <option value="processing">处理中</option>
            <option value="failed">处理失败</option>
          </select>
          <div className="kb-document-toolbar-actions">
            <button
              disabled={!!pending}
              onClick={() => {
                setPending("refresh");
                setError("");
                void refresh()
                  .catch((error) => {
                    if (mounted.current) setError(message(error));
                  })
                  .finally(() => {
                    if (mounted.current) setPending("");
                  });
              }}
            >
              {pending === "refresh" ? "正在刷新…" : "刷新文档状态"}
            </button>
            <button
              className="primary"
              disabled={loading}
              onClick={() => setUpload({ replacement: null })}
            >
              上传文档
            </button>
          </div>
        </div>
        {loading ? (
          <p className="kb-loading" role="status">
            正在读取文档…
          </p>
        ) : !documents.length ? (
          <div className="kb-empty kb-documents-empty">
            <Icon name="upload" size={28} className="kb-empty-icon" />
            <h3>还没有文档</h3>
            <p>点击“上传文档”添加 TXT、Markdown、DOCX 或文本 PDF。</p>
          </div>
        ) : (
          <div
            className="kb-document-table"
            role="table"
            aria-label="知识库文档"
          >
            <div className="kb-document-row kb-table-heading" role="row">
              <span role="columnheader">文档</span>
              <span role="columnheader">索引状态</span>
              <span role="columnheader">检索版本</span>
              <span role="columnheader" className="sr-only">
                操作
              </span>
            </div>
            {visible.map((doc) => (
              <div className="kb-document-row" role="row" key={doc.id}>
                <div role="cell" className="kb-document-name">
                  <button onClick={() => setDetail(doc.id)} title={doc.title}>
                    {doc.title}
                  </button>
                  <small>
                    {size(doc.size)}
                    {doc.state === "ready" ? ` · ${doc.chunks} 个片段` : ""}
                  </small>
                  {doc.error && (
                    <p className="kb-row-error" title={doc.error}>
                      {doc.error}
                      {doc.active_version > 0 && !doc.deleted
                        ? ` 仍使用已发布的 v${doc.active_version}。`
                        : ""}
                    </p>
                  )}
                </div>
                <div role="cell">
                  <span
                    className={`knowledge-state ${doc.cleanup_pending ? "failed" : doc.state}`}
                  >
                    {doc.cleanup_pending
                      ? "文件清理待重试"
                      : (states[doc.state] ?? doc.state)}
                  </span>
                </div>
                <div role="cell" className="kb-version">
                  {doc.active_version > 0 && !doc.deleted
                    ? `v${doc.active_version}`
                    : "—"}
                  {doc.version !== doc.active_version && (
                    <small>最新 v{doc.version}</small>
                  )}
                </div>
                <div role="cell">
                  <details
                    className="kb-actions"
                    onBlur={(event) => {
                      if (!event.currentTarget.contains(event.relatedTarget))
                        event.currentTarget.open = false;
                    }}
                  >
                    <summary aria-label={`${doc.title}的更多操作`}>
                      <Icon name="more" />
                    </summary>
                    <div
                      onClick={(event) => {
                        const details = event.currentTarget.closest("details");
                        if (details) details.open = false;
                      }}
                    >
                      <button onClick={() => setDetail(doc.id)}>
                        文档详情
                      </button>
                      {!doc.deleted && (
                        <>
                          <button
                            disabled={
                              !!pending ||
                              ["queued", "processing"].includes(doc.state)
                            }
                            onClick={() => setUpload({ replacement: doc })}
                          >
                            替换文件
                          </button>
                          <button
                            disabled={
                              !!pending ||
                              ["queued", "processing"].includes(doc.state)
                            }
                            onClick={() => void retry(doc)}
                          >
                            {doc.state === "failed" ? "重试处理" : "重新索引"}
                          </button>
                        </>
                      )}
                      <button
                        className="kb-danger"
                        disabled={!!pending}
                        onClick={() => setDeleting(doc)}
                      >
                        {doc.cleanup_pending ? "重试文件清理" : "删除文档"}
                      </button>
                    </div>
                  </details>
                </div>
              </div>
            ))}
            {!visible.length && (
              <p className="kb-filter-empty">
                没有匹配的文档，请调整名称或状态筛选。
              </p>
            )}
          </div>
        )}
      </div>
      <div
        role="tabpanel"
        id="kb-search"
        aria-labelledby="kb-tab-search"
        hidden={tab !== "search"}
      >
        <KnowledgeSettings key={`cfg-${base.id}`} request={request} baseID={base.id} />
        <KnowledgeSearch
          key={base.id}
          request={request}
          baseID={base.id}
          revision={documents
            .map((doc) => `${doc.id}:${doc.active_version}:${doc.deleted}`)
            .join("|")}
        />
      </div>
      <div
        role="tabpanel"
        id="kb-eval"
        aria-labelledby="kb-tab-eval"
        hidden={tab !== "eval"}
      >
        <KnowledgeEval
          key={base.id}
          request={request}
          baseID={base.id}
          documents={documents.filter((doc) => !doc.deleted)}
        />
      </div>
      <div
        role="tabpanel"
        id="kb-gaps"
        aria-labelledby="kb-tab-gaps"
        hidden={tab !== "gaps"}
      >
        <KnowledgeGaps
          key={base.id}
          request={request}
          baseID={base.id}
          active={tab === "gaps"}
        />
      </div>
      <div
        role="tabpanel"
        id="kb-conflicts"
        aria-labelledby="kb-tab-conflicts"
        hidden={tab !== "conflicts"}
      >
        <KnowledgeConflicts
          key={base.id}
          request={request}
          baseID={base.id}
          active={tab === "conflicts"}
        />
      </div>
      {upload && (
        <UploadDialog
          request={request}
          baseID={base.id}
          replacement={upload.replacement}
          pdf={pdf}
          onClose={() => setUpload(null)}
          onUploaded={(doc) => {
            reads.current++;
            setDocuments((values) => [
              doc,
              ...values.filter((value) => value.id !== doc.id),
            ]);
            setUpload(null);
            setFeedback(
              "文档已接收，处理完成后才进入检索。替换失败时继续使用原版本。",
            );
            onChanged();
          }}
        />
      )}
      {deleting && (
        <DeleteDialog
          title={deleting.cleanup_pending ? "重试文件清理？" : "删除文档？"}
          description={`“${deleting.title}”将退出检索，已有来源链接保留删除标记。其他文档不受影响。`}
          onClose={() => setDeleting(null)}
          onDelete={async () => {
            await request(`/knowledge/documents/${deleting.id}`, "DELETE");
            reads.current++;
            setDocuments((values) =>
              values.filter((doc) => doc.id !== deleting.id),
            );
            setFeedback("文档已删除，不再参与新检索。已有来源链接显示已删除。");
            onChanged();
            void refresh().catch((error) => {
              if (mounted.current) setError(message(error));
            });
          }}
        />
      )}
      {selectedDoc && (
        <KnowledgeDialog title="文档详情" onClose={() => setDetail(null)}>
          <p className="kb-target-name">{selectedDoc.title}</p>
          <dl className="kb-document-metadata">
            <dt>原始文件</dt>
            <dd>{selectedDoc.filename}</dd>
            <dt>文件大小</dt>
            <dd>{size(selectedDoc.size)}</dd>
            <dt>最新版本</dt>
            <dd>
              v{selectedDoc.version} ·{" "}
              {states[selectedDoc.state] ?? selectedDoc.state}
            </dd>
            <dt>当前检索版本</dt>
            <dd>
              {selectedDoc.deleted
                ? "已退出检索"
                : selectedDoc.active_version
                  ? `v${selectedDoc.active_version}`
                  : "尚未发布"}
            </dd>
            <dt>字符 / 片段</dt>
            <dd>
              {selectedDoc.characters.toLocaleString()} / {selectedDoc.chunks}
            </dd>
          </dl>
          {selectedDoc.error && (
            <p className="error">
              {selectedDoc.error}
              {selectedDoc.active_version > 0 && !selectedDoc.deleted
                ? ` 仍使用已发布的 v${selectedDoc.active_version}。`
                : ""}
            </p>
          )}
          <footer>
            {selectedDoc.active_version > 0 && !selectedDoc.deleted && (
              <a
                className="kb-source-link"
                href={`?source=${selectedDoc.id}.${selectedDoc.active_version}.0`}
                target="_blank"
                rel="noopener noreferrer"
              >
                查看检索原文 <Icon name="arrow-up-right" size={14} />
              </a>
            )}
            <button data-dialog-cancel onClick={() => setDetail(null)}>
              关闭
            </button>
          </footer>
        </KnowledgeDialog>
      )}
    </section>
  );
}

type Conflict = {
  id: number;
  document_a: string;
  document_b: string;
  detail: string;
  status: string;
  created: number;
};
type Outdated = {
  run_id: string;
  source_id: string;
  document_id: string;
  title: string;
  cited_version: number;
  active_version: number;
};

function KnowledgeConflicts({
  request,
  baseID,
  active,
}: {
  request: Api;
  baseID: string;
  active: boolean;
}) {
  const [conflicts, setConflicts] = useState<Conflict[]>([]);
  const [outdated, setOutdated] = useState<Outdated[]>([]);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");

  async function load() {
    setLoading(true);
    try {
      const [items, old] = await Promise.all([
        request<Conflict[]>(`/knowledge/bases/${baseID}/conflicts?status=open`),
        request<Outdated[]>(`/knowledge/outdated?knowledge_id=${baseID}`),
      ]);
      setConflicts(items);
      setOutdated(old);
    } catch (error) {
      setError(message(error));
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    if (active) void load();
  }, [baseID, active]);

  async function detect() {
    setPending(true);
    setError("");
    try {
      const value = await request<{ found: number }>(
        `/knowledge/bases/${baseID}/conflicts/detect`,
        "POST",
      );
      setFeedback(`检测完成，新增 ${value.found} 条冲突。`);
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }

  async function resolve(id: number, status: string) {
    setPending(true);
    try {
      await request(`/knowledge/conflicts/${id}/status`, "POST", { status });
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }

  return (
    <section className="kb-search-content">
      <p className="kb-help">
        冲突检测比较“数字不同、措辞相同”的片段，用于发现两份资料说法不一致；过期引用列出仍指向旧版本的来源。
      </p>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {feedback && (
        <p className="kb-feedback success" role="status">
          {feedback}
        </p>
      )}
      <div className="form-actions">
        <button disabled={pending} onClick={() => void detect()}>
          {pending ? "正在检测…" : "检测冲突"}
        </button>
        <button disabled={loading} onClick={() => void load()}>
          {loading ? "正在读取…" : "刷新"}
        </button>
      </div>
      <h3 className="kb-subhead">冲突（{conflicts.length}）</h3>
      {!loading && !conflicts.length && (
        <p className="kb-search-placeholder">没有发现数字冲突。</p>
      )}
      <ul className="kb-gap-list">
        {conflicts.map((item) => (
          <li key={item.id}>
            <div>
              <strong>{item.detail}</strong>
              <small>
                {item.document_a} ↔ {item.document_b}
              </small>
            </div>
            <div className="kb-gap-actions">
              <button
                disabled={pending}
                onClick={() => void resolve(item.id, "resolved")}
              >
                标记已解决
              </button>
              <button
                disabled={pending}
                onClick={() => void resolve(item.id, "ignored")}
              >
                忽略
              </button>
            </div>
          </li>
        ))}
      </ul>
      <h3 className="kb-subhead">过期引用（{outdated.length}）</h3>
      {!loading && !outdated.length && (
        <p className="kb-search-placeholder">没有指向旧版本的引用。</p>
      )}
      <ul className="kb-gap-list">
        {outdated.map((item) => (
          <li key={`${item.run_id}-${item.source_id}`}>
            <div>
              <strong>{item.title}</strong>
              <small>
                引用 v{item.cited_version}，当前 v{item.active_version} · 运行{" "}
                {item.run_id}
              </small>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

function KnowledgeSettings({
  request,
  baseID,
}: {
  request: Api;
  baseID: string;
}) {
  const [config, setConfig] = useState<BaseConfig>({
    expand: true,
    llm_expand: false,
    gate: false,
  });
  const [vocabulary, setVocabulary] = useState<VocabEntry[]>([]);
  const [term, setTerm] = useState("");
  const [expansion, setExpansion] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");

  useEffect(() => {
    void Promise.all([
      request<BaseConfig>(`/knowledge/bases/${baseID}/config`),
      request<VocabEntry[]>(`/knowledge/bases/${baseID}/vocabulary`),
    ])
      .then(([cfg, words]) => {
        setConfig(cfg);
        setVocabulary(words);
      })
      .catch((error) => setError(message(error)));
  }, [baseID]);

  async function save(next: BaseConfig) {
    setPending(true);
    setError("");
    setFeedback("");
    try {
      setConfig(await request<BaseConfig>(`/knowledge/bases/${baseID}/config`, "PUT", next));
      setFeedback("检索设置已保存，只影响之后提交的检索。");
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }

  async function addWord() {
    setPending(true);
    setError("");
    try {
      setVocabulary(
        await request<VocabEntry[]>(`/knowledge/bases/${baseID}/vocabulary`, "POST", {
          term,
          expansion,
        }),
      );
      setTerm("");
      setExpansion("");
      setFeedback("词条已保存。");
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }

  async function removeWord(value: string) {
    setPending(true);
    try {
      await request(
        `/knowledge/bases/${baseID}/vocabulary?term=${encodeURIComponent(value)}`,
        "DELETE",
      );
      setVocabulary((words) => words.filter((word) => word.term !== value));
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(false);
    }
  }

  return (
    <details className="kb-settings">
      <summary>检索设置与领域词表</summary>
      <div className="kb-settings-body">
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {feedback && (
          <p className="kb-feedback success" role="status">
            {feedback}
          </p>
        )}
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={config.expand}
            disabled={pending}
            onChange={(event) => void save({ ...config, expand: event.target.checked })}
          />
          启用规则同义词扩展
        </label>
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={config.gate}
            disabled={pending}
            onChange={(event) => void save({ ...config, gate: event.target.checked })}
          />
          评测退化时阻止替换与重新索引（可在调用时用 force 覆盖）
        </label>
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={config.llm_expand}
            disabled={pending}
            onChange={(event) => void save({ ...config, llm_expand: event.target.checked })}
          />
          允许模型改写 2 条检索查询（额外一次模型调用，失败自动回退）
        </label>
        <div className="kb-vocab">
          <strong>领域词表</strong>
          {!vocabulary.length && <p className="muted">还没有自定义词条。</p>}
          <ul>
            {vocabulary.map((word) => (
              <li key={word.term}>
                <span>{word.term}</span>
                <span>→ {word.expansion}</span>
                <button
                  type="button"
                  disabled={pending}
                  onClick={() => void removeWord(word.term)}
                >
                  删除
                </button>
              </li>
            ))}
          </ul>
          <div className="kb-vocab-form">
            <input
              aria-label="领域词条"
              maxLength={100}
              value={term}
              onChange={(event) => setTerm(event.target.value)}
              placeholder="词条，例如：报销"
            />
            <input
              aria-label="扩展词"
              maxLength={200}
              value={expansion}
              onChange={(event) => setExpansion(event.target.value)}
              placeholder="扩展词，例如：费用核销"
            />
            <button
              type="button"
              disabled={pending || !term.trim() || !expansion.trim()}
              onClick={() => void addWord()}
            >
              添加词条
            </button>
          </div>
        </div>
      </div>
    </details>
  );
}

function KnowledgeGaps({
  request,
  baseID,
  active,
}: {
  request: Api;
  baseID: string;
  active: boolean;
}) {
  const [gaps, setGaps] = useState<ContentGap[]>([]);
  const [status, setStatus] = useState("open");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(0);

  async function load(next = status) {
    setLoading(true);
    setError("");
    try {
      setGaps(
        await request<ContentGap[]>(
          `/knowledge/gaps?knowledge_id=${encodeURIComponent(baseID)}&status=${next}`,
        ),
      );
    } catch (error) {
      setError(message(error));
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    if (!active) return;
    void load(status);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [baseID, status, active]);

  async function act(id: number, action: "resolved" | "ignored" | "delete") {
    setPending(id);
    setError("");
    try {
      if (action === "delete") await request(`/knowledge/gaps/${id}`, "DELETE");
      else
        await request(`/knowledge/gaps/${id}/status`, "POST", {
          status: action,
        });
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setPending(0);
    }
  }

  return (
    <section className="kb-search-content kb-gaps">
      <p className="kb-help">
        检索没有命中时，问题会记录在这里，用于补充资料或调整用词。只保存问题文本，不保存回答或来源。
      </p>
      <div className="kb-gap-toolbar">
        <label>
          <span className="sr-only">缺口状态</span>
          <select
            aria-label="缺口状态"
            value={status}
            onChange={(event) => setStatus(event.target.value)}
          >
            <option value="open">待处理</option>
            <option value="resolved">已解决</option>
            <option value="ignored">已忽略</option>
            <option value="">全部</option>
          </select>
        </label>
        <button disabled={loading} onClick={() => void load()}>
          {loading ? "正在读取…" : "刷新"}
        </button>
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {!loading && !gaps.length && !error && (
        <p className="kb-search-placeholder">
          还没有记录到未命中的问题。检索到空结果时会自动出现在这里。
        </p>
      )}
      {!!gaps.length && (
        <ul className="kb-gap-list">
          {gaps.map((gap) => (
            <li key={gap.id}>
              <div>
                <strong>{gap.query}</strong>
                <small>
                  命中 0 次 · 出现 {gap.count} 次 · 最近{" "}
                  {new Date(gap.last_seen).toLocaleString("zh-CN", {
                    hourCycle: "h23",
                  })}
                </small>
              </div>
              <div className="kb-gap-actions">
                {gap.status !== "resolved" && (
                  <button
                    disabled={pending === gap.id}
                    onClick={() => void act(gap.id, "resolved")}
                  >
                    标记已解决
                  </button>
                )}
                {gap.status !== "ignored" && (
                  <button
                    disabled={pending === gap.id}
                    onClick={() => void act(gap.id, "ignored")}
                  >
                    忽略
                  </button>
                )}
                <button
                  className="kb-danger"
                  disabled={pending === gap.id}
                  onClick={() => void act(gap.id, "delete")}
                >
                  删除
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function TracePanel({ trace }: { trace: RetrievalTrace }) {
  const ms = (value: number) => `${(value / 1e6).toFixed(2)} ms`;
  return (
    <details className="kb-trace">
      <summary>
        <Icon name="caret-right" size={14} className="disclosure-icon" />
        检索过程 · {trace.query_type} · {trace.match_mode} ·{" "}
        {trace.total_candidates} 个片段 · {ms(trace.duration_ns)}
      </summary>
      <div className="kb-trace-body">
        <dl className="kb-trace-meta">
          <dt>查询</dt>
          <dd>{trace.query}</dd>
          <dt>分词</dt>
          <dd>{trace.terms.join(" / ") || "无"}</dd>
          <dt>范围</dt>
          <dd>{trace.knowledge_ids.length} 个知识库</dd>
        </dl>
        <ol className="kb-trace-stages">
          {trace.stages.map((stage, index) => (
            <li key={`${stage.name}-${index}`}>
              <span>{stage.name}</span>
              <span>{stage.detail ?? ""}</span>
              <span>{stage.hits} 个</span>
              <span>{ms(stage.duration_ns)}</span>
            </li>
          ))}
        </ol>
        {trace.selected.length > 0 && (
          <ol className="kb-trace-hits">
            {trace.selected.map((hit) => (
              <li key={hit.source_id}>
                <span className="kb-trace-rank">#{hit.rank}</span>
                <span className="kb-trace-title">{hit.title}</span>
                <span className="kb-trace-score">
                  BM25 {hit.score.toFixed(4)}
                </span>
                <span className="kb-trace-terms">
                  {(hit.matched_terms ?? []).join(" ")}
                </span>
              </li>
            ))}
          </ol>
        )}
        {trace.rerank && (
          <p className="kb-trace-rerank">
            重排：{trace.rerank.mode}
            {trace.rerank.fallback
              ? ` · 已回退（${trace.rerank.fallback}）`
              : ` · 调整 ${changedRanks(trace.rerank.before, trace.rerank.after)} 个位置`}
          </p>
        )}
        <p className="kb-help">
          候选分数为 SQLite BM25（越小越相关）；入选片段显示融合后分数（越大越相关）。
        </p>
      </div>
    </details>
  );
}

function KnowledgeEval({
  request,
  baseID,
  documents,
}: {
  request: Api;
  baseID: string;
  documents: Document[];
}) {
  const [questions, setQuestions] = useState<EvalQuestion[]>([]);
  const [runs, setRuns] = useState<EvalRunSummary[]>([]);
  const [run, setRun] = useState<EvalRun | null>(null);
  const [question, setQuestion] = useState("");
  const [expected, setExpected] = useState<string[]>([]);
  const [answerable, setAnswerable] = useState(true);
  const [importing, setImporting] = useState(false);
  const [answerRun, setAnswerRun] = useState<{ metrics: EvalMetrics } | null>(null);
  const [importFeedback, setImportFeedback] = useState("");
  const [pending, setPending] = useState("");
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState("");
  const ready = documents.filter((doc) => doc.state === "ready");

  async function load() {
    const [values, history] = await Promise.all([
      request<EvalQuestion[]>(`/knowledge/bases/${baseID}/eval/questions`),
      request<EvalRunSummary[]>(`/knowledge/bases/${baseID}/eval/runs?limit=10`),
    ]);
    setQuestions(values);
    setRuns(history);
  }
  useEffect(() => {
    let active = true;
    void Promise.all([
      request<EvalQuestion[]>(`/knowledge/bases/${baseID}/eval/questions`),
      request<EvalRunSummary[]>(`/knowledge/bases/${baseID}/eval/runs?limit=10`),
    ])
      .then(([values, history]) => {
        if (!active) return;
        setQuestions(values);
        setRuns(history);
      })
      .catch((error) => {
        if (active) setError(message(error));
      });
    return () => {
      active = false;
    };
  }, [baseID]);

  async function add(event: FormEvent) {
    event.preventDefault();
    if (!question.trim() || (answerable && !expected.length)) return;
    setPending("add");
    setError("");
    setFeedback("");
    try {
      await request(`/knowledge/bases/${baseID}/eval/questions`, "POST", {
        question,
        expected_document_ids: expected,
        answerable,
      });
      setQuestion("");
      setExpected([]);
      setFeedback("评测问题已添加。");
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setPending("");
    }
  }

  async function remove(id: string) {
    setPending(id);
    setError("");
    try {
      await request(`/knowledge/eval/questions/${id}`, "DELETE");
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setPending("");
    }
  }

  async function evaluate() {
    setPending("run");
    setError("");
    setFeedback("");
    try {
      const value = await request<EvalRun>(
        `/knowledge/bases/${baseID}/eval/run`,
        "POST",
      );
      setRun(value);
      setFeedback(`评测完成：Recall@5 ${percent(value.metrics.recall_at_5)}。`);
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setPending("");
    }
  }

  async function importFile(file: File) {
    setImporting(true);
    setError("");
    setImportFeedback("");
    try {
      const raw = await file.text();
      const csv = /\.csv$/i.test(file.name);
      const response = await fetch(
        appURL(`/api/knowledge/bases/${baseID}/eval/questions/import`),
        {
          method: "POST",
          headers: { "Content-Type": csv ? "text/csv" : "application/json" },
          body: raw,
        },
      );
      const value = await response.json();
      if (!response.ok) throw new Error(value.error ?? "导入失败。");
      setImportFeedback(
        `已导入 ${value.imported} 条` +
          (value.failures?.length ? `，${value.failures.length} 条失败` : ""),
      );
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setImporting(false);
    }
  }

  async function evaluateAnswers() {
    setPending("answers");
    setError("");
    setFeedback("");
    try {
      const value = await request<{ metrics: EvalMetrics }>(
        `/knowledge/bases/${baseID}/eval/answers`,
        "POST",
      );
      setAnswerRun(value);
      setFeedback("回答质量评测已完成。");
      await load();
    } catch (error) {
      setError(message(error));
    } finally {
      setPending("");
    }
  }

  async function openRun(id: string) {
    setPending(id);
    setError("");
    try {
      setRun(await request<EvalRun>(`/knowledge/eval/runs/${id}`));
    } catch (error) {
      setError(message(error));
    } finally {
      setPending("");
    }
  }

  const title = (id: string) =>
    documents.find((doc) => doc.id === id)?.title ?? id;

  return (
    <section className="kb-search-content kb-eval">
      <p className="kb-help">
        用黄金问题衡量检索效果。评测只查询本地索引，不调用模型，可重复运行并对比。
      </p>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {feedback && (
        <p className="kb-feedback success" role="status">
          {feedback}
        </p>
      )}
      <form className="kb-eval-form" onSubmit={add}>
        <label>
          评测问题
          <input
            required
            maxLength={2000}
            value={question}
            onChange={(event) => setQuestion(event.target.value)}
            placeholder="例如：出差住宿标准是多少？"
          />
        </label>
        <label className="checkbox-label">
          <input
            type="checkbox"
            checked={!answerable}
            onChange={(event) => setAnswerable(!event.target.checked)}
          />
          资料中无法回答（用于衡量拒答）
        </label>
        <fieldset className="kb-eval-docs" disabled={!answerable}>
          <legend>期望来源文档</legend>
          {!ready.length && (
            <p className="muted">先上传并等待文档变为可检索。</p>
          )}
          {ready.map((doc) => (
            <label key={doc.id} className="checkbox-label">
              <input
                type="checkbox"
                checked={expected.includes(doc.id)}
                onChange={(event) =>
                  setExpected((values) =>
                    event.target.checked
                      ? [...values, doc.id]
                      : values.filter((id) => id !== doc.id),
                  )
                }
              />
              {doc.title}
            </label>
          ))}
        </fieldset>
        <div className="form-actions">
          <button
            className="primary"
            disabled={pending === "add" || !question.trim() || !expected.length}
          >
            {pending === "add" ? "正在添加…" : "添加问题"}
          </button>
          <button
            type="button"
            disabled={!questions.length || pending === "run"}
            onClick={() => void evaluate()}
          >
            {pending === "run" ? "正在评测…" : "运行检索评测"}
          </button>
          <button
            type="button"
            disabled={!questions.length || pending === "answers"}
            onClick={() => void evaluateAnswers()}
          >
            {pending === "answers" ? "正在评测…" : "评测回答质量"}
          </button>
        </div>
        <div className="kb-eval-io">
          <label className="kb-eval-import">
            {importing ? "正在导入…" : "导入 CSV / JSON"}
            <input
              type="file"
              accept=".csv,.json,text/csv,application/json"
              disabled={importing}
              onChange={(event) => {
                const file = event.target.files?.[0];
                if (file) void importFile(file);
                event.target.value = "";
              }}
            />
          </label>
          <a
            href={appURL(
              `/api/knowledge/bases/${baseID}/eval/questions/export?format=csv`,
            )}
            download
          >
            导出 CSV
          </a>
          <a
            href={appURL(
              `/api/knowledge/bases/${baseID}/eval/questions/export`,
            )}
            download="eval-questions.json"
          >
            导出 JSON
          </a>
        </div>
        {importFeedback && (
          <p className="kb-feedback success" role="status">
            {importFeedback}
          </p>
        )}
      </form>
      {questions.length > 0 && (
        <ul className="kb-eval-questions">
          {questions.map((item) => (
            <li key={item.id}>
              <div>
                <strong>{item.question}</strong>
                <small>
                  期望来源：{item.expected_document_ids.map(title).join("、")}
                </small>
              </div>
              <button
                disabled={pending === item.id}
                onClick={() => void remove(item.id)}
              >
                删除
              </button>
            </li>
          ))}
        </ul>
      )}
      {answerRun && (
        <div className="kb-eval-metrics kb-eval-answer">
          <div>
            <strong>{percent(answerRun.metrics.citation_precision ?? 0)}</strong>
            <span>引用准确率</span>
          </div>
          <div>
            <strong>{percent(answerRun.metrics.faithfulness ?? 0)}</strong>
            <span>忠实度（数字核对）</span>
          </div>
          <div>
            <strong>{percent(answerRun.metrics.abstention_accuracy ?? 0)}</strong>
            <span>拒答准确率</span>
          </div>
        </div>
      )}
      {run && (
        <div className="kb-eval-run">
          <div className="kb-eval-metrics">
            {[
              ["Recall@1", run.metrics.recall_at_1],
              ["Recall@3", run.metrics.recall_at_3],
              ["Recall@5", run.metrics.recall_at_5],
              ["MRR", run.metrics.mrr],
            ].map(([label, value]) => (
              <div key={label as string}>
                <strong>{percent(value as number)}</strong>
                <span>{label}</span>
              </div>
            ))}
            <div>
              <strong>{run.metrics.questions}</strong>
              <span>问题数</span>
            </div>
            <div>
              <strong>{percent(run.metrics.abstention_accuracy ?? 0)}</strong>
              <span>拒答准确</span>
            </div>
            <div>
              <strong>{run.metrics.avg_latency_ms.toFixed(1)}</strong>
              <span>平均毫秒</span>
            </div>
            {run.metrics.baseline_recall_at_5 !== undefined && (
              <div>
                <strong>{percent(run.metrics.baseline_recall_at_5)}</strong>
                <span>
                  基线 R@5
                  {run.metrics.recall_at_5 >= run.metrics.baseline_recall_at_5
                    ? ` (+${((run.metrics.recall_at_5 - run.metrics.baseline_recall_at_5) * 100).toFixed(1)})`
                    : ` (${((run.metrics.recall_at_5 - run.metrics.baseline_recall_at_5) * 100).toFixed(1)})`}
                </span>
              </div>
            )}
          </div>
          <ul className="kb-eval-cases">
            {run.cases.map((item) => (
              <li key={item.question_id} className={item.pass ? "" : "failed"}>
                <span>{item.pass ? `#${item.first_rank}` : "未命中"}</span>
                <strong>{item.question}</strong>
                <small>
                  {item.ranked_document_ids.length
                    ? item.ranked_document_ids.slice(0, 3).map(title).join("、")
                    : "无结果"}
                </small>
              </li>
            ))}
          </ul>
        </div>
      )}
      {runs.length > 0 && (
        <div className="kb-eval-history">
          <h3>历史评测</h3>
          <ul>
            {runs.map((item) => (
              <li key={item.id}>
                <button
                  disabled={pending === item.id}
                  onClick={() => void openRun(item.id)}
                >
                  {new Date(item.created).toLocaleString("zh-CN", {
                    hourCycle: "h23",
                  })}
                </button>
                <span>R@5 {percent(item.metrics.recall_at_5)}</span>
                <span>MRR {percent(item.metrics.mrr)}</span>
                <span>{item.metrics.questions} 题</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

const percent = (value: number) => `${(value * 100).toFixed(1)}%`;
const changedRanks = (before: string[], after: string[]) =>
  after.filter((id, index) => before[index] !== id).length;

function KnowledgeSearch({
  request,
  baseID,
  revision,
}: {
  request: Api;
  baseID: string;
  revision: string;
}) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<KnowledgeHit[] | null>(null);
  const [trace, setTrace] = useState<RetrievalTrace | null>(null);
  const [searched, setSearched] = useState("");
  const [rerank, setRerank] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  const generation = useRef(0);
  useEffect(() => {
    generation.current++;
    setResults(null);
    setTrace(null);
    setSearched("");
    setPending(false);
    setError("");
  }, [revision]);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  async function search(event: FormEvent) {
    event.preventDefault();
    setPending(true);
    setError("");
    setResults(null);
    setTrace(null);
    const submitted = query;
    const currentGeneration = ++generation.current;
    try {
      const result = await request<{
        hits: KnowledgeHit[];
        trace?: RetrievalTrace;
      }>("/knowledge/search", "POST", {
        query: submitted,
        knowledge_ids: [baseID],
        limit: 5,
        trace: true,
        rerank,
      });
      if (mounted.current && generation.current === currentGeneration) {
        setResults(result.hits);
        setTrace(result.trace ?? null);
        setSearched(submitted);
      }
    } catch (error) {
      if (mounted.current && generation.current === currentGeneration)
        setError(message(error));
    } finally {
      if (mounted.current && generation.current === currentGeneration)
        setPending(false);
    }
  }
  return (
    <section className="kb-search-content">
      <p className="kb-help">
        使用文档中的关键词验证检索结果。直接查询本地索引，不调用模型。
      </p>
      <form onSubmit={search}>
        <label>
          <span className="sr-only">检索问题</span>
          <input
            required
            maxLength={500}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="输入问题或资料中的关键词"
          />
        </label>
        <button className="primary" disabled={pending || !query.trim()}>
          {pending ? "正在检索…" : "检索当前知识库"}
        </button>
      </form>
      <label className="checkbox-label kb-rerank-toggle">
        <input
          type="checkbox"
          checked={rerank}
          onChange={(event) => setRerank(event.target.checked)}
        />
        使用模型重排 Top 20（额外一次模型调用，失败自动回退）
      </label>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {results === null && !error && !pending && (
        <p className="kb-search-placeholder">
          检索结果会在这里展示，可打开来源查看片段与原文。
        </p>
      )}
      {results && (
        <p className="kb-results-count">
          “{searched}” ·{" "}
          {results.length
            ? `${results.length} 条来源`
            : "没有找到依据。请检查文档是否处理完成，或使用资料中的关键词。"}
        </p>
      )}
      {trace && <TracePanel trace={trace} />}
      {results && (
        <ol className="knowledge-hits">
          {results.map((hit) => (
            <li key={hit.source_id}>
              <a
                href={hit.source_url}
                target="_blank"
                rel="noopener noreferrer"
              >
                {hit.title} · v{hit.version}
                {hit.location?.page ? ` · 第 ${hit.location.page} 页` : ""}{" "}
                <Icon name="arrow-up-right" size={14} />
              </a>
              <p>{hit.text}</p>
              {hit.location?.paragraph && (
                <small>解析文本第 {hit.location.paragraph} 段</small>
              )}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
export function KnowledgeSource({
  request,
  sourceID,
}: {
  request: Api;
  sourceID: string;
}) {
  const [source, setSource] = useState<Source | null>(null);
  const [offset, setOffset] = useState(0);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void request<Source>(
      `/knowledge/sources/${encodeURIComponent(sourceID)}?offset=${offset}&limit=4000`,
    )
      .then((source) => {
        if (active) {
          setSource(source);
          setError("");
        }
      })
      .catch((error) => {
        if (active) setError(message(error));
      });
    return () => {
      active = false;
    };
  }, [sourceID, offset]);
  return (
    <main className="knowledge-source-page">
      <a href={appURL("/")}>
        <Icon name="arrow-left" size={16} /> 返回 SiAgent 工作台
      </a>
      <p className="eyebrow">知识来源 · 原文版本可追溯</p>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {!source && !error && <p role="status">正在读取来源…</p>}
      {source && (
        <>
          <h1>{source.title}</h1>
          <p className="muted">
            {source.deleted
              ? "来源版本"
              : source.historical
                ? "历史版本"
                : "检索版本"}{" "}
            v{source.version}
            {source.location?.page ? ` · 第 ${source.location.page} 页` : ""}
          </p>
          {source.deleted ? (
            <p className="permission-notice">
              来源已删除。历史引用标识保留，该内容不再参与检索。
            </p>
          ) : (
            <>
              <section className="knowledge-source-excerpt">
                <h2>检索片段</h2>
                <pre>{source.text}</pre>
                <small>
                  {source.location?.paragraph && (
                    <>解析文本第 {source.location.paragraph} 段</>
                  )}
                  {source.location?.end !== undefined && (
                    <>
                      {" "}
                      · 段内字符 {(source.location.start ?? 0) + 1}–
                      {source.location.end}
                    </>
                  )}
                </small>
              </section>
              <section>
                <h2>解析原文</h2>
                <p className="muted">
                  第 {source.offset + 1}–
                  {source.offset + Array.from(source.document_text).length}{" "}
                  字符，共 {source.total_characters.toLocaleString()}{" "}
                  字符。文档内容以文本展示。
                </p>
                <pre className="knowledge-original">{source.document_text}</pre>
                <div className="form-actions">
                  <button
                    disabled={offset === 0}
                    onClick={() => setOffset(Math.max(0, offset - 4000))}
                  >
                    上一段原文
                  </button>
                  <button
                    disabled={source.next_offset === undefined}
                    onClick={() => setOffset(source.next_offset ?? offset)}
                  >
                    下一段原文
                  </button>
                </div>
              </section>
            </>
          )}
        </>
      )}
    </main>
  );
}
