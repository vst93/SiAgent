package platform

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type usageTotals struct {
	Input         int64   `json:"input"`
	Output        int64   `json:"output"`
	CacheRead     int64   `json:"cache_read"`
	CacheWrite    int64   `json:"cache_write"`
	Total         int64   `json:"total"`
	Cost          float64 `json:"cost"`
	Calls         int     `json:"calls"`
	ReportedCalls int     `json:"reported_calls"`
}

type usageBucket struct {
	Start      int64 `json:"start"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Total      int64 `json:"total"`
	Calls      int   `json:"calls"`
}

type usageModel struct {
	Model      string  `json:"model"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Total      int64   `json:"total"`
	Cost       float64 `json:"cost"`
	Calls      int     `json:"calls"`
}

type usageConversation struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	AgentID    string  `json:"agent_id"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Total      int64   `json:"total"`
	Cost       float64 `json:"cost"`
	Calls      int     `json:"calls"`
	LastSeen   int64   `json:"last_seen"`
}

type usageSummary struct {
	From          int64               `json:"from"`
	To            int64               `json:"to"`
	Bucket        string              `json:"bucket"`
	Totals        usageTotals         `json:"totals"`
	Series        []usageBucket       `json:"series"`
	Models        []usageModel        `json:"models"`
	Conversations []usageConversation `json:"conversations"`
}

func usageBucketStart(value time.Time, bucket string) time.Time {
	value = value.In(time.Local)
	year, month, day := value.Date()
	switch bucket {
	case "hour":
		return time.Date(year, month, day, value.Hour(), 0, 0, 0, time.Local)
	case "week":
		weekday := int(value.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		return time.Date(year, month, day-(weekday-1), 0, 0, 0, 0, time.Local)
	case "month":
		return time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
	default:
		return time.Date(year, month, day, 0, 0, 0, 0, time.Local)
	}
}

func usageNextBucket(value time.Time, bucket string) time.Time {
	switch bucket {
	case "hour":
		return value.Add(time.Hour)
	case "week":
		return value.AddDate(0, 0, 7)
	case "month":
		return value.AddDate(0, 1, 0)
	default:
		return value.AddDate(0, 0, 1)
	}
}

func usageBucketForSpan(span time.Duration) string {
	switch {
	case span <= 48*time.Hour:
		return "hour"
	case span <= 120*24*time.Hour:
		return "day"
	case span <= 730*24*time.Hour:
		return "week"
	default:
		return "month"
	}
}

func usageMillis(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func (s *Server) usageSummary(w http.ResponseWriter, r *http.Request) {
	to, ok := usageMillis(r.URL.Query().Get("to"))
	if !ok {
		fail(w, 400, "结束时间无效。")
		return
	}
	if to == 0 {
		to = time.Now().UnixMilli()
	}
	rawFrom := r.URL.Query().Get("from")
	from, ok := usageMillis(rawFrom)
	if !ok {
		fail(w, 400, "开始时间无效。")
		return
	}
	terminal := "('completed','failed','cancelled','interrupted')"
	if rawFrom == "" {
		from = usageBucketStart(time.UnixMilli(to).AddDate(0, 0, -29), "day").UnixMilli()
	} else if from == 0 {
		_ = s.store.db.QueryRow(`SELECT coalesce(min(u.created),0) FROM run_usage u JOIN runs r ON r.id=u.run_id WHERE r.status IN ` + terminal).Scan(&from)
		if from == 0 || from > to {
			from = usageBucketStart(time.UnixMilli(to).AddDate(0, 0, -29), "day").UnixMilli()
		}
	}
	if from >= to {
		fail(w, 400, "开始时间必须早于结束时间。")
		return
	}
	bucket := usageBucketForSpan(time.Duration(to-from) * time.Millisecond)

	rows, err := s.store.db.Query(`SELECT u.conversation_id,u.agent_id,u.model,u.input_tokens,u.output_tokens,u.cache_read_tokens,u.cache_write_tokens,u.total_tokens,u.cost,u.reported,u.created
		FROM run_usage u JOIN runs r ON r.id=u.run_id
		WHERE u.created>=? AND u.created<=? AND r.status IN `+terminal+`
		ORDER BY u.created`, from, to)
	if err != nil {
		internal(w)
		return
	}
	defer rows.Close()

	summary := usageSummary{From: from, To: to, Bucket: bucket, Series: []usageBucket{}, Models: []usageModel{}, Conversations: []usageConversation{}}
	series := map[int64]*usageBucket{}
	models := map[string]*usageModel{}
	conversations := map[string]*usageConversation{}
	for rows.Next() {
		var conversationID, agentID, model string
		var input, output, cacheRead, cacheWrite, total int64
		var cost float64
		var reported int
		var created int64
		if err := rows.Scan(&conversationID, &agentID, &model, &input, &output, &cacheRead, &cacheWrite, &total, &cost, &reported, &created); err != nil {
			internal(w)
			return
		}
		summary.Totals.Input += input
		summary.Totals.Output += output
		summary.Totals.CacheRead += cacheRead
		summary.Totals.CacheWrite += cacheWrite
		summary.Totals.Total += total
		summary.Totals.Cost += cost
		summary.Totals.Calls++
		if reported != 0 {
			summary.Totals.ReportedCalls++
		}

		start := usageBucketStart(time.UnixMilli(created), bucket).UnixMilli()
		item := series[start]
		if item == nil {
			item = &usageBucket{Start: start}
			series[start] = item
		}
		item.Input += input
		item.Output += output
		item.CacheRead += cacheRead
		item.CacheWrite += cacheWrite
		item.Total += total
		item.Calls++

		name := strings.TrimSpace(model)
		if name == "" {
			name = "未记录"
		}
		modelItem := models[name]
		if modelItem == nil {
			modelItem = &usageModel{Model: name}
			models[name] = modelItem
		}
		modelItem.Input += input
		modelItem.Output += output
		modelItem.CacheRead += cacheRead
		modelItem.CacheWrite += cacheWrite
		modelItem.Total += total
		modelItem.Cost += cost
		modelItem.Calls++

		conversation := conversations[conversationID]
		if conversation == nil {
			conversation = &usageConversation{ID: conversationID, AgentID: agentID, LastSeen: created}
			conversations[conversationID] = conversation
		}
		conversation.Input += input
		conversation.Output += output
		conversation.CacheRead += cacheRead
		conversation.CacheWrite += cacheWrite
		conversation.Total += total
		conversation.Cost += cost
		conversation.Calls++
		if created > conversation.LastSeen {
			conversation.LastSeen = created
		}
	}
	if err := rows.Err(); err != nil {
		internal(w)
		return
	}

	for start := usageBucketStart(time.UnixMilli(from), bucket); start.UnixMilli() <= to; start = usageNextBucket(start, bucket) {
		item := series[start.UnixMilli()]
		if item == nil {
			item = &usageBucket{Start: start.UnixMilli()}
		}
		summary.Series = append(summary.Series, *item)
	}

	for _, item := range models {
		summary.Models = append(summary.Models, *item)
	}
	sort.Slice(summary.Models, func(i, j int) bool {
		if summary.Models[i].Total == summary.Models[j].Total {
			return summary.Models[i].Model < summary.Models[j].Model
		}
		return summary.Models[i].Total > summary.Models[j].Total
	})

	for _, item := range conversations {
		summary.Conversations = append(summary.Conversations, *item)
	}
	sort.Slice(summary.Conversations, func(i, j int) bool {
		if summary.Conversations[i].Total == summary.Conversations[j].Total {
			return summary.Conversations[i].LastSeen > summary.Conversations[j].LastSeen
		}
		return summary.Conversations[i].Total > summary.Conversations[j].Total
	})
	if len(summary.Conversations) > 12 {
		summary.Conversations = summary.Conversations[:12]
	}
	if len(summary.Conversations) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(summary.Conversations)), ",")
		args := make([]any, 0, len(summary.Conversations))
		for _, item := range summary.Conversations {
			args = append(args, item.ID)
		}
		titleRows, err := s.store.db.Query("SELECT id,title,agent_id FROM conversations WHERE id IN ("+placeholders+")", args...)
		if err == nil {
			titles := map[string]string{}
			agents := map[string]string{}
			for titleRows.Next() {
				var id, title, agent string
				if titleRows.Scan(&id, &title, &agent) == nil {
					titles[id] = title
					agents[id] = agent
				}
			}
			titleRows.Close()
			for index := range summary.Conversations {
				if title := titles[summary.Conversations[index].ID]; title != "" {
					summary.Conversations[index].Title = title
				} else {
					summary.Conversations[index].Title = "未命名会话"
				}
				if agent := agents[summary.Conversations[index].ID]; agent != "" {
					summary.Conversations[index].AgentID = agent
				}
			}
		}
	}
	respond(w, 200, summary)
}
