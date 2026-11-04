package knowledge

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"unicode/utf16"

	"github.com/richardlehane/mscfb"
)

var little = binary.LittleEndian

const legacyTextLimit = 1200000

// Legacy Office 97–2003 uses OLE/CFB, not ZIP. Only bounded streams are read;
// no Office application, macro engine or external-resource resolver is invoked.
func extractLegacyOffice(ctx context.Context, data []byte, kind string) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = invalid("旧版 Office 文件结构损坏，无法解析。")
		}
	}()
	if len(data) > 64<<20 {
		return "", invalid("旧版 Office 文件超过解析大小上限。")
	}
	compound, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", invalid("旧版 Office 文件不是有效的 OLE 文档，请检查格式。")
	}
	if len(compound.File) > 4096 {
		return "", invalid("旧版 Office 文件内部条目过多。")
	}
	streams := map[string][]byte{}
	var streamBytes int64
	for _, file := range compound.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := strings.ToLower(file.Name)
		// Storages are included in this inspection even when their streams are not
		// needed for text. Macros and embedded packages must not pass unnoticed.
		switch name {
		case "vba", "macros", "_vba_project", "_vba_project_cur", "mbd", "encryptedpackage", "encryptioninfo":
			return "", invalid("不支持含宏、嵌入对象或加密内容的旧版 Office 文件。")
		}
		if strings.HasPrefix(name, "mbd") || strings.Contains(name, "ole10native") || strings.Contains(name, "activex") {
			return "", invalid("不支持含嵌入对象或 ActiveX 的旧版 Office 文件。")
		}
		for _, parent := range file.Path {
			if strings.EqualFold(parent, "ObjectPool") {
				return "", invalid("不支持含嵌入对象的旧版 Office 文件。")
			}
		}
		if len(file.Path) != 0 {
			continue
		}
		switch file.Name {
		case "WordDocument", "0Table", "1Table", "Workbook", "Book", "PowerPoint Document", "Current User":
		default:
			continue
		}
		if file.Size < 0 || file.Size > int64(len(data)) {
			return "", invalid("旧版 Office 数据流大小无效。")
		}
		streamBytes += file.Size
		if streamBytes > 64<<20 {
			return "", invalid("旧版 Office 数据流总大小超过上限。")
		}
		if _, duplicate := streams[file.Name]; duplicate {
			return "", invalid("旧版 Office 文件包含重复数据流。")
		}
		value, err := io.ReadAll(io.LimitReader(file, file.Size+1))
		if err != nil || int64(len(value)) != file.Size {
			return "", invalid("旧版 Office 数据流损坏。")
		}
		streams[file.Name] = value
	}
	switch kind {
	case "doc":
		return legacyWord(ctx, streams)
	case "xls":
		return legacyExcel(ctx, streams)
	case "ppt":
		return legacyPowerPoint(ctx, streams)
	}
	return "", invalid("不支持的旧版 Office 格式。")
}

func utf16LE(data []byte) (string, error) {
	if len(data)%2 != 0 {
		return "", invalid("Office Unicode 文本长度无效。")
	}
	chars := make([]uint16, len(data)/2)
	for i := range chars {
		chars[i] = little.Uint16(data[i*2:])
	}
	return string(utf16.Decode(chars)), nil
}

