// Package pglog reads complete PostgreSQL log records, retaining source offsets.
package pglog

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Event struct {
	Offset                                                                                                              int64
	Time                                                                                                                time.Time
	Type, Phase, DBName, Username, Client, Application, SQLState, SQL, Message, Detail, Hint, Context, Raw, Fingerprint string
	PID                                                                                                                 int
	DurationMs                                                                                                          float64
}
type Batch struct {
	Events   []Event
	Consumed int64
	Records  int
}

var durationRE = regexp.MustCompile(`(?is)^duration:\s*([0-9.]+)\s*ms\s*(?::\s*)?(statement|execute\s+[^\n]*?|parse\s+[^\n]*?|bind\s+[^\n]*?)\s*:\s*(.*)$`)
var plainBoundary = regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\d[ T]\d\d:\d\d:\d\d[^\n]*?\b(?:LOG|ERROR|FATAL|PANIC|WARNING|DETAIL|HINT|CONTEXT|STATEMENT):`)
var severityRE = regexp.MustCompile(`\b(LOG|ERROR|FATAL|PANIC|WARNING|DETAIL|HINT|CONTEXT|STATEMENT):\s*`)
var pidRE = regexp.MustCompile(`\[(\d+)\]`)
var timeRE = regexp.MustCompile(`^\d{4}-\d\d-\d\d[ T]\d\d:\d\d:\d\d(?:\.\d+)?(?:\s*(?:[+-]\d\d(?::?\d\d)?|[A-Z]{2,5}))?`)

func ParseTime(text, zone string) (time.Time, error) {
	text = strings.TrimSpace(text)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999Z07:00", "2006-01-02 15:04:05.999999Z0700", "2006-01-02 15:04:05.999999Z07"} {
		if t, e := time.Parse(layout, text); e == nil {
			return t, nil
		}
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("无效日志时区 %q", zone)
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999 MST", "2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999"} {
		if t, e := time.ParseInLocation(layout, text, loc); e == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法识别日志时间 %q", text)
}
func classify(e *Event, threshold int) bool {
	if e.SQLState == "40P01" || strings.HasPrefix(strings.ToLower(e.Message), "deadlock detected") {
		e.Type = "deadlock"
		return true
	}
	if m := durationRE.FindStringSubmatch(e.Message); m != nil {
		e.DurationMs, _ = strconv.ParseFloat(m[1], 64)
		if e.DurationMs < float64(threshold) {
			return false
		}
		e.Type = "slow_sql"
		e.Phase = strings.ToLower(strings.Fields(m[2])[0])
		if e.SQL == "" {
			e.SQL = strings.TrimSpace(m[3])
		}
		h := sha256.Sum256([]byte(e.SQL))
		e.Fingerprint = hex.EncodeToString(h[:])
		return true
	}
	msg := strings.ToLower(e.Message)
	if strings.HasPrefix(msg, "process ") && (strings.Contains(msg, "still waiting for") || strings.Contains(msg, " acquired ")) {
		e.Type = "lock_wait"
		return true
	}
	return false
}

// Partial records stay at their start offset and are reread on the next poll.
func ParseCSV(data []byte, zone string, threshold int) (Batch, error) {
	b := Batch{Events: []Event{}}
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	for {
		start := r.InputOffset()
		row, err := r.Read()
		end := r.InputOffset()
		if err == io.EOF {
			return b, nil
		}
		if err != nil {
			if end >= int64(len(data)) {
				if pe, ok := err.(*csv.ParseError); ok && pe.Err == csv.ErrQuote {
					return b, nil
				}
			}
			return b, fmt.Errorf("CSV 日志偏移 %d 解析失败: %w", start, err)
		}
		if end == int64(len(data)) && (len(data) == 0 || data[len(data)-1] != '\n') {
			return b, nil
		}
		if len(row) < 23 {
			return b, fmt.Errorf("CSV 日志偏移 %d 只有 %d 列，需要 PostgreSQL CSV 日志（至少 23 列）", start, len(row))
		}
		t, err := ParseTime(row[0], zone)
		if err != nil {
			return b, err
		}
		pid, _ := strconv.Atoi(row[3])
		e := Event{Offset: start, Time: t, DBName: row[2], Username: row[1], PID: pid, Client: row[4], Application: row[22], SQLState: row[12], Message: row[13], Detail: row[14], Hint: row[15], Context: row[18], SQL: row[19], Raw: string(data[start:end])}
		b.Consumed = end
		b.Records++
		if classify(&e, threshold) {
			b.Events = append(b.Events, e)
		}
	}
}

// Plain text is grouped by timestamped headers and backend PID. An unfinished
// final group is emitted only once the file has remained unchanged for a poll.
func ParseText(data []byte, zone string, threshold int, final bool) (Batch, error) {
	b := Batch{Events: []Event{}}
	starts := plainBoundary.FindAllIndex(data, -1)
	if len(starts) == 0 {
		if len(bytes.TrimSpace(data)) == 0 {
			b.Consumed = int64(len(data))
			return b, nil
		}
		return b, fmt.Errorf("未识别日志记录边界，请核对 log_line_prefix；支持时间戳与日志级别前缀")
	}
	type entry struct {
		ev         Event
		start, end int
		main       bool
	}
	var entries []entry
	for i, p := range starts {
		end := len(data)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		} else if !final {
			break
		}
		text := string(data[p[0]:end])
		headEnd := strings.Index(text, "\n")
		if headEnd < 0 {
			headEnd = len(text)
		}
		head := text[:headEnd]
		s := severityRE.FindStringSubmatchIndex(head)
		if s == nil {
			continue
		}
		ts := timeRE.FindString(head)
		t, err := ParseTime(ts, zone)
		if err != nil {
			return b, err
		}
		pid := 0
		if m := pidRE.FindStringSubmatch(head[:s[0]]); m != nil {
			pid, _ = strconv.Atoi(m[1])
		}
		kind := head[s[2]:s[3]]
		message := strings.TrimSpace(text[s[1]:])
		e := Event{Offset: int64(p[0]), Time: t, PID: pid, Raw: text}
		main := kind != "DETAIL" && kind != "HINT" && kind != "CONTEXT" && kind != "STATEMENT"
		if !main {
			for j := len(entries) - 1; j >= 0; j-- {
				if entries[j].main && entries[j].ev.PID == pid {
					switch kind {
					case "DETAIL":
						entries[j].ev.Detail = message
					case "HINT":
						entries[j].ev.Hint = message
					case "CONTEXT":
						entries[j].ev.Context = message
					case "STATEMENT":
						entries[j].ev.SQL = message
					}
					entries[j].ev.Raw += text
					break
				}
			}
		} else {
			e.Message = message
			entries = append(entries, entry{e, p[0], end, true})
		}
		b.Consumed = int64(end)
	}
	// On active files retain the final main record and its detail records, since
	// the next write may contain another DETAIL/STATEMENT for that same backend.
	if !final && len(entries) > 0 {
		last := len(entries) - 1
		b.Consumed = int64(entries[last].start)
		entries = entries[:last]
	}
	for _, en := range entries {
		if en.main {
			b.Records++
			if classify(&en.ev, threshold) {
				b.Events = append(b.Events, en.ev)
			}
		}
	}
	return b, nil
}
