package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Follow the actual index -> route import chain. An unused newer bundle in
// assets must not make an old embedded frontend pass this regression check.
func TestEmbeddedSQLExecutionActions(t *testing.T) {
	index, err := StaticFS.ReadFile("dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	main := regexp.MustCompile(`src="/([^" ]+\.js)"`).FindSubmatch(index)
	if len(main) != 2 {
		t.Fatal("index does not reference a frontend module")
	}
	module, err := StaticFS.ReadFile("dist/" + string(main[1]))
	if err != nil {
		t.Fatal(err)
	}
	route := regexp.MustCompile(`PostgreSQLSQLExecutionView-[A-Za-z0-9_-]+\.js`).Find(module)
	if route == nil {
		t.Fatal("SQL execution route missing from the active frontend")
	}
	view, err := StaticFS.ReadFile("dist/assets/" + string(route))
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"标记失败并继续", "单独重试", "执行 / 继续后续 SQL", "只执行未执行语句", "本轮可执行 SQL 已全部处理", "进度刷新失败", "重试刷新", "查看原因与处理方式"} {
		if !strings.Contains(string(view), label) {
			t.Fatalf("active embedded SQL page is stale: missing %q; rebuild frontend before compiling dfops", label)
		}
	}
}

func TestEmbeddedResumableArtifactUpload(t *testing.T) {
	index, err := StaticFS.ReadFile("dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	main := regexp.MustCompile(`src="/([^" ]+\.js)"`).FindSubmatch(index)
	if len(main) != 2 {
		t.Fatal("index does not reference a frontend module")
	}
	module, err := StaticFS.ReadFile("dist/" + string(main[1]))
	if err != nil {
		t.Fatal(err)
	}
	route := regexp.MustCompile(`BatchDeployView-[A-Za-z0-9_-]+\.js`).Find(module)
	if route == nil {
		t.Fatal("active artifact upload route missing")
	}
	view, err := StaticFS.ReadFile("dist/assets/" + string(route))
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"支持分片续传", "服务端确认接收进度", "暂停上传", "高速模式"} {
		if !strings.Contains(string(view), label) {
			t.Fatalf("active upload page missing %q; rebuild frontend", label)
		}
	}
	worker := regexp.MustCompile(`upload-hash\.worker-[A-Za-z0-9_-]+\.js`).Find(view)
	if worker == nil {
		t.Fatal("upload hashing worker missing from the active frontend")
	}
	if _, err := StaticFS.ReadFile("dist/assets/" + string(worker)); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedOperationalFixes(t *testing.T) {
	index, err := StaticFS.ReadFile("dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	main := regexp.MustCompile(`src="/([^" ]+\.js)"`).FindSubmatch(index)
	if len(main) != 2 {
		t.Fatal("index does not reference a frontend module")
	}
	module, err := StaticFS.ReadFile("dist/" + string(main[1]))
	if err != nil {
		t.Fatal(err)
	}
	for route, labels := range map[string][]string{
		"ArtifactBatchesView":                {"业务版本", "更新说明", "发布时间"},
		"ReleaseVersionInfo":                 {"业务版本", "功能变更", "参数说明", "version.json"},
		"BatchDeployView":                    {"查询已创建任务", "正在校验制品并创建镜像任务"},
		"BuildQueueView":                     {"构建队列刷新失败", "重试刷新", "运行任务状态暂未读取成功"},
		"PostgreSQLViewDependencyChangeView": {"视图依赖任务记录", "按编号打开", "关联任务编号无效", "复制完整错误详情"},
		"PostgreSQLWorkbenchView":            {"固定目标", "SQL 模板排行", "累计 SQL 统计", "重新执行并导出 CSV"},
	} {
		name := regexp.MustCompile(route + `-[A-Za-z0-9_-]+\.js`).Find(module)
		if name == nil {
			t.Fatalf("active route missing: %s", route)
		}
		view, err := StaticFS.ReadFile("dist/assets/" + string(name))
		if err != nil {
			t.Fatal(err)
		}
		for _, label := range labels {
			if !strings.Contains(string(view), label) {
				t.Fatalf("active %s missing %q; rebuild frontend", route, label)
			}
		}
	}
}

func TestSPACacheAndMissingAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterSPA(router)
	for _, path := range []string{"/", "/index.html", "/postgresql/sql-execution", "/postgresql/workbench"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s can serve stale index: %v", path, response.Header())
		}
	}
	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest("GET", "/assets/missing-version.js", nil))
	if missing.Code != 404 {
		t.Fatalf("missing JS was replaced with HTML: %d", missing.Code)
	}
}
