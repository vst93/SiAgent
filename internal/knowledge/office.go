package knowledge

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// Office attachments are read as bounded XML packages. Nothing is unpacked to
// disk, executed, or fetched from external relationships.
func readOffice(data []byte) (map[string][]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, invalid("Office 文件损坏或已加密，请提供未加密的 DOCX/PPTX/XLSX 文件。")
	}
	if len(z.File) > 4096 {
		return nil, invalid("Office 文件内部条目过多，请拆分文件。")
	}
	files := make(map[string][]byte)
	var total uint64
	for _, f := range z.File {
		name := strings.ToLower(f.Name)
		switch path.Ext(name) {
		case ".exe", ".dll", ".com", ".bat", ".cmd", ".ps1", ".sh", ".vbs", ".js", ".msi", ".scr":
			return nil, invalid("Office 文件包含可执行文件或脚本。")
		}
		if strings.Contains(name, "vbaproject") || strings.Contains(name, "/embeddings/") || strings.Contains(name, "/activex/") {
			return nil, invalid("不支持包含宏、嵌入对象或 ActiveX 的 Office 文件。")
		}
		if f.UncompressedSize64 > 16<<20 || f.UncompressedSize64 > (64<<20)-total {
			return nil, invalid("Office 解压大小超过上限，请拆分文件。")
		}
		total += f.UncompressedSize64
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".rels") {
			continue
		}
		if _, exists := files[f.Name]; exists {
			return nil, invalid("Office 文件包含重复条目。")
		}
		r, err := f.Open()
		if err != nil {
			return nil, invalid("Office 文件无法读取。")
		}
		b, err := io.ReadAll(io.LimitReader(r, 16<<20+1))
		r.Close()
		if err != nil || len(b) > 16<<20 {
			return nil, invalid("Office 文件内容损坏或过大。")
		}
		files[f.Name] = b
	}
	// Macro-enabled content can be disguised with an ordinary Office extension.
	types := strings.ToLower(string(files["[Content_Types].xml"]))
	if strings.Contains(types, "macroenabled") || strings.Contains(types, "vbaproject") {
		return nil, invalid("不支持含宏的 Office 文件。")
	}
	return files, nil
}

