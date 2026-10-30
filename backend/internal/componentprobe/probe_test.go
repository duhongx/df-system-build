package componentprobe

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPCollectorsReportPartialAndResourceFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "reader" || p != "fixture" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/_cluster/health":
			fmt.Fprint(w, `{"cluster_name":"customer","status":"yellow","number_of_nodes":1,"number_of_data_nodes":1,"active_shards":10,"unassigned_shards":2}`)
		case "/_nodes/stats/fs,jvm":
			fmt.Fprint(w, `{"nodes":{"a":{"name":"es-1","fs":{"total":{"total_in_bytes":100,"available_in_bytes":10}},"jvm":{"mem":{"heap_used_percent":30}}}}}`)
		case "/api/overview":
			fmt.Fprint(w, `{"rabbitmq_version":"3.12","cluster_name":"mq","object_totals":{"connections":3,"queues":10,"consumers":4},"queue_totals":{"messages_ready":15,"messages_unacknowledged":2}}`)
		case "/api/nodes":
			fmt.Fprint(w, `[{"name":"mq-1","running":true,"mem_alarm":false,"disk_free_alarm":true,"partitions":[]}]`)
		default:
			w.WriteHeader(403)
		}
	}))
	defer srv.Close()
	for _, kind := range []string{"elasticsearch", "rabbitmq"} {
		c := Config{Kind: kind, Endpoint: srv.URL, Username: "reader", Secret: "fixture"}
		out := Collect(context.Background(), c)
		expected := 2
		if kind == "rabbitmq" {
			expected = 3
		}
		if len(out) != expected || out[1].Status != "WARNING" {
			t.Fatalf("%s: %+v", kind, out)
		}
		c.Secret = "bad"
		out = Collect(context.Background(), c)
		if len(out) != 1 || out[0].Status != "UNKNOWN" || !strings.Contains(out[0].Reason, "401") {
			t.Fatalf("authentication failure hidden: %+v", out)
		}
	}
}
func TestHTTPRedirectDoesNotForwardCredentials(t *testing.T) {
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer source.Close()
	out := Collect(context.Background(), Config{Kind: "elasticsearch", Endpoint: source.URL, Username: "secret-user", Secret: "secret-value"})
	if called || out[0].Status != "UNKNOWN" || strings.Contains(out[0].Reason, "secret-value") {
		t.Fatal("unsafe redirect handling")
	}
}

func TestRedisCollectorUsesOnlyInspectionCommands(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan []string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- nil
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		var commands []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				break
			}
			var n int
			if _, err = fmt.Sscanf(line, "*%d", &n); err != nil {
				break
			}
			var args []string
			for i := 0; i < n; i++ {
				header, _ := r.ReadString('\n')
				var size int
				fmt.Sscanf(header, "$%d", &size)
				b := make([]byte, size+2)
				for pos := 0; pos < len(b); {
					n, e := r.Read(b[pos:])
					pos += n
					if e != nil {
						break
					}
				}
				args = append(args, string(b[:size]))
			}
			commands = append(commands, strings.Join(args, " "))
			switch args[0] {
			case "AUTH":
				fmt.Fprint(conn, "+OK\r\n")
			case "PING":
				fmt.Fprint(conn, "+PONG\r\n")
			case "INFO":
				body := "redis_version:7.2\r\nrole:slave\r\nconnected_clients:3\r\nused_memory:90\r\nmaxmemory:100\r\nused_memory_human:90B\r\nmaxmemory_human:100B\r\nmaster_link_status:down\r\nrdb_last_bgsave_status:ok\r\naof_enabled:1\r\naof_last_write_status:err\r\n"
				fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(body), body)
			case "SLOWLOG":
				if args[1] == "LEN" {
					fmt.Fprint(conn, ":0\r\n")
				} else {
					fmt.Fprint(conn, "*0\r\n")
				}
			default:
				fmt.Fprint(conn, "-unsupported\r\n")
			}
		}
		done <- commands
	}()
	out := Collect(context.Background(), Config{Kind: "redis", Endpoint: "redis://" + listener.Addr().String(), Username: "reader", Secret: "fixture"})
	if len(out) != 3 || out[0].Status != "WARNING" || out[1].Status != "WARNING" || out[2].Status != "NORMAL" {
		t.Fatalf("bad facts: %+v", out)
	}
	commands := <-done
	if strings.Join(commands, ";") != "AUTH reader fixture;PING;INFO;SLOWLOG LEN;SLOWLOG GET 50" {
		t.Fatalf("unexpected commands: %v", commands)
	}
}
func TestRedisMalformedReplyIsRejected(t *testing.T) {
	for _, s := range []string{"$-1\r\n", "$4194305\r\n", "$3\r\nabcXX", "-NOAUTH secret-value\r\n"} {
		if _, err := redisReply(bufio.NewReader(strings.NewReader(s))); err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("accepted or leaked %q: %v", s, err)
		}
	}
}