// Word's compressed Unicode pieces use the Windows-1252 exceptions specified
// in MS-DOC; XLS/PPT compressed Unicode instead maps each byte to U+00xx.
func compressedUnicode(data []byte) string {
	chars := make([]rune, len(data))
	for i, c := range data {
		chars[i] = rune(c)
	}
	return string(chars)
}
func wordCompressed(data []byte) string {
	exceptions := []rune{'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8d, 'Ž', 0x8f, 0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9d, 'ž', 'Ÿ'}
	chars := make([]rune, len(data))
	for i, c := range data {
		chars[i] = rune(c)
		if c >= 0x80 && c <= 0x9f {
			chars[i] = exceptions[c-0x80]
		}
	}
	return string(chars)
}

func legacyWord(ctx context.Context, streams map[string][]byte) (string, error) {
	doc := streams["WordDocument"]
	if len(doc) < 426 || little.Uint16(doc) != 0xa5ec || little.Uint16(doc[2:]) < 0xc1 {
		return "", invalid("DOC 需要 Word 97–2003 二进制格式；更早的文档请另存为 DOC/DOCX。")
	}
	flags := little.Uint16(doc[10:])
	if flags&0x8100 != 0 {
		return "", invalid("不支持加密或密码保护的 DOC 文件。")
	}
	table := streams["0Table"]
	if flags&0x200 != 0 {
		table = streams["1Table"]
	}
	start, size := uint64(little.Uint32(doc[418:])), uint64(little.Uint32(doc[422:]))
	if size == 0 || start+size > uint64(len(table)) {
		return "", invalid("DOC 文本片段表缺失或损坏。")
	}
	clx := table[start : start+size]
	for len(clx) > 0 && clx[0] == 1 {
		if len(clx) < 3 || int(little.Uint16(clx[1:])) > len(clx)-3 {
			return "", invalid("DOC 格式属性损坏。")
		}
		clx = clx[3+int(little.Uint16(clx[1:])):]
	}
	if len(clx) < 5 || clx[0] != 2 {
		return "", invalid("DOC 缺少文本片段表。")
	}
	length := uint64(little.Uint32(clx[1:]))
	if length < 4 || length > uint64(len(clx)-5) || (length-4)%12 != 0 {
		return "", invalid("DOC 文本片段表无效。")
	}
	pieces := clx[5 : 5+length]
	count := int((length - 4) / 12)
	mainChars := uint64(little.Uint32(doc[76:]))
	if mainChars > legacyTextLimit {
		return "", invalid("DOC 正文过长，请拆分文件。")
	}
	if little.Uint32(pieces) != 0 || uint64(little.Uint32(pieces[count*4:])) < mainChars {
		return "", invalid("DOC 文本片段表未覆盖完整正文。")
	}
	var out strings.Builder
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		from, to := uint64(little.Uint32(pieces[i*4:])), uint64(little.Uint32(pieces[(i+1)*4:]))
		if to < from {
			return "", invalid("DOC 文本位置无效。")
		}
		if from >= mainChars {
			break
		}
		if to > mainChars {
			to = mainChars
		}
		fc := little.Uint32(pieces[(count+1)*4+i*8+2:])
		compressed := fc&0x40000000 != 0
		offset, bytesCount := uint64(fc&0x3fffffff), (to-from)*2
		if compressed {
			offset /= 2
			bytesCount = to - from
		}
		if offset+bytesCount > uint64(len(doc)) {
			return "", invalid("DOC 文本位置超出文件范围。")
		}
		raw := doc[offset : offset+bytesCount]
		var text string
		var err error
		if compressed {
			text = wordCompressed(raw)
		} else {
			text, err = utf16LE(raw)
		}
		if err != nil {
			return "", err
		}
		out.WriteString(text)
		if out.Len() > legacyTextLimit {
			return "", invalid("DOC 文本过长，请拆分文件。")
		}
	}
	// Retain field results, omit instructions such as INCLUDETEXT/DDE. Nested
	// fields are handled explicitly; nothing in a field is evaluated.
	var clean strings.Builder
	fields := []bool{}
	hidden := 0
	for _, c := range out.String() {
		switch c {
		case 0x13:
			fields = append(fields, false)
			hidden++
			continue
		case 0x14:
			if len(fields) > 0 && !fields[len(fields)-1] {
				hidden--
				fields[len(fields)-1] = true
			}
			continue
		case 0x15:
			if len(fields) > 0 {
				if !fields[len(fields)-1] {
					hidden--
				}
				fields = fields[:len(fields)-1]
			}
			continue
		}
		if hidden > 0 {
			continue
		}
		switch c {
		case '\r', '\v', '\f':
			clean.WriteByte('\n')
		case 7:
			clean.WriteByte('\t')
		default:
			if c >= 32 || c == '\n' || c == '\t' {
				clean.WriteRune(c)
			}
		}
	}
	return strings.TrimSpace(clean.String()), nil
}
