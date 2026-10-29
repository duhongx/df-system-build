package service

import (
	"context"
	"df-build-server/internal/model"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKubernetesInspectionPaginatesAndSeparatesCurrentFailureFromHistory(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/nodes":
			fmt.Fprint(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"master","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":"v1.28"}}}]} `)
		case "/api/v1/pods":
			pages++
			if r.URL.Query().Get("continue") == "" {
				fmt.Fprint(w, `{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"next"},"items":[{"metadata":{"name":"coredns","namespace":"kube-system"},"spec":{"containers":[{"name":"dns"}]},"status":{"phase":"Running","containerStatuses":[{"name":"dns","ready":true,"restartCount":2}]}}]}`)
			} else {
				fmt.Fprint(w, `{"apiVersion":"v1","kind":"PodList","items":[{"metadata":{"name":"app","namespace":"prod"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Pending","containerStatuses":[{"name":"app","ready":false,"state":{"waiting":{"reason":"ImagePullBackOff"}}}]}},{"metadata":{"name":"completed-job","namespace":"prod"},"status":{"phase":"Succeeded"}}]}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	kubeconfig := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: %s\ncontexts:\n- name: fixture\n  context:\n    cluster: fixture\n    user: fixture\ncurrent-context: fixture\nusers:\n- name: fixture\n  user: {}\n", srv.URL)
	svc := &OperationsService{}
	out := svc.collectKubernetes(context.Background(), model.ComponentConnection{Kind: "kubernetes"}, kubeconfig)
	if pages != 2 || len(out) != 5 {
		t.Fatalf("incomplete collection: pages=%d %+v", pages, out)
	}
	if out[2].Status != "NORMAL" || !strings.Contains(out[2].Value, "当前异常 0") || !strings.Contains(out[2].Value, "1 个有历史重启") {
		t.Fatalf("historical restart became current anomaly: %+v", out[2])
	}
	if out[4].Status != "WARNING" || !strings.Contains(out[4].Value, "当前异常 1") || !strings.Contains(out[4].Value, "已成功结束 1") {
		t.Fatalf("wrong app classification: %+v", out[4])
	}
}
