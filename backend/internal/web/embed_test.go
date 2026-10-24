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
	for _, label := range []string{"标记失败并继续", "单独重试", "执行 / 继续后续 SQL", "只执行未执行语句", "本轮可执行 SQL 已全部处理", "无法获取最新执行进度", "查看原因与处理方式"} {
		if !strings.Contains(string(view), label) {
			t.Fatalf("active embedded SQL page is stale: missing %q; rebuild frontend before compiling dfops", label)
		}
	}
}

func TestSPACacheAndMissingAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterSPA(router)
	for _, path := range []string{"/", "/index.html", "/postgresql/sql-execution"} {
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
