package knowledge

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Limits bound what a knowledge base accepts. They are deployment settings:
// large documents cost parsing time, memory and disk, while some formats need
// optional parsers, so an administrator may need to raise or narrow them.
type Limits struct {
	MaxUploadBytes int64
	MaxCharacters  int
	MaxChunks      int
	MaxPDFPages    int
	// Extensions lists accepted kinds without a leading dot: "txt", "markdown",
	// "pdf", "docx" and any extension declared by an installed parser extension.
	// An empty list keeps the built-in defaults.
	Extensions []string
	// ExtendWithParsers also accepts formats declared by installed parser
	// extensions. It stays on for the default list and turns off when the
	// deployment enumerates its own allowed extensions.
	ExtendWithParsers bool
}

const (
	DefaultMaxUploadBytes int64 = 20 << 20
	DefaultMaxCharacters        = 1000000
	DefaultMaxChunks            = 5000
	DefaultMaxPDFPages          = 1000
)

func DefaultLimits() Limits {
	return Limits{
		MaxUploadBytes: DefaultMaxUploadBytes,
		MaxCharacters:  DefaultMaxCharacters,
		MaxChunks:      DefaultMaxChunks,
		MaxPDFPages:    DefaultMaxPDFPages,
		Extensions:     []string{"txt", "markdown", "pdf", "docx"},
		// Installed parser extensions extend the default list; enumerating the
		// list in the deployment config restricts it instead.
		ExtendWithParsers: true,
	}
}

// KindOf maps a filename to the stored document kind. Markdown covers both
// .md and .markdown, and plugin parsers use the plain extension.
func KindOf(filename string) string {
	switch strings.ToLower(filepath.Ext(filepath.Base(filename))) {
	case ".txt":
		return "txt"
	case ".md", ".markdown":
		return "markdown"
	}
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
}

// Normalize fills defaults for zero values and cleans the extension list.
func (l Limits) Normalize() Limits {
	defaults := DefaultLimits()
	if l.MaxUploadBytes <= 0 {
		l.MaxUploadBytes = defaults.MaxUploadBytes
	}
	if l.MaxCharacters <= 0 {
		l.MaxCharacters = defaults.MaxCharacters
	}
	if l.MaxChunks <= 0 {
		l.MaxChunks = defaults.MaxChunks
	}
	if l.MaxPDFPages <= 0 {
		l.MaxPDFPages = defaults.MaxPDFPages
	}
	extend := len(l.Extensions) == 0
	seen := map[string]bool{}
	extensions := []string{}
	for _, value := range l.Extensions {
		kind := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, ".")))
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true
		extensions = append(extensions, kind)
	}
	if len(extensions) == 0 {
		extensions = append(extensions, defaults.Extensions...)
	}
	sort.Strings(extensions)
	l.Extensions = extensions
	if extend {
		l.ExtendWithParsers = true
	}
	return l
}

func (l Limits) Validate() error {
	value := l.Normalize()
	// A 1 MiB floor keeps accidental small values from breaking normal uploads;
	// the 1 GiB ceiling bounds memory and disk use of a single document.
	if value.MaxUploadBytes < 1<<20 || value.MaxUploadBytes > 1<<30 {
		return fmt.Errorf("knowledge.max_file_bytes must be between 1 MiB and 1 GiB")
	}
	if value.MaxCharacters < 1000 || value.MaxCharacters > 50_000_000 {
		return fmt.Errorf("knowledge.max_characters must be between 1,000 and 50,000,000")
	}
	if value.MaxChunks < 1 || value.MaxChunks > 200000 {
		return fmt.Errorf("knowledge.max_chunks must be between 1 and 200,000")
	}
	if value.MaxPDFPages < 1 || value.MaxPDFPages > 10000 {
		return fmt.Errorf("knowledge.max_pdf_pages must be between 1 and 10,000")
	}
	for _, kind := range value.Extensions {
		if len(kind) > 16 || strings.IndexFunc(kind, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '-')
		}) >= 0 {
			return fmt.Errorf("knowledge.allowed_extensions contains an invalid extension: %q", kind)
		}
	}
	return nil
}

func (l Limits) allowsKind(kind string) bool {
	for _, value := range l.Extensions {
		if value == kind {
			return true
		}
	}
	return false
}

// builtinKind reports whether the core ships a parser for this kind.
func builtinKind(kind string) bool {
	switch kind {
	case "txt", "markdown", "pdf", "docx":
		return true
	}
	return false
}

// Accepts reports whether a filename may be uploaded and returns its kind. A
// parser extension format is accepted only while the configured list extends to
// installed parsers; the platform still verifies that such a parser exists.
func (l Limits) Accepts(filename string) (string, bool) {
	kind := KindOf(filename)
	if kind == "" {
		return "", false
	}
	if l.allowsKind(kind) || (l.ExtendWithParsers && !builtinKind(kind)) {
		return kind, true
	}
	return "", false
}

// Description lists the accepted kinds for error messages and the UI.
func (l Limits) Description() string {
	names := make([]string, 0, len(l.Extensions))
	for _, kind := range l.Extensions {
		switch kind {
		case "markdown":
			names = append(names, "Markdown")
		case "txt":
			names = append(names, "TXT")
		case "pdf":
			names = append(names, "PDF")
		case "docx":
			names = append(names, "DOCX")
		default:
			names = append(names, strings.ToUpper(kind))
		}
	}
	value := strings.Join(names, "、")
	if l.ExtendWithParsers {
		value += "，以及已启用的解析扩展格式"
	}
	return value
}

// HumanSize formats a byte count for messages and the UI.
func HumanSize(value int64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.2g GiB", float64(value)/(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.2g MiB", float64(value)/(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.2g KiB", float64(value)/(1<<10))
	}
	return fmt.Sprintf("%d B", value)
}
