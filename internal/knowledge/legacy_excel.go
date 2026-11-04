package knowledge

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type biffRecord struct {
	id     uint16
	offset int
	data   []byte
}
type biffStrings struct {
	parts        [][]byte
	part, offset int
}

func (s *biffStrings) raw(n int) ([]byte, error) {
	if n < 0 || n > legacyTextLimit {
		return nil, invalid("XLS 字符串长度无效。")
	}
	out := make([]byte, 0, n)
	for len(out) < n {
		for s.part < len(s.parts) && s.offset == len(s.parts[s.part]) {
			s.part++
			s.offset = 0
		}
		if s.part == len(s.parts) {
			return nil, invalid("XLS 字符串被截断。")
		}
		count := min(n-len(out), len(s.parts[s.part])-s.offset)
		out = append(out, s.parts[s.part][s.offset:s.offset+count]...)
		s.offset += count
	}
	return out, nil
}
func (s *biffStrings) text() (string, error) {
	header, err := s.raw(3)
	if err != nil {
		return "", err
	}
	count := int(little.Uint16(header))
	flags := header[2]
	runs, extended := 0, 0
	if flags&8 != 0 {
		b, err := s.raw(2)
		if err != nil {
			return "", err
		}
		runs = int(little.Uint16(b))
	}
	if flags&4 != 0 {
		b, err := s.raw(4)
		if err != nil {
			return "", err
		}
		extended = int(little.Uint32(b))
	}
	wide := flags&1 != 0
	chars := make([]byte, 0, count*2)
	for i := 0; i < count; i++ {
		if s.part >= len(s.parts) {
			return "", invalid("XLS 文本被截断。")
		}
		if s.offset == len(s.parts[s.part]) {
			s.part++
			s.offset = 0
			flag, err := s.raw(1)
			if err != nil {
				return "", err
			}
			wide = flag[0]&1 != 0
		}
		size := 1
		if wide {
			size = 2
		}
		if s.part >= len(s.parts) || len(s.parts[s.part])-s.offset < size {
			return "", invalid("XLS 字符被截断。")
		}
		b, err := s.raw(size)
		if err != nil {
			return "", err
		}
		chars = append(chars, b[0])
		if wide {
			chars = append(chars, b[1])
		} else {
			chars = append(chars, 0)
		}
	}
	if _, err := s.raw(runs * 4); err != nil {
		return "", err
	}
	if _, err := s.raw(extended); err != nil {
		return "", err
	}
	return utf16LE(chars)
}
func excelCell(row, col int) string {
	name := ""
	for col++; col > 0; col = (col - 1) / 26 {
		name = string(rune('A'+(col-1)%26)) + name
	}
	return name + strconv.Itoa(row+1)
}
func excelRK(v uint32) string {
	var number float64
	if v&2 != 0 {
		number = float64(int32(v) >> 2)
	} else {
		number = math.Float64frombits(uint64(v&0xfffffffc) << 32)
	}
	if v&1 != 0 {
		number /= 100
	}
	return strconv.FormatFloat(number, 'g', -1, 64)
}
func legacyExcel(ctx context.Context, streams map[string][]byte) (string, error) {
	book := streams["Workbook"]
	if len(book) == 0 {
		book = streams["Book"]
	}
	if len(book) < 8 || little.Uint16(book) != 0x809 || little.Uint16(book[4:]) != 0x600 {
		return "", invalid("XLS 需要 Excel 97–2003 BIFF8 格式；更早的工作簿请另存为 XLS/XLSX。")
	}
	records := []biffRecord{}
	byOffset := map[int]int{}
	for offset := 0; offset < len(book); {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(book)-offset < 4 {
			break
		}
		id := little.Uint16(book[offset:])
		size := int(little.Uint16(book[offset+2:]))
		if size > len(book)-offset-4 {
			return "", invalid("XLS 记录被截断。")
		}
		if id == 0 && size == 0 {
			break
		}
		if id == 0x2f {
			return "", invalid("不支持加密 XLS 文件。")
		}
		if id == 0xd3 {
			return "", invalid("不支持含宏的 XLS 文件。")
		}
		if len(records) > 1000000 {
			return "", invalid("XLS 记录过多，请拆分文件。")
		}
		byOffset[offset] = len(records)
		records = append(records, biffRecord{id, offset, book[offset+4 : offset+4+size]})
		offset += 4 + size
	}
	type sheetInfo struct {
		name   string
		offset int
	}
	sheets := []sheetInfo{}
	shared := []string{}
	stringBytes := 0
	for index, r := range records {
		if r.id == 0xa {
			break
		}
		switch r.id {
		case 0x85:
			if len(r.data) < 8 {
				return "", invalid("XLS 工作表信息无效。")
			}
			if r.data[5] == 1 || r.data[5] == 6 {
				return "", invalid("不支持 Excel 4.0 宏表或 VBA 模块。")
			}
			if r.data[5] != 0 {
				continue
			}
			count := int(r.data[6])
			var name string
			var err error
			if r.data[7]&1 != 0 {
				if len(r.data) < 8+count*2 {
					return "", invalid("XLS 工作表名损坏。")
				}
				name, err = utf16LE(r.data[8 : 8+count*2])
			} else {
				if len(r.data) < 8+count {
					return "", invalid("XLS 工作表名损坏。")
				}
				name = compressedUnicode(r.data[8 : 8+count])
			}
			if err != nil {
				return "", err
			}
			sheets = append(sheets, sheetInfo{name, int(little.Uint32(r.data))})
		case 0xfc:
			if len(r.data) < 8 {
				return "", invalid("XLS 共享字符串表无效。")
			}
			count := int(little.Uint32(r.data[4:]))
			if count > 100000 {
				return "", invalid("XLS 共享字符串过多。")
			}
			reader := biffStrings{parts: [][]byte{r.data[8:]}}
			for j := index + 1; j < len(records) && records[j].id == 0x3c; j++ {
				reader.parts = append(reader.parts, records[j].data)
			}
			for i := 0; i < count; i++ {
				text, err := reader.text()
				if err != nil {
					return "", err
				}
				stringBytes += len(text)
				if stringBytes > legacyTextLimit {
					return "", invalid("XLS 文本过长，请拆分文件。")
				}
				shared = append(shared, text)
			}
		}
	}
	var out strings.Builder
	for _, sheet := range sheets {
		first, ok := byOffset[sheet.offset]
		if !ok || records[first].id != 0x809 {
			return "", invalid("XLS 工作表位置无效。")
		}
		cells := map[int]string{}
		total := 0
		pendingString := -1
		put := func(row, col int, value string) error {
			if col > 255 {
				return invalid("XLS 单元格位置无效。")
			}
			if value == "" {
				return nil
			}
			total += len(value) + 16
			if total > legacyTextLimit {
				return invalid("XLS 文本过长，请拆分文件。")
			}
			cells[row*256+col] = value
			return nil
		}
		for recordIndex, r := range records[first+1:] {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if r.id == 0xa {
				break
			}
			if r.id == 0x207 && pendingString >= 0 {
				reader := biffStrings{parts: [][]byte{r.data}}
				for j := first + recordIndex + 2; j < len(records) && records[j].id == 0x3c; j++ {
					reader.parts = append(reader.parts, records[j].data)
				}
				text, err := reader.text()
				if err != nil {
					return "", err
				}
				if err = put(pendingString/256, pendingString%256, text); err != nil {
					return "", err
				}
				pendingString = -1
				continue
			}
			switch r.id {
			case 0xfd, 0x203, 0x27e, 0xbd, 0x205, 0x204, 6:
			default:
				continue
			}
			if len(r.data) < 6 {
				return "", invalid("XLS 单元格记录无效。")
			}
			row, col := int(little.Uint16(r.data)), int(little.Uint16(r.data[2:]))
			value := ""
			switch r.id {
			case 0xfd:
				if len(r.data) < 10 {
					return "", invalid("XLS 文本索引无效。")
				}
				i := int(little.Uint32(r.data[6:]))
				if i >= len(shared) {
					return "", invalid("XLS 文本索引超出范围。")
				}
				value = shared[i]
			case 0x203:
				if len(r.data) < 14 {
					return "", invalid("XLS 数值记录无效。")
				}
				value = strconv.FormatFloat(math.Float64frombits(little.Uint64(r.data[6:])), 'g', -1, 64)
			case 0x27e:
				if len(r.data) < 10 {
					return "", invalid("XLS RK 数值记录无效。")
				}
				value = excelRK(little.Uint32(r.data[6:]))
			case 0xbd:
				if (len(r.data)-6)%6 != 0 {
					return "", invalid("XLS 多单元格记录无效。")
				}
				count := (len(r.data) - 6) / 6
				if col+count-1 != int(little.Uint16(r.data[len(r.data)-2:])) {
					return "", invalid("XLS 单元格范围无效。")
				}
				for i := 0; i < count; i++ {
					if err := put(row, col+i, excelRK(little.Uint32(r.data[6+i*6:]))); err != nil {
						return "", err
					}
				}
				continue
			case 0x205:
				if len(r.data) < 8 {
					return "", invalid("XLS 布尔值记录无效。")
				}
				if r.data[7] != 0 {
					value = fmt.Sprintf("[Excel 错误 %d]", r.data[6])
				} else {
					value = "FALSE"
					if r.data[6] != 0 {
						value = "TRUE"
					}
				}
			case 0x204:
				reader := biffStrings{parts: [][]byte{r.data[6:]}}
				var err error
				value, err = reader.text()
				if err != nil {
					return "", err
				}
			case 6:
				if len(r.data) < 20 {
					return "", invalid("XLS 公式缓存无效。")
				}
				if little.Uint16(r.data[12:]) == 0xffff {
					switch r.data[6] {
					case 0:
						pendingString = row*256 + col
						continue
					case 1:
						value = "FALSE"
						if r.data[8] != 0 {
							value = "TRUE"
						}
					case 2:
						value = fmt.Sprintf("[Excel 错误 %d]", r.data[8])
					case 3:
						continue
					default:
						return "", invalid("XLS 公式缓存类型无效。")
					}
				} else {
					value = strconv.FormatFloat(math.Float64frombits(little.Uint64(r.data[6:])), 'g', -1, 64)
				}
			}
			if err := put(row, col, value); err != nil {
				return "", err
			}
		}
		if pendingString >= 0 {
			return "", invalid("XLS 公式文本缓存缺失。")
		}
		if len(cells) == 0 {
			continue
		}
		keys := make([]int, 0, len(cells))
		for key := range cells {
			keys = append(keys, key)
		}
		sort.Ints(keys)
		out.WriteString("[工作表 " + sheet.name + "]\n")
		previousRow := -1
		for _, key := range keys {
			row, col := key/256, key%256
			if row != previousRow && previousRow >= 0 {
				out.WriteByte('\n')
			}
			out.WriteString(excelCell(row, col) + "=" + cells[key] + "\t")
			previousRow = row
		}
		out.WriteString("\n\n")
		if out.Len() > legacyTextLimit {
			return "", invalid("XLS 文本过长，请拆分文件。")
		}
	}
	return out.String(), nil
}
