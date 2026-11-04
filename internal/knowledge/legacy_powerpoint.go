package knowledge

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

type pptRecord struct {
	options, kind uint16
	body          []byte
	end           int
}

func pptAt(data []byte, offset int) (pptRecord, error) {
	if offset < 0 || offset > len(data)-8 {
		return pptRecord{}, invalid("PPT 记录位置无效。")
	}
	size := uint64(little.Uint32(data[offset+4:]))
	if size > uint64(len(data)-offset-8) {
		return pptRecord{}, invalid("PPT 记录被截断。")
	}
	end := offset + 8 + int(size)
	return pptRecord{little.Uint16(data[offset:]), little.Uint16(data[offset+2:]), data[offset+8 : end], end}, nil
}
func walkPPT(ctx context.Context, data []byte, depth int, visit func(pptRecord) error) error {
	if depth > 64 {
		return invalid("PPT 容器嵌套过深。")
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(data)-offset < 8 {
			if bytes.Equal(data[offset:], make([]byte, len(data)-offset)) {
				return nil
			}
			return invalid("PPT 容器尾部损坏。")
		}
		r, err := pptAt(data, offset)
		if err != nil {
			return err
		}
		if err = visit(r); err != nil {
			return err
		}
		// OfficeArtClientTextbox is an Escher atom containing PPT text records.
		if r.options&15 == 15 || r.kind == 0xf00d {
			if err = walkPPT(ctx, r.body, depth+1, visit); err != nil {
				return err
			}
		}
		offset = r.end
	}
	return nil
}
func legacyPowerPoint(ctx context.Context, streams map[string][]byte) (string, error) {
	data, user := streams["PowerPoint Document"], streams["Current User"]
	if len(data) == 0 || len(user) < 20 {
		return "", invalid("PPT 缺少有效的演示文稿或当前编辑记录。")
	}
	if little.Uint32(user[12:]) != 0xe391c05f {
		return "", invalid("不支持加密或无效的 PPT 文件。")
	}
	// Scan every record for active content, including incrementally saved objects.
	if err := walkPPT(ctx, data, 0, func(r pptRecord) error {
		switch r.kind {
		case 4113, 4035:
			return invalid("不支持含嵌入对象的 PPT 文件。")
		case 12052:
			return invalid("不支持加密 PPT 文件。")
		case 1024:
			if len(r.body) < 8 || little.Uint32(r.body[4:]) != 0 {
				return invalid("不支持含宏的 PPT 文件。")
			}
		}
		return nil
	}); err != nil {
		return "", err
	}
	// Newest persist pointers win. Scanning all text in the stream would expose
	// deleted slides from earlier saves, so follow only the current document.
	pointers := map[uint32]int{}
	seen := map[int]bool{}
	editOffset := int(little.Uint32(user[16:]))
	var documentID uint32
	for editOffset != 0 {
		if seen[editOffset] || len(seen) > 4096 {
			return "", invalid("PPT 编辑链循环或过长。")
		}
		seen[editOffset] = true
		edit, err := pptAt(data, editOffset)
		if err != nil || edit.kind != 4085 || len(edit.body) < 28 {
			return "", invalid("PPT 编辑记录无效。")
		}
		if len(edit.body) > 28 {
			return "", invalid("不支持带加密会话的 PPT 文件。")
		}
		if documentID == 0 {
			documentID = little.Uint32(edit.body[16:])
		}
		persist, err := pptAt(data, int(little.Uint32(edit.body[12:])))
		if err != nil || (persist.kind != 6001 && persist.kind != 6002) {
			return "", invalid("PPT 持久化指针表无效。")
		}
		for p := 0; p < len(persist.body); {
			if len(persist.body)-p < 4 {
				return "", invalid("PPT 指针表被截断。")
			}
			entry := little.Uint32(persist.body[p:])
			p += 4
			id, count := entry&0xfffff, int(entry>>20)
			if count == 0 || count > (len(persist.body)-p)/4 {
				return "", invalid("PPT 指针表范围无效。")
			}
			for i := 0; i < count; i++ {
				key := id + uint32(i)
				if _, exists := pointers[key]; !exists {
					pointers[key] = int(little.Uint32(persist.body[p:]))
				}
				p += 4
			}
			if len(pointers) > 100000 {
				return "", invalid("PPT 对象过多。")
			}
		}
		editOffset = int(little.Uint32(edit.body[8:]))
	}
	documentOffset, ok := pointers[documentID]
	if !ok {
		return "", invalid("PPT 当前文档引用缺失。")
	}
	document, err := pptAt(data, documentOffset)
	if err != nil || document.kind != 1000 {
		return "", invalid("PPT 当前文档记录无效。")
	}
	type slideText struct {
		id    uint32
		parts []string
	}
	slides := []slideText{}
	// The instance-zero SlideListWithText is the ordered slide list; other
	// instances contain master/notes lists and must not become user slides.
	for offset := 0; offset < len(document.body); {
		r, err := pptAt(document.body, offset)
		if err != nil {
			return "", err
		}
		offset = r.end
		if r.kind != 4080 || r.options>>4 != 0 {
			continue
		}
		for p := 0; p < len(r.body); {
			atom, err := pptAt(r.body, p)
			if err != nil {
				return "", err
			}
			p = atom.end
			if atom.kind == 1011 {
				if len(atom.body) < 4 {
					return "", invalid("PPT 幻灯片引用无效。")
				}
				slides = append(slides, slideText{id: little.Uint32(atom.body)})
				continue
			}
			if len(slides) > 0 {
				value, err := pptText(atom)
				if err != nil {
					return "", err
				}
				if value != "" {
					slides[len(slides)-1].parts = append(slides[len(slides)-1].parts, value)
				}
			}
		}
	}
	var out strings.Builder
	for index, slide := range slides {
		slideOffset, ok := pointers[slide.id]
		if !ok {
			return "", invalid("PPT 幻灯片内容缺失。")
		}
		record, err := pptAt(data, slideOffset)
		if err != nil || record.kind != 1006 {
			return "", invalid("PPT 幻灯片记录无效。")
		}
		parts := slide.parts
		if err := walkPPT(ctx, record.body, 0, func(r pptRecord) error {
			value, err := pptText(r)
			if value != "" {
				parts = append(parts, value)
			}
			return err
		}); err != nil {
			return "", err
		}
		used := map[string]bool{}
		var text strings.Builder
		for _, part := range parts {
			if !used[part] {
				text.WriteString(part)
				text.WriteByte('\n')
				used[part] = true
			}
		}
		if text.Len() > 0 {
			fmt.Fprintf(&out, "[幻灯片 %d]\n%s\n", index+1, text.String())
		}
		if out.Len() > legacyTextLimit {
			return "", invalid("PPT 文本过长，请拆分文件。")
		}
	}
	return out.String(), nil
}
func pptText(r pptRecord) (string, error) {
	var text string
	var err error
	switch r.kind {
	case 4000:
		text, err = utf16LE(r.body)
	case 4008:
		text = compressedUnicode(r.body)
	}
	text = strings.NewReplacer("\r", "\n", "\v", "\n", "\x00", "").Replace(text)
	return strings.TrimSpace(text), err
}
