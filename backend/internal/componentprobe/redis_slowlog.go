package componentprobe

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
)

func redisSlowlog(command func(...string) (any, error)) inspection.Observation {
	fail := func() inspection.Observation {
		return Failed("redis-slowlog", "Redis 慢查询", fmt.Errorf("慢日志读取失败，请核对 SLOWLOG LEN / GET 权限"))
	}
	length, err := command("SLOWLOG", "LEN")
	if err != nil {
		return fail()
	}
	n, ok := length.(int64)
	if !ok || n < 0 {
		return fail()
	}
	value, err := command("SLOWLOG", "GET", "50")
	if err != nil {
		return fail()
	}
	entries, ok := value.([]any)
	if !ok || len(entries) > 50 {
		return fail()
	}
	o := Observation("redis-slowlog", "Redis 慢查询", fmt.Sprintf("当前慢日志缓存 %d 条", n), "NORMAL")
	o.Measurements = map[string]float64{"slowLogLength": float64(n)}
	t := inspection.Table{Columns: []string{"ID", "发生时间", "耗时 ms", "命令摘要", "客户端"}, Note: "最近 48 小时，最多 50 条；使用 Redis 既有慢日志阈值。"}
	cutoff := time.Now().Add(-48 * time.Hour)
	for _, entry := range entries {
		fields, ok := entry.([]any)
		if !ok || len(fields) < 4 {
			return fail()
		}
		id, idOK := fields[0].(int64)
		at, timeOK := fields[1].(int64)
		duration, durationOK := fields[2].(int64)
		args, argsOK := fields[3].([]any)
		if !idOK || !timeOK || !durationOK || !argsOK || id < 0 || duration < 0 || at <= 0 {
			return fail()
		}
		when := time.Unix(at, 0)
		if when.Before(cutoff) {
			continue
		}
		var commandArgs []string
		for _, arg := range args {
			s, ok := arg.(string)
			if !ok {
				return fail()
			}
			commandArgs = append(commandArgs, s)
		}
		client := "—"
		if len(fields) > 4 {
			if v, ok := fields[4].(string); ok {
				client = v
			}
		}
		milliseconds := float64(duration) / 1000
		o.Measurements["slowLogMaxMs"] = max(o.Measurements["slowLogMaxMs"], milliseconds)
		t.Rows = append(t.Rows, []string{strconv.FormatInt(id, 10), when.Format(time.RFC3339), fmt.Sprintf("%.3f", milliseconds), safeRedisCommand(commandArgs), inspection.Preview(client, 128)})
	}
	o.Measurements["slowLogRecentShown"] = float64(len(t.Rows))
	if len(t.Rows) == 0 {
		o.Value = "最近 48 小时的慢日志缓存中暂无记录"
	}
	o.Tables = []inspection.Table{t}
	return o
}

func safeRedisCommand(args []string) string {
	if len(args) == 0 {
		return "—"
	}
	verb := strings.ToUpper(args[0])
	if verb == "AUTH" || verb == "HELLO" || verb == "ACL" || verb == "EVAL" || verb == "EVALSHA" || verb == "SCRIPT" {
		return verb + " [参数已隐藏]"
	}
	if (verb == "SET" || verb == "SETEX" || verb == "PSETEX" || verb == "GETSET" || verb == "HSET") && len(args) > 1 {
		return inspection.Preview(verb+" "+args[1]+" [写入参数已隐藏]", 512)
	}
	if verb == "MSET" || verb == "MSETNX" || verb == "CONFIG" {
		return verb + " [参数已隐藏]"
	}
	return inspection.Preview(strings.Join(args, " "), 512)
}