type officeRelationship struct {
	ID     string `xml:"Id,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
}

func officeRelations(files map[string][]byte, name string) (map[string]string, error) {
	var rels struct {
		Items []officeRelationship `xml:"Relationship"`
	}
	if err := xml.Unmarshal(files[name], &rels); err != nil {
		return nil, invalid("Office 文件关系无效。")
	}
	result := map[string]string{}
	base := path.Dir(path.Dir(name))
	for _, rel := range rels.Items {
		if rel.Mode == "External" {
			continue
		}
		target := path.Clean(path.Join(base, rel.Target))
		if strings.HasPrefix(rel.Target, "/") {
			target = strings.TrimPrefix(path.Clean(rel.Target), "/")
		}
		result[rel.ID] = target
	}
	return result, nil
}

// officeText preserves paragraph boundaries and rich-text runs, including
// text in slide tables. Images, charts and drawing geometry are not OCR'd.
func officeText(data []byte) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var b strings.Builder
	for {
		token, err := d.Token()
		if err == io.EOF {
			return strings.TrimSpace(b.String()), nil
		}
		if err != nil {
			return "", invalid("Office XML 内容无效。")
		}
		switch v := token.(type) {
		case xml.StartElement:
			if v.Name.Local == "t" {
				var text string
				if err := d.DecodeElement(&text, &v); err != nil {
					return "", invalid("Office 文本无效。")
				}
				b.WriteString(text)
			}
		case xml.EndElement:
			if v.Name.Local == "p" {
				b.WriteByte('\n')
			}
		}
	}
}

func extractOffice(ctx context.Context, files map[string][]byte, kind string) (string, error) {
	var output strings.Builder
	appendText := func(text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if output.Len()+len(text) > 4*300000 {
			return invalid("文件提取文本过多，请拆分文件。")
		}
		output.WriteString(text)
		return nil
	}
	if kind == "pptx" {
		var presentation struct {
			Slides []struct {
				RID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			} `xml:"sldIdLst>sldId"`
		}
		if err := xml.Unmarshal(files["ppt/presentation.xml"], &presentation); err != nil {
			return "", invalid("PPTX 缺少有效的演示文稿。")
		}
		rels, err := officeRelations(files, "ppt/_rels/presentation.xml.rels")
		if err != nil {
			return "", err
		}
		for i, slide := range presentation.Slides {
			text, err := officeText(files[rels[slide.RID]])
			if err != nil || len(files[rels[slide.RID]]) == 0 {
				return "", invalid("PPTX 幻灯片内容缺失或损坏。")
			}
			if text != "" {
				if err := appendText(fmt.Sprintf("[幻灯片 %d]\n%s\n\n", i+1, text)); err != nil {
					return "", err
				}
			}
		}
	} else {
		var workbook struct {
			Sheets []struct {
				Name string `xml:"name,attr"`
				RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			} `xml:"sheets>sheet"`
		}
		if err := xml.Unmarshal(files["xl/workbook.xml"], &workbook); err != nil {
			return "", invalid("XLSX 缺少有效的工作簿。")
		}
		rels, err := officeRelations(files, "xl/_rels/workbook.xml.rels")
		if err != nil {
			return "", err
		}
		var shared struct {
			Items []struct {
				Inner string `xml:",innerxml"`
			} `xml:"si"`
		}
		if data := files["xl/sharedStrings.xml"]; len(data) > 0 {
			if err := xml.Unmarshal(data, &shared); err != nil {
				return "", invalid("XLSX 共享文本无效。")
			}
		}
		stringsTable := make([]string, len(shared.Items))
		for i, item := range shared.Items {
			text, err := officeText([]byte("<si>" + item.Inner + "</si>"))
			if err != nil {
				return "", err
			}
			stringsTable[i] = text
		}
		for _, sheet := range workbook.Sheets {
			var worksheet struct {
				Rows []struct {
					Cells []struct {
						Ref     string `xml:"r,attr"`
						Type    string `xml:"t,attr"`
						Value   string `xml:"v"`
						Formula string `xml:"f"`
						Inline  struct {
							Inner string `xml:",innerxml"`
						} `xml:"is"`
					} `xml:"c"`
				} `xml:"sheetData>row"`
			}
			if err := xml.Unmarshal(files[rels[sheet.RID]], &worksheet); err != nil {
				return "", invalid("XLSX 工作表内容无效。")
			}
			var rows strings.Builder
			for _, row := range worksheet.Rows {
				for _, cell := range row.Cells {
					value := cell.Value
					switch cell.Type {
					case "s":
						index, err := strconv.Atoi(value)
						if err != nil || index < 0 || index >= len(stringsTable) {
							return "", invalid("XLSX 文本索引无效。")
						}
						value = stringsTable[index]
					case "inlineStr":
						value, err = officeText([]byte("<is>" + cell.Inline.Inner + "</is>"))
						if err != nil {
							return "", err
						}
					case "b":
						if value == "1" {
							value = "TRUE"
						} else {
							value = "FALSE"
						}
					}
					if value == "" && cell.Formula != "" {
						value = "[公式无缓存值，请在 Excel 中重新计算并保存]"
					}
					if value != "" {
						rows.WriteString(cell.Ref + "=" + value + "\t")
					}
					if rows.Len() > 4*300000 {
						return "", invalid("工作表文本过多，请拆分文件。")
					}
				}
				rows.WriteByte('\n')
			}
			if strings.TrimSpace(rows.String()) != "" {
				if err := appendText("[工作表 " + sheet.Name + "]\n" + rows.String() + "\n"); err != nil {
					return "", err
				}
			}
		}
	}
	return output.String(), nil
}
