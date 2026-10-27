package pglog

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

func csvRecord(message, detail, sql, state string) []byte {
	row := make([]string, 26)
	row[0] = "2026-10-27 09:12:30.123 CST"
	row[1] = "doctor_role"
	row[2] = "df_his"
	row[3] = "1234"
	row[4] = "192.0.2.1:3456"
	row[5] = "fixture-session"
	row[11] = "LOG"
	row[12] = state
	row[13] = message
	row[14] = detail
	row[15] = "fixture hint"
	row[18] = "SQL statement in test"
	row[19] = sql
	row[22] = "clinic-app"
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write(row)
	w.Flush()
	return b.Bytes()
}
func TestCSVSlowRecordAndChunkBoundaries(t *testing.T) {
	sql := "SELECT '逗号,与换行\n双引号\"' AS value;"
	record := csvRecord("duration: 5123.456 ms  statement: "+sql, "parameters: $1 = 'fixture'", "", "")
	for _, cut := range []int{1, 25, len(record) / 2, len(record) - 2, len(record) - 1} {
		b, err := ParseCSV(record[:cut], "Asia/Shanghai", 5000)
		if err != nil || b.Consumed != 0 || len(b.Events) != 0 {
			t.Fatalf("partial %d: %+v %v", cut, b, err)
		}
	}
	b, err := ParseCSV(record, "Asia/Shanghai", 5000)
	if err != nil || b.Consumed != int64(len(record)) || len(b.Events) != 1 {
		t.Fatalf("complete: %+v %v", b, err)
	}
	e := b.Events[0]
	if e.SQL != sql || e.PID != 1234 || e.Phase != "statement" || e.DurationMs != 5123.456 || e.Username != "doctor_role" || e.Time.UTC().Hour() != 1 || !strings.Contains(e.Raw, "双引号") {
		t.Fatalf("lost fields: %+v", e)
	}
	combined := append(append([]byte{}, record...), record[:len(record)/2]...)
	b, err = ParseCSV(combined, "Asia/Shanghai", 5000)
	if err != nil || len(b.Events) != 1 || b.Consumed != int64(len(record)) {
		t.Fatalf("bad cursor: %+v %v", b, err)
	}
}
func TestCSVPhasesDeadlocksAndThreshold(t *testing.T) {
	for _, phase := range []string{"execute <unnamed>", "execute prepared_query", "parse <unnamed>", "bind <unnamed>"} {
		b, err := ParseCSV(csvRecord("duration: 6000.100 ms  "+phase+": SELECT $1", "parameters: $1 = 'fixture'", "", ""), "Asia/Shanghai", 5000)
		if err != nil || len(b.Events) != 1 || b.Events[0].SQL != "SELECT $1" || b.Events[0].Phase != strings.Fields(phase)[0] {
			t.Fatalf("phase %s: %+v %v", phase, b, err)
		}
	}
	b, err := ParseCSV(csvRecord("duration: 4999.9 ms statement: SELECT 1", "", "", ""), "Asia/Shanghai", 5000)
	if err != nil || len(b.Events) != 0 || b.Consumed == 0 {
		t.Fatal(b, err)
	}
	detail := "Process 1234 waits for ShareLock on transaction 123; blocked by process 5678.\nProcess 5678 waits for transaction 456."
	b, err = ParseCSV(csvRecord("deadlock detected", detail, "UPDATE fixture SET value=2", "40P01"), "Asia/Shanghai", 5000)
	if err != nil || len(b.Events) != 1 || b.Events[0].Type != "deadlock" || b.Events[0].Detail != detail || b.Events[0].Context == "" {
		t.Fatal(b, err)
	}
	b, err = ParseCSV([]byte("bad\"quote,record\n"), "Asia/Shanghai", 5000)
	if err == nil || b.Consumed != 0 {
		t.Fatalf("corrupt record hidden: %+v %v", b, err)
	}
}
func TestTextMultilineAndSessionDetails(t *testing.T) {
	text := "2026-10-27 09:00:00.123 CST [123] LOG:  duration: 6000.000 ms  execute <unnamed>: SELECT $1,\n  '中文' AS x\n2026-10-27 09:00:00.123 CST [123] DETAIL:  parameters: $1 = 'fixture'\n2026-10-27 09:00:01.123 CST [456] ERROR:  deadlock detected\n2026-10-27 09:00:01.123 CST [456] DETAIL:  blocked by process 123\n2026-10-27 09:00:01.123 CST [456] STATEMENT:  UPDATE fixture SET x=1\n"
	b, err := ParseText([]byte(text), "Asia/Shanghai", 5000, true)
	if err != nil || len(b.Events) != 2 {
		t.Fatalf("text: %+v %v", b, err)
	}
	if b.Events[0].PID != 123 || !strings.Contains(b.Events[0].SQL, "中文") || !strings.Contains(b.Events[0].Detail, "parameters") || b.Events[1].PID != 456 || b.Events[1].SQL != "UPDATE fixture SET x=1" || b.Events[1].Type != "deadlock" {
		t.Fatalf("session linkage: %+v", b.Events)
	}
	partial, err := ParseText([]byte(text), "Asia/Shanghai", 5000, false)
	if err != nil || len(partial.Events) != 1 || partial.Consumed != int64(strings.Index(text, "2026-10-27 09:00:01")) {
		t.Fatalf("text cursor: %+v %v", partial, err)
	}
}
