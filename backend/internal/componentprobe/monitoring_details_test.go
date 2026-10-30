package componentprobe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
)

func TestRedisSlowlogReadsNestedRESPAndPreservesUnits(t *testing.T) {
	now := time.Now().Unix()
	entry := func(id, at, micros int64, args ...string) string {
		s := fmt.Sprintf("*6\r\n:%d\r\n:%d\r\n:%d\r\n*%d\r\n", id, at, micros, len(args))
		for _, a := range args {
			s += fmt.Sprintf("$%d\r\n%s\r\n", len(a), a)
		}
		return s + "$9\r\n127.0.0.1\r\n$0\r\n\r\n"
	}
	response := "*3\r\n" + entry(3, now, 12345, "SET", "key", "private-value") + entry(2, now, 250, "AUTH", "private-password") + entry(1, now-49*3600, 999999, "GET", "old-key")
	var calls []string
	o := redisSlowlog(func(args ...string) (any, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[1] == "LEN" {
			return redisValue(bufio.NewReader(strings.NewReader(":3\r\n")))
		}
		return redisValue(bufio.NewReader(strings.NewReader(response)))
	})
	if o.Status != "NORMAL" || o.Measurements["slowLogLength"] != 3 || o.Measurements["slowLogMaxMs"] != 12.345 || len(o.Tables[0].Rows) != 2 || o.Tables[0].Rows[0][2] != "12.345" {
		t.Fatalf("bad slowlog: %+v", o)
	}
	data, _ := json.Marshal(o)
	for _, secret := range []string{"private-value", "private-password", "old-key"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("exposed %s", secret)
		}
	}
	if strings.Join(calls, ";") != "SLOWLOG LEN;SLOWLOG GET 50" {
		t.Fatalf("unexpected commands: %v", calls)
	}
}

func TestRedisSlowlogPermissionAndMalformedResponsesStayUnknown(t *testing.T) {
	for _, wire := range []string{"-NOPERM sensitive-command\r\n", "*1\r\n*1\r\n:7\r\n", "*1\r\n$3\r\nabcXX", "*10001\r\n", strings.Repeat("*1\r\n", 10) + ":1\r\n", "$4194305\r\n"} {
		o := redisSlowlog(func(args ...string) (any, error) {
			if args[1] == "LEN" {
				return int64(1), nil
			}
			return redisValue(bufio.NewReader(strings.NewReader(wire)))
		})
		if o.Status != "UNKNOWN" || len(o.Measurements) != 0 || strings.Contains(o.Reason, "sensitive-command") {
			t.Fatalf("false zero/permission leak: %+v", o)
		}
	}
}

func TestRabbitQueuesPaginationAndBacklogChanges(t *testing.T) {
	queue := func(name string, ready, consumers int) map[string]any {
		return map[string]any{"name": name, "vhost": "/", "state": "running", "messages_ready": ready, "messages_unacknowledged": 2, "consumers": consumers, "message_stats": map[string]any{"publish_details": map[string]any{"rate": 3}, "deliver_get_details": map[string]any{"rate": 2}, "ack_details": map[string]any{"rate": 1}}}
	}
	var pages []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/queues" || r.URL.Query().Get("pagination") != "true" || r.URL.Query().Get("page_size") != "100" {
			t.Errorf("unsafe/unbounded request: %s", r.URL)
			w.WriteHeader(400)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, page)
		if page == 1 {
			json.NewEncoder(w).Encode(map[string]any{"items": []any{queue("a", 10, 1)}, "page": 1, "page_count": 2, "total_count": 2})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"items": []any{queue("b", 20, 0)}, "page": 2, "page_count": 2, "total_count": 2})
		}
	}))
	defer srv.Close()
	o := rabbitQueues(context.Background(), Config{Endpoint: srv.URL}, 2)
	if o.Status != "WARNING" || fmt.Sprint(pages) != "[1 2]" || o.Measurements["backlogQueues"] != 2 || o.Measurements["noConsumerQueues"] != 1 || o.Measurements["publishRate"] != 6 {
		t.Fatalf("queues: %+v pages %v", o, pages)
	}
	previous := inspection.Observation{Code: o.Code, Status: "NORMAL", Tables: []inspection.Table{{Rows: [][]string{{"/", "a", "running", "4", "2", "1", "—", "—", "—", "—", ""}, {"/", "new", "running", "0", "0", "0", "—", "—", "—", "—", ""}}}}}
	current := []inspection.Observation{o}
	CompareRabbitQueues(current, []inspection.Observation{previous})
	rows := current[0].Tables[0].Rows
	if current[0].Measurements["growingQueues"] != 1 || rows[1][9] != "+6" || rows[0][9] != "—" || rows[1][10] != "待消费积压增加" {
		t.Fatalf("fabricated trend: %+v", current)
	}
}

func TestRabbitQueuesIncompleteDataNeverBecomesHealthyZero(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		expected   int
		status     string
	}{
		{"empty", `{"items":[],"page":1,"page_count":0,"total_count":0}`, 0, "NORMAL"},
		{"old API empty", `[]`, 0, "NORMAL"},
		{"missing counts", `[{"name":"a","vhost":"/","state":"running"}]`, 1, "UNKNOWN"},
		{"scope mismatch", `[]`, 5, "UNKNOWN"},
		{"missing page", `{"items":[],"page":1,"page_count":2,"total_count":4}`, 4, "UNKNOWN"},
		{"short total", `{"items":[{"name":"a","vhost":"/","messages_ready":0,"messages_unacknowledged":0,"consumers":0}],"page":1,"page_count":1,"total_count":2}`, 2, "UNKNOWN"},
		{"duplicate", `[{"name":"a","vhost":"/"},{"name":"a","vhost":"/"}]`, 2, "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			o := rabbitQueues(context.Background(), Config{Endpoint: srv.URL}, tc.expected)
			if o.Status != tc.status {
				t.Fatalf("%+v", o)
			}
			if tc.name == "missing counts" {
				if _, ok := o.Measurements["backlogQueues"]; ok || o.Tables[0].Rows[0][3] != "—" {
					t.Fatalf("fabricated zero: %+v", o)
				}
			}
		})
	}
}

func TestRabbitQueueReadIsIndependentOfNodePermissions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/overview":
			fmt.Fprint(w, `{"object_totals":{"queues":0}}`)
		case "/api/queues":
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(403)
		}
	}))
	defer srv.Close()
	out := Collect(context.Background(), Config{Kind: "rabbitmq", Endpoint: srv.URL})
	if len(out) != 3 || out[1].Status != "UNKNOWN" || out[2].Status != "NORMAL" {
		t.Fatalf("node permission discarded queue data: %+v", out)
	}
}
