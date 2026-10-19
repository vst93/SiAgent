package knowledge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-ego/gse"
)

type page struct {
	Number int    `json:"number,omitempty"`
	Text   string `json:"text"`
}
type limitedOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedOutput) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		b.exceeded = true
		return 0, errors.New("output limit exceeded")
	}
	return b.Buffer.Write(data)
}
func pdfAvailable() bool {
	path, err := exec.LookPath("pdftotext")
	if err != nil {
		return false
	}
	_, err = exec.LookPath(filepath.Join(filepath.Dir(path), "pdfinfo"))
	return err == nil
}
func parserCommand(ctx context.Context, program string, limit int, args ...string) (string, error) {
	output := &limitedOutput{limit: limit}
	command := exec.CommandContext(ctx, program, args...)
	command.WaitDelay = 2 * time.Second
	command.Env = []string{"LC_ALL=C", "LANG=C"}
	command.Stdout = output
	command.Stderr = io.Discard
	err := command.Run()
	if output.exceeded {
		return "", invalid("PDF 提取内容超过上限，请拆分文件后重试。")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", invalid("PDF 解析超时，请拆分或简化文档后重试。")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", invalid("PDF 损坏、加密或无法提取文本，请检查文件；扫描件和 OCR 暂不支持。")
	}
	return output.String(), nil
}
func parse(ctx context.Context, path, kind string, limits Limits) ([]page, error) {
	if kind != "pdf" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return nil, invalid("文档不是有效的 UTF-8 纯文本，请转换编码后重试。")
		}
		text := strings.TrimPrefix(string(data), "\ufeff")
		text = strings.ReplaceAll(text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		if utf8.RuneCountInString(text) > limits.MaxCharacters {
			return nil, invalid(fmt.Sprintf("提取文本超过 %d 字符上限，请拆分文档或由管理员调整 knowledge.max_characters。", limits.MaxCharacters))
		}
		if strings.TrimSpace(text) == "" {
			return nil, invalid("文档没有可检索文本。")
		}
		return []page{{Text: text}}, nil
	}
	program, err := exec.LookPath("pdftotext")
	if err != nil || !pdfAvailable() {
		return nil, unavailable("PDF 解析依赖未就绪，请安装 Poppler（pdftotext 和 pdfinfo）后重试；TXT 和 Markdown 不受影响。")
	}
	pdfCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := parserCommand(pdfCtx, filepath.Join(filepath.Dir(program), "pdfinfo"), 32000, path)
	if err != nil {
		return nil, err
	}
	if regexp.MustCompile(`(?m)^Encrypted:\s+yes`).MatchString(info) {
		return nil, invalid("暂不支持加密 PDF，请先提供未加密副本。")
	}
	match := regexp.MustCompile(`(?m)^Pages:\s+(\d+)`).FindStringSubmatch(info)
	if len(match) != 2 {
		return nil, invalid("无法确定 PDF 页数，请检查文件。")
	}
	count, _ := strconv.Atoi(match[1])
	if count < 1 || count > limits.MaxPDFPages {
		return nil, invalid(fmt.Sprintf("PDF 页数须在 1–%d 页，请拆分文件后重试。", limits.MaxPDFPages))
	}
	text, err := parserCommand(pdfCtx, program, 4*limits.MaxCharacters+1, "-enc", "UTF-8", "-eol", "unix", "-layout", path, "-")
	if err != nil {
		return nil, err
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > limits.MaxCharacters {
		return nil, invalid(fmt.Sprintf("PDF 提取文本超过 %d 字符上限或编码无效，请拆分文件。", limits.MaxCharacters))
	}
	parts := strings.Split(text, "\f")
	if len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) != count {
		return nil, invalid("PDF 提取页数与原文不一致，未建立索引，请检查文件。")
	}
	pages := []page{}
	nonempty := false
	for index, text := range parts {
		if strings.TrimSpace(text) != "" {
			nonempty = true
		}
		pages = append(pages, page{Number: index + 1, Text: text})
	}
	if !nonempty {
		return nil, invalid("PDF 没有可提取文本；扫描件和 OCR 暂不支持。")
	}
	return pages, nil
}

var paragraphBreak = regexp.MustCompile(`\n[\t ]*\n`)

// Keep paragraph coordinates stable while preferring complete sentences and words.
func chunkEnd(text []rune, start int) int {
	end := min(start+600, len(text))
	if end == len(text) {
		return end
	}
	for i := end; i >= start+360; i-- {
		switch text[i-1] {
		case '\n', '。', '！', '？', '!', '?', '；', ';':
			return i
		case '.':
			if !(i > 1 && unicode.IsDigit(text[i-2]) && unicode.IsDigit(text[i])) {
				return i
			}
		}
	}
	for i := end; i >= start+480; i-- {
		if unicode.IsSpace(text[i-1]) {
			return i
		}
	}
	return end
}

func split(ctx context.Context, documentID string, version int, pages []page, limits Limits) ([]chunk, int, error) {
	chunks := []chunk{}
	characters := 0
	paragraph := 0
	for _, page := range pages {
		characters += utf8.RuneCountInString(page.Text)
		if characters > limits.MaxCharacters {
			return nil, 0, invalid(fmt.Sprintf("提取文本超过 %d 字符上限。", limits.MaxCharacters))
		}
		for _, block := range paragraphBreak.Split(page.Text, -1) {
			block = strings.TrimSpace(block)
			if block == "" {
				continue
			}
			paragraph++
			runes := []rune(block)
			for start := 0; start < len(runes); {
				if err := ctx.Err(); err != nil {
					return nil, 0, err
				}
				end := chunkEnd(runes, start)
				if len(chunks) >= limits.MaxChunks {
					return nil, 0, invalid(fmt.Sprintf("文档超过 %d 个片段，请合并过短段落或拆分文档后重试。", limits.MaxChunks))
				}
				position := len(chunks)
				chunks = append(chunks, chunk{ID: sourceID(documentID, version, position), Text: string(runes[start:end]), Location: Location{Page: page.Number, Paragraph: paragraph, Start: start, End: end}, Position: position})
				if end == len(runes) {
					break
				}
				start = end - 80
			}
		}
	}
	if len(chunks) == 0 {
		return nil, 0, invalid("文档没有可检索文本。")
	}
	return chunks, characters, nil
}

var tokenizer struct {
	once      sync.Once
	mu        sync.Mutex
	segmenter gse.Segmenter
	err       error
}
var stopwords = map[string]bool{"的": true, "了": true, "和": true, "与": true, "是": true, "在": true, "为": true, "请": true, "如何": true, "什么": true, "一个": true, "怎么": true, "the": true, "a": true, "an": true, "is": true, "are": true, "and": true, "or": true, "to": true, "for": true, "of": true, "in": true, "on": true, "how": true, "what": true}

func tokenize(text string) (string, error) {
	tokenizer.once.Do(func() { tokenizer.segmenter.SkipLog = true; tokenizer.err = tokenizer.segmenter.LoadDictEmbed("zh") })
	if tokenizer.err != nil {
		return "", unavailable("本地分词器初始化失败，请检查安装和版本。")
	}
	tokenizer.mu.Lock()
	words := tokenizer.segmenter.CutSearch(strings.ToLower(text), true)
	tokenizer.mu.Unlock()
	terms := []string{}
	for _, word := range words {
		normalized := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return ' '
		}, word)
		for _, term := range strings.Fields(normalized) {
			if !stopwords[term] && utf8.RuneCountInString(term) <= 80 {
				terms = append(terms, term)
			}
		}
	}
	return strings.Join(terms, " "), nil
}
