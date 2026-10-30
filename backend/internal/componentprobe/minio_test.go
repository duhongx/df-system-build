package componentprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const minioFixture = `{"mode":"online","backend":{"backendType":"Erasure","onlineDisks":2,"offlineDisks":0},"buckets":{"count":3},"objects":{"count":42},"usage":{"size":1073741824},"servers":[{"endpoint":"minio-a:9000","state":"online","version":"2025-04-22","uptime":3600,"drives":[{"path":"/data","state":"ok","totalspace":107374182400,"usedspace":10737418240,"availspace":96636764160}],"minio_env_vars":{"MINIO_ROOT_PASSWORD":"never-store-environment-secret"}},{"endpoint":"minio-b:9000","state":"online","version":"2025-04-22","uptime":7200,"drives":[{"path":"/data","state":"ok","totalspace":107374182400,"usedspace":10737418240,"availspace":96636764160}]}]}`

func TestMinIOEndpoint(t *testing.T) {
	for input, want := range map[string]string{"192.168.199.102:9000": "http://192.168.199.102:9000", " https://minio.example:9443/ ": "https://minio.example:9443", "[::1]:9000": "http://[::1]:9000"} {
		got, err := MinIOEndpoint(input)
		if err != nil || got != want {
			t.Fatalf("%q: %q / %v", input, got, err)
		}
	}
	for _, input := range []string{"", "ftp://minio:9000", "http://key:secret@minio:9000", "http://minio:9000/bucket", "http://minio:9000?token=x", "http://minio:9000?", "http://minio:0", "http://minio:65536", "http://minio:9000/#console"} {
		if _, err := MinIOEndpoint(input); err == nil {
			t.Fatalf("accepted invalid API endpoint %q", input)
		}
	}
}

func TestMinIOAPISignedReadOnlyFacts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/minio/admin/v3/info" || r.URL.Query().Get("metrics") != "false" {
			t.Errorf("unexpected API operation: %s %s", r.Method, r.URL)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=fixture-access/") || r.Header.Get("X-Amz-Date") == "" || r.Header.Get("X-Amz-Content-Sha256") != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
			t.Error("missing MinIO SigV4 authentication")
		}
		fmt.Fprint(w, minioFixture)
	}))
	defer server.Close()
	out := Collect(context.Background(), Config{Kind: "minio", Endpoint: strings.TrimPrefix(server.URL, "http://"), Username: "fixture-access", Secret: "fixture-secret"})
	if calls.Load() != 1 || len(out) != 2 || out[0].Status != "RECORDED" || out[1].Status != "NORMAL" {
		t.Fatalf("invalid API result: %+v", out)
	}
	if out[1].Measurements["onlineDisks"] != 2 || out[1].Measurements["objects"] != 42 || out[1].Measurements["usedBytes"] != 1<<30 || len(out[0].Tables[0].Rows) != 2 || len(out[1].Tables[0].Rows) != 2 {
		t.Fatal("missing structured monitoring and report facts")
	}
	encoded, _ := json.Marshal(out)
	for _, secret := range []string{"never-store-environment-secret", "fixture-secret", "fixture-access", "Authorization", "minio_env_vars"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("sensitive response field stored: %s", secret)
		}
	}
}

func TestMinIOAPIRejectsInvalidAndUnauthorizedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", 403, `{"Code":"AccessDenied","Message":"fixture-secret"}`},
		{"console", 200, "<html>MinIO Console</html>"},
		{"empty", 200, `{}`},
		{"null", 200, `null`},
		{"partial", 200, `{"servers":[`},
		{"oversized", 200, minioFixture + strings.Repeat(" ", 4<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			out := Collect(context.Background(), Config{Kind: "minio", Endpoint: server.URL, Username: "fixture-access", Secret: "fixture-secret"})
			if len(out) != 2 || out[0].Status != "UNKNOWN" || out[1].Status != "UNKNOWN" || strings.Contains(out[1].Reason, "fixture-secret") {
				t.Fatalf("invalid API response reported healthy or leaked: %+v", out)
			}
			if tc.status == 403 && !strings.Contains(out[1].Reason, "admin:ServerInfo") {
				t.Fatal("permission requirement not explained")
			}
		})
	}
}

func TestMinIOAPIKeepsOfflineAndUnknownStates(t *testing.T) {
	for _, tc := range []struct{ name, body, status string }{
		{"offline drive", strings.Replace(minioFixture, `"state":"ok"`, `"state":"offline"`, 1), "WARNING"},
		{"offline node", strings.Replace(minioFixture, `"state":"online"`, `"state":"offline"`, 1), "WARNING"},
		{"unknown state", strings.ReplaceAll(minioFixture, `"state":"ok"`, `"state":"unrecognized"`), "UNKNOWN"},
		{"high disk usage", strings.ReplaceAll(minioFixture, `"usedspace":10737418240`, `"usedspace":100000000000`), "WARNING"},
		{"missing drives", `{"servers":[{"endpoint":"node:9000","state":"online"}]}`, "UNKNOWN"},
		{"summary offline", strings.Replace(minioFixture, `"offlineDisks":0`, `"offlineDisks":1`, 1), "WARNING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			out := Collect(context.Background(), Config{Kind: "minio", Endpoint: server.URL, Username: "access", Secret: "secret"})
			if out[1].Status != tc.status {
				t.Fatalf("expected %s: %+v", tc.status, out[1])
			}
		})
	}
}

func TestMinIOAPIRequiresCredentialsAndDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	for _, c := range []Config{{Kind: "minio", Endpoint: server.URL}, {Kind: "minio", Endpoint: server.URL, Username: "access", Secret: "secret"}} {
		out := Collect(context.Background(), c)
		if out[1].Status != "UNKNOWN" || calls.Load() != 0 {
			t.Fatal("credentials omitted or forwarded to redirected target")
		}
	}
}

func TestMinIOAPIHonorsCancellationAndTLSVerification(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, minioFixture) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	c := Config{Kind: "minio", Endpoint: server.URL, Username: "access", Secret: "secret"}
	if out := Collect(context.Background(), c); out[1].Status != "UNKNOWN" {
		t.Fatal("untrusted TLS certificate accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if out := Collect(ctx, c); out[1].Status != "UNKNOWN" || time.Since(start) > time.Second {
		t.Fatal("cancellation ignored")
	}
}

func TestMinIOAPIDoesNotInventMissingCapacityOrCounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"buckets":{},"objects":{},"usage":{},"servers":[{"endpoint":"node:9000","state":"online","drives":[{"state":"ok","totalspace":100}]}]}`)
	}))
	defer server.Close()
	out := Collect(context.Background(), Config{Kind: "minio", Endpoint: server.URL, Username: "access", Secret: "secret"})
	for _, key := range []string{"maxDiskPercent", "buckets", "objects", "usedBytes"} {
		if _, ok := out[1].Measurements[key]; ok {
			t.Fatalf("missing measurement %s was filled with zero", key)
		}
	}
	if out[1].Tables[0].Rows[0][3] != "未取得" {
		t.Fatal("missing capacity displayed as valid")
	}
}
