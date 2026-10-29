package opsprobe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEtcdHealthReadsActualEndpointAndRejectsMissingResults(t *testing.T) {
	for _, v := range []struct{ body, want string }{{`{"health":"true"}`, "NORMAL"}, {`{"health":false,"reason":"quorum unavailable"}`, "WARNING"}, {`{}`, "UNKNOWN"}, {`not json`, "UNKNOWN"}} {
		t.Run(v.want+v.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/health" || r.URL.Query().Get("serializable") != "false" {
					t.Error("unexpected operation")
				}
				fmt.Fprint(w, v.body)
			}))
			defer srv.Close()
			o := inspectEtcd(context.Background(), []string{"etcd", "--advertise-client-urls=" + srv.URL}, t.TempDir())
			if o.Status != v.want {
				t.Fatalf("%+v", o)
			}
		})
	}
}
