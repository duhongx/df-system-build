package componentprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"df-build-server/internal/inspection"
)

type rabbitRate struct {
	Rate *float64 `json:"rate"`
}
type rabbitQueue struct {
	Name      string `json:"name"`
	VHost     string `json:"vhost"`
	State     string `json:"state"`
	Ready     *int64 `json:"messages_ready"`
	Unacked   *int64 `json:"messages_unacknowledged"`
	Consumers *int64 `json:"consumers"`
	Stats     struct {
		Publish rabbitRate `json:"publish_details"`
		Deliver rabbitRate `json:"deliver_get_details"`
		Ack     rabbitRate `json:"ack_details"`
	} `json:"message_stats"`
}

func rabbitQueues(ctx context.Context, c Config, expected int) inspection.Observation {
	fail := func(err error) inspection.Observation {
		return Failed("rabbitmq-queues", "RabbitMQ 队列与积压", err)
	}
	var queues []rabbitQueue
	pageTotal := -1
	seen := map[string]bool{}
	for page := 1; ; page++ {
		if page > 100 {
			return fail(fmt.Errorf("队列超过 10000 个，未取得完整列表"))
		}
		var raw json.RawMessage
		endpoint := fmt.Sprintf("/api/queues?pagination=true&page=%d&page_size=100&columns=name,vhost,state,messages_ready,messages_unacknowledged,consumers,message_stats.publish_details.rate,message_stats.deliver_get_details.rate,message_stats.ack_details.rate", page)
		if err := getJSON(ctx, c, endpoint, &raw); err != nil {
			return fail(err)
		}
		raw = bytes.TrimSpace(raw)
		var batch []rabbitQueue
		last := true
		if len(raw) > 0 && raw[0] == '[' {
			if json.Unmarshal(raw, &batch) != nil {
				return fail(fmt.Errorf("队列列表格式无效"))
			}
		} else {
			var result struct {
				Items []rabbitQueue `json:"items"`
				Page  int           `json:"page"`
				Pages int           `json:"page_count"`
				Total *int          `json:"total_count"`
			}
			if json.Unmarshal(raw, &result) != nil || result.Page != page || result.Pages < 0 || result.Total == nil || *result.Total < 0 || (result.Pages == 0 && *result.Total > 0) || (len(result.Items) == 0 && *result.Total > 0) {
				return fail(fmt.Errorf("分页队列响应不完整"))
			}
			batch = result.Items
			if pageTotal >= 0 && pageTotal != *result.Total {
				return fail(fmt.Errorf("分页期间队列数量变化，未确认完整性"))
			}
			pageTotal = *result.Total
			last = page >= result.Pages
		}
		for _, q := range batch {
			key := q.VHost + "\x00" + q.Name
			if q.Name == "" || q.VHost == "" || seen[key] {
				return fail(fmt.Errorf("队列列表包含空名称或重复对象，采样范围可能发生变化"))
			}
			seen[key] = true
			queues = append(queues, q)
		}
		if len(queues) > 10000 {
			return fail(fmt.Errorf("队列列表超过 10000 个，未确认完整性"))
		}
		if last {
			if pageTotal >= 0 && len(queues) != pageTotal {
				return fail(fmt.Errorf("队列分页未返回完整结果"))
			}
			break
		}
	}
	o := Observation("rabbitmq-queues", "RabbitMQ 队列与积压", fmt.Sprintf("共 %d 个队列", len(queues)), "NORMAL")
	o.Measurements = map[string]float64{"queueCount": float64(len(queues))}
	t := inspection.Table{Columns: []string{"VHost", "队列", "状态", "待消费", "未确认", "消费者", "生产/秒", "投递/秒", "确认/秒", "待消费变化", "判断"}}
	sort.Slice(queues, func(i, j int) bool {
		a, b := int64(-1), int64(-1)
		if queues[i].Ready != nil {
			a = *queues[i].Ready
		}
		if queues[j].Ready != nil {
			b = *queues[j].Ready
		}
		if a != b {
			return a > b
		}
		return queues[i].VHost+queues[i].Name < queues[j].VHost+queues[j].Name
	})
	backlog, noConsumer := 0, 0
	valid := true
	var publishing, delivering, acknowledging float64
	ratesComplete := true
	for _, q := range queues {
		judgment := "无待处理消息"
		if q.Ready == nil || q.Unacked == nil || q.Consumers == nil || *q.Ready < 0 || *q.Unacked < 0 || *q.Consumers < 0 {
			valid = false
			judgment = "数据不完整"
		} else {
			if *q.Unacked > 0 {
				judgment = "消息处理中／待确认"
			}
			if *q.Ready > 0 {
				backlog++
				judgment = "存在待消费积压"
				if *q.Consumers == 0 {
					noConsumer++
					judgment = "待消费且无消费者"
					o.Status = "WARNING"
				}
			}
		}
		if q.State == "down" || q.State == "crashed" || q.State == "stopped" {
			o.Status = "WARNING"
			judgment = "队列不可用"
		}
		if q.Stats.Publish.Rate == nil || q.Stats.Deliver.Rate == nil || q.Stats.Ack.Rate == nil || *q.Stats.Publish.Rate < 0 || *q.Stats.Deliver.Rate < 0 || *q.Stats.Ack.Rate < 0 {
			ratesComplete = false
		} else {
			publishing += *q.Stats.Publish.Rate
			delivering += *q.Stats.Deliver.Rate
			acknowledging += *q.Stats.Ack.Rate
		}
		t.Rows = append(t.Rows, []string{inspection.Preview(q.VHost, 256), inspection.Preview(q.Name, 256), q.State, rabbitNumber(q.Ready), rabbitNumber(q.Unacked), rabbitNumber(q.Consumers), rabbitRateText(q.Stats.Publish.Rate), rabbitRateText(q.Stats.Deliver.Rate), rabbitRateText(q.Stats.Ack.Rate), "—", judgment})
	}
	if valid {
		o.Measurements["backlogQueues"] = float64(backlog)
		o.Measurements["noConsumerQueues"] = float64(noConsumer)
	}
	if ratesComplete {
		o.Measurements["publishRate"] = publishing
		o.Measurements["deliverRate"] = delivering
		o.Measurements["ackRate"] = acknowledging
	}
	if len(queues) != expected || !valid {
		o.Status = "UNKNOWN"
		o.Reason = fmt.Sprintf("读取 %d 个队列，汇总 %d 个；队列范围变化、权限或部分统计缺失，无法确认全部队列状态", len(queues), expected)
	} else if noConsumer > 0 {
		o.Reason = fmt.Sprintf("%d 个队列存在待消费消息且无消费者", noConsumer)
	}
	if len(queues) == 0 {
		o.Value = "暂无队列"
	}
	o.Tables = []inspection.Table{t}
	return o
}

