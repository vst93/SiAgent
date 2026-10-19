import { memo, useRef, useState, type ReactNode } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

const plugins = [remarkGfm];

function CodeBlock({ children }: { children?: ReactNode }) {
  const ref = useRef<HTMLPreElement>(null);
  const [copied, setCopied] = useState(false);
  async function copy() {
    const text = ref.current?.innerText ?? "";
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      // Clipboard access can be unavailable; the code remains selectable.
    }
  }
  return (
    <div className="markdown-code">
      <button
        type="button"
        className="markdown-copy"
        aria-label="复制代码"
        title="复制代码"
        onClick={() => void copy()}
      >
        {copied ? "已复制" : "复制"}
      </button>
      <pre ref={ref}>{children}</pre>
    </div>
  );
}

export const MessageMarkdown = memo(function MessageMarkdown({
  content,
}: {
  content: string;
}) {
  return (
    <div className="message-markdown">
      <Markdown
        remarkPlugins={plugins}
        components={{
          a: ({ href, children }) => (
            <a
              href={href || undefined}
              target="_blank"
              rel="noopener noreferrer"
            >
              {children}
            </a>
          ),
          // Images stay explicit links rather than making unsolicited remote requests.
          img: ({ src, alt }) =>
            src ? (
              <a href={src} target="_blank" rel="noopener noreferrer">
                {alt || "查看图片"}
              </a>
            ) : (
              <span>{alt}</span>
            ),
          table: ({ children }) => (
            <div className="markdown-table">
              <table>{children}</table>
            </div>
          ),
          pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
        }}
      >
        {content}
      </Markdown>
    </div>
  );
});
