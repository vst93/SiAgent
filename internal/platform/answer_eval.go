package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"siagent/internal/knowledge"
)

const answerPrompt = `你是严格的知识问答助手。只能依据给出的资料回答：
- 每条事实性结论后用 [n] 标注来源编号；
- 资料中没有答案时，只回答“资料中未找到相关内容”，不要猜测；
- 不要引用未提供的编号。`

var citationPattern = regexp.MustCompile(`\[(\d{1,3})\]`)
var numberPattern = regexp.MustCompile(`\d+(?:\.\d+)?`)
var abstainPattern = regexp.MustCompile(`未找到|没有找到|未提及|无法确定|资料中没有`)

// answerScore is the deterministic part of answer evaluation.
type answerScore struct {
	CitationPrecision float64 `json:"citation_precision"`
	Faithfulness      float64 `json:"faithfulness"`
	Abstained         bool    `json:"abstained"`
	ValidCitations    int     `json:"valid_citations"`
	InvalidCitations  int     `json:"invalid_citations"`
}

// scoreAnswer compares one model answer with the retrieved sources. It never
// calls a model, so it is unit-testable and stable across runs.
func scoreAnswer(answer string, sources []string) answerScore {
	matches := citationPattern.FindAllStringSubmatch(answer, -1)
	valid, invalid := 0, 0
	used := map[int]bool{}
	for _, match := range matches {
		index, err := strconv.Atoi(match[1])
		if err != nil || index < 1 || index > len(sources) {
			invalid++
			continue
		}
		valid++
		used[index] = true
	}
	score := answerScore{ValidCitations: valid, InvalidCitations: invalid, Abstained: abstainPattern.MatchString(answer)}
	total := valid + invalid
	if total > 0 {
		score.CitationPrecision = float64(valid) / float64(total)
	}
	// Faithfulness proxy: every number in the answer must appear in the cited
	// sources (or in any source when no citation was given).
	reference := strings.Builder{}
	if len(used) == 0 {
		for _, source := range sources {
			reference.WriteString(source)
			reference.WriteString("\n")
		}
	} else {
		for index := range used {
			reference.WriteString(sources[index-1])
			reference.WriteString("\n")
		}
	}
	numbers := numberPattern.FindAllString(citationPattern.ReplaceAllString(answer, ""), -1)
	if len(numbers) == 0 {
		score.Faithfulness = 1
	} else {
		grounded := 0
		for _, number := range numbers {
			if strings.Contains(reference.String(), number) {
				grounded++
			}
		}
		score.Faithfulness = float64(grounded) / float64(len(numbers))
	}
	return score
}

// runAnswerEval asks the active model to answer each eval question and scores
// the answers. It is opt-in, bounded, and stored as a separate run kind.
func (s *Server) runAnswerEval(w http.ResponseWriter, r *http.Request) {
	baseID := r.PathValue("id")
	questions, err := s.knowledge.EvalQuestions(r.Context(), baseID)
	if err != nil {
		knowledgeError(w, err)
		return
	}
	if len(questions) == 0 {
		fail(w, 400, "请先添加至少一个评测问题。")
		return
	}
	judge, err := s.newAnswerJudge()
	if err != nil {
		fail(w, 409, "回答评测需要先配置一个可用模型连接。")
		return
	}
	scope := knowledge.Scope{KnowledgeIDs: []string{baseID}}
	type caseResult struct {
		Question          string  `json:"question"`
		Answer            string  `json:"answer"`
		CitationPrecision float64 `json:"citation_precision"`
		Faithfulness      float64 `json:"faithfulness"`
		Abstained         bool    `json:"abstained"`
		Pass              bool    `json:"pass"`
	}
	cases := []caseResult{}
	var citation, faithful, abstain float64
	count := 0
	for _, question := range questions {
		result, err := s.knowledge.Search(r.Context(), scope, knowledge.SearchRequest{Query: question.Question, Limit: 5})
		if err != nil {
			continue
		}
		sources := []string{}
		labels := []string{}
		for index, hit := range result.Hits {
			sources = append(sources, hit.Text)
			labels = append(labels, "["+strconv.Itoa(index+1)+"] "+hit.Title+"："+hit.Text)
		}
		answer, err := judge(r.Context(), question.Question, labels)
		if err != nil {
			continue
		}
		score := scoreAnswer(answer, sources)
		pass := false
		if question.Answerable {
			pass = score.CitationPrecision >= 0.99 && score.Faithfulness >= 0.99 && !score.Abstained
		} else {
			pass = score.Abstained
		}
		cases = append(cases, caseResult{Question: question.Question, Answer: answer, CitationPrecision: score.CitationPrecision, Faithfulness: score.Faithfulness, Abstained: score.Abstained, Pass: pass})
		citation += score.CitationPrecision
		faithful += score.Faithfulness
		if score.Abstained {
			abstain++
		}
		count++
	}
	if count == 0 {
		fail(w, 503, "模型未返回可用回答，请检查模型连接后重试。")
		return
	}
	metrics := knowledge.EvalMetrics{Questions: count}
	metrics.CitationPrecision = round4(citation / float64(count))
	metrics.Faithfulness = round4(faithful / float64(count))
	// Abstention accuracy compares the abstain decision with the label.
	correct := 0
	for index, item := range cases {
		if questions[index].Answerable != item.Abstained {
			correct++
		}
	}
	metrics.AbstentionAcc = round4(float64(correct) / float64(count))
	run, err := s.knowledge.SaveAnswerEvalRun(r.Context(), baseID, metrics, cases)
	if err != nil {
		internal(w)
		return
	}
	respond(w, 201, run)
}

func round4(value float64) float64 {
	return float64(int64(value*10000+0.5)) / 10000
}

// newAnswerJudge returns a function that answers one question with the first
// active model connection.
func (s *Server) newAnswerJudge() (func(context.Context, string, []string) (string, error), error) {
	reranker := &modelReranker{store: s.store}
	model, err := reranker.activeModel()
	if err != nil {
		return nil, err
	}
	endpoint, err := rerankEndpoint(model)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, question string, sources []string) (string, error) {
		user := "问题：" + question + "\n资料：\n" + strings.Join(sources, "\n")
		payload, _ := json.Marshal(map[string]any{
			"model":       model.Name,
			"temperature": 0,
			"max_tokens":  500,
			"messages": []map[string]string{
				{"role": "system", "content": answerPrompt},
				{"role": "user", "content": user},
			},
		})
		requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		request.Header.Set("Content-Type", "application/json")
		if model.APIKey != "" {
			request.Header.Set("Authorization", "Bearer "+model.APIKey)
		}
		client := &http.Client{Timeout: 32 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", errors.New("model status")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
		if err != nil {
			return "", err
		}
		var body struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Choices) == 0 {
			return "", errors.New("model response invalid")
		}
		return strings.TrimSpace(body.Choices[0].Message.Content), nil
	}, nil
}