func rabbitNumber(value *int64) string {
	if value == nil || *value < 0 {
		return "—"
	}
	return strconv.FormatInt(*value, 10)
}
func rabbitRateText(value *float64) string {
	if value == nil || *value < 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f", *value)
}

// Compare only consecutive snapshots of the same source. New/missing queues have
// no fabricated baseline; a positive change is not automatically an outage.
func CompareRabbitQueues(current, previous []inspection.Observation) {
	prior := map[string]int64{}
	for _, check := range previous {
		if check.Code == "rabbitmq-queues" && check.Status != "UNKNOWN" {
			for _, table := range check.Tables {
				for _, row := range table.Rows {
					if len(row) < 11 {
						continue
					}
					n, err := strconv.ParseInt(row[3], 10, 64)
					if err == nil {
						prior[row[0]+"\x00"+row[1]] = n
					}
				}
			}
		}
	}
	for i := range current {
		o := &current[i]
		if o.Code != "rabbitmq-queues" || o.Status == "UNKNOWN" {
			continue
		}
		matched, growing := 0, 0
		for ti := range o.Tables {
			for ri := range o.Tables[ti].Rows {
				row := o.Tables[ti].Rows[ri]
				if len(row) < 11 {
					continue
				}
				old, ok := prior[row[0]+"\x00"+row[1]]
				n, err := strconv.ParseInt(row[3], 10, 64)
				if !ok || err != nil {
					continue
				}
				matched++
				delta := n - old
				row[9] = fmt.Sprintf("%+d", delta)
				if delta > 0 {
					growing++
					if row[10] == "存在待消费积压" {
						row[10] = "待消费积压增加"
					}
				}
			}
		}
		if matched > 0 {
			if o.Measurements == nil {
				o.Measurements = map[string]float64{}
			}
			o.Measurements["growingQueues"] = float64(growing)
		}
	}
}
