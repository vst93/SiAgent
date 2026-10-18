package knowledge

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// EvalQuestionInput is one imported question. Answerable defaults to true when
// omitted so older exports keep working.
type EvalQuestionInput struct {
	Question            string   `json:"question"`
	ExpectedDocumentIDs []string `json:"expected_document_ids"`
	Tags                []string `json:"tags"`
	Answerable          *bool    `json:"answerable"`
	ExpectedAnswer      string   `json:"expected_answer"`
}

// ImportEvalQuestions validates and stores rows one by one, returning the
// number imported and a per-row error list. Valid rows are kept even when
// others fail, so a partially broken file is still usable.
func (c *Core) ImportEvalQuestions(ctx context.Context, baseID string, rows []EvalQuestionInput) (int, []string) {
	imported := 0
	failures := []string{}
	for index, row := range rows {
		answerable := true
		if row.Answerable != nil {
			answerable = *row.Answerable
		}
		if _, err := c.SaveEvalQuestionFull(ctx, "", baseID, row.Question, row.ExpectedDocumentIDs, row.Tags, answerable, row.ExpectedAnswer); err != nil {
			failures = append(failures, fmt.Sprintf("第 %d 行：%s", index+1, err.Error()))
			continue
		}
		imported++
	}
	return imported, failures
}

var evalCSVHeader = []string{"question", "expected_document_ids", "tags", "answerable", "expected_answer"}

// ParseEvalCSV reads a header-driven CSV. Document ids and tags use semicolons.
func ParseEvalCSV(reader io.Reader) ([]EvalQuestionInput, error) {
	parser := csv.NewReader(reader)
	parser.FieldsPerRecord = -1
	parser.LazyQuotes = true
	records, err := parser.ReadAll()
	if err != nil {
		return nil, errors.New("CSV 格式无效。")
	}
	if len(records) == 0 {
		return nil, errors.New("CSV 为空。")
	}
	index := map[string]int{}
	for position, name := range records[0] {
		index[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))] = position
	}
	if _, ok := index["question"]; !ok {
		return nil, errors.New("CSV 缺少 question 列。")
	}
	values := []EvalQuestionInput{}
	for _, record := range records[1:] {
		field := func(name string) string {
			position, ok := index[name]
			if !ok || position >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[position])
		}
		question := field("question")
		if question == "" {
			continue
		}
		row := EvalQuestionInput{Question: question, ExpectedAnswer: field("expected_answer")}
		row.ExpectedDocumentIDs = splitList(field("expected_document_ids"))
		row.Tags = splitList(field("tags"))
		if raw := field("answerable"); raw != "" {
			answerable := !strings.EqualFold(raw, "false") && raw != "0" && !strings.EqualFold(raw, "no")
			row.Answerable = &answerable
		}
		values = append(values, row)
	}
	return values, nil
}

func splitList(value string) []string {
	values := []string{}
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == '|' }) {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

// FormatEvalCSV exports questions in the same header order ParseEvalCSV reads.
func FormatEvalCSV(questions []EvalQuestion) string {
	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	_ = writer.Write(evalCSVHeader)
	for _, question := range questions {
		answerable := "true"
		if !question.Answerable {
			answerable = "false"
		}
		_ = writer.Write([]string{
			question.Question,
			strings.Join(question.ExpectedDocumentIDs, ";"),
			strings.Join(question.Tags, ";"),
			answerable,
			question.ExpectedAnswer,
		})
	}
	writer.Flush()
	return builder.String()
}

// ParseEvalJSON accepts the export format: an array of question objects.
func ParseEvalJSON(raw []byte) ([]EvalQuestionInput, error) {
	var values []EvalQuestionInput
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, errors.New("JSON 评测集格式无效，应为对象数组。")
	}
	if len(values) == 0 {
		return nil, errors.New("JSON 评测集为空。")
	}
	return values, nil
}
