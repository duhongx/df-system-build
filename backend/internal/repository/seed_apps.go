package repository

import (
	"df-build-server/internal/model"
	"df-build-server/pkg/logger"
)

// SeedApplications inserts legacy _config.yaml applications into the database.
// Uses per-record check: only inserts if app_name doesn't already exist.
// Existing records are never overwritten (preserves user modifications).
func SeedApplications() {
	// Vue sub-app code mapping
	zipNameMap := map[string]string{
		"web-bui":            "00",
		"web-menzhenysz":     "04",
		"web-yaofang":        "05",
		"web-yaoku":          "06",
		"web-wuzi":           "08",
		"web-zhuyuanysz":     "10",
		"web-zhuyuansf":      "11",
		"web-zhuyuanhsz":     "12",
		"web-ruyuanzbzx":     "15",
		"web-cunyi":          "16",
		"web-shouma":         "18",
		"web-zhushujugl":     "19",
		"web-bingangl":       "20",
		"web-binglizk":       "21",
		"web-guahaosf":       "23",
		"web-pishi":          "27",
		"web-kangfu":         "36",
		"web-yiji":           "37",
		"web-gyql":           "60",
		"web-mobanbjq":       "71",
		"web-chuanranbingbk": "75",
		"web-biaodangl":      "80",
		"web-report":         "81",
		"web-cdss":           "83",
		"web-buliangsj":      "88",
		"web-message":        "94",
		"web-yibaogl":        "99",
		"web-yewugymk":       "gymk",
	}

	// Main apps (vue but not sub-apps)
	mainApps := map[string]bool{
		"web-main":       true,
		"web-cdr":        true,
		"web-opm":        true,
		"web-biaodanbjq": true,
	}

	type appEntry struct {
		Name    string
		GitRepo string
	}

	entries := []appEntry{
		// Java backend services
		{"his-gateway", "ssh://git@192.168.1.206/df-his-backend/base/df-his-gateway.git"},
		{"gy-jichufw", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-jichufw.git"},
		{"gy-bingrenzsy", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-bingrenzsy.git"},
		{"gy-mobanku", "ssh://git@192.168.1.206/df-his-backend/lc/df-mic-mobanku.git"},
		{"gy-renwugl", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-renwugl.git"},
		{"gy-web-renwugl", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-renwugl.git"},
		{"dfmessage-service", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-message.git"},
		{"df-authorization", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-oauth2.git"},
		{"oss", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-oss.git"},
		{"onecenter-service", "ssh://git@192.168.1.206/df-his-op/df-his-op-service.git"},
		{"gy-biaodan", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-biaodan.git"},
		{"lc-zhuyuan", "ssh://git@192.168.1.206/df-his-backend/lc/df-mic-lc-zhuyuan.git"},
		{"lc-menzhenjz", "ssh://git@192.168.1.206/df-his-backend/lc/df-mic-lc-menzhen.git"},
		{"lc-shenqingdanzx", "ssh://git@192.168.1.206/df-his-backend/lc/df-mic-shengqingdan.git"},
		{"lc-bingliku", "ssh://git@192.168.1.206/df-his-backend/lc/df-mic-bingliku.git"},
		{"lc-binglizk", "ssh://git@192.168.1.206/df-his-backend/ylgl/df-mic-binglizk.git"},
		{"lc-jibingbk", "ssh://git@192.168.1.206/df-his-backend/ylgl/df-mic-jibingbk.git"},
		{"cdr", "ssh://git@192.168.1.206/df-his-backend/lc/df-mic-cdr.git"},
		{"cdss", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-guizeyq.git"},
		{"jd-api", "ssh://git@192.168.1.206/df-his-op/jd-api.git"},
		{"df-buliangsj", "ssh://git@192.168.1.206/df-his-backend/ylgl/df-mic-buliangsj.git"},
		{"jj-guahao", "ssh://git@192.168.1.206/df-his-backend/cw/df-mic-jj-menzhen.git"},
		{"jj-zhuyuan", "ssh://git@192.168.1.206/df-his-backend/cw/df-mic-jj-zhuyuan.git"},
		{"jj-piaojugl", "ssh://git@192.168.1.206/df-his-backend/cw/df-mic-piaojugl.git"},
		{"gy-report", "ssh://git@192.168.1.206/df-report/df-mic-report.git"},
		{"gy-bingan", "ssh://git@192.168.1.206/df-his-backend/ylgl/df-mic-bingan.git"},
		{"gy-yiliaogl", "ssh://git@192.168.1.206/df-his-backend/ylgl/df-mic-yiliaogl.git"},
		{"df-gongzuoliu", "ssh://git@192.168.1.206/df-his-backend/base/df-mic-gongzuoliu.git"},
		{"df-cdc", "ssh://git@192.168.1.206/df-his-op/df-cdc.git"},
		{"df-dataforge", "ssh://git@192.168.1.206/df-his-backend/base/df-dataforge.git"},
		{"df-wuzi", "ssh://git@192.168.1.206/df-his-backend/ykf/df-mic-wuzixt.git"},
		{"agg-shenqingdan", "ssh://git@192.168.1.206/df-his-backend/lc/df-agg-shenqingdan.git"},
		{"agg-zhuyuancwgl", "ssh://git@192.168.1.206/df-his-backend/cw/df-agg-chuangweigl.git"},
		{"agg-feiyongkz", "ssh://git@192.168.1.206/df-his-backend/cw/df-agg-feiyongkz.git"},
		{"agg-bingangl", "ssh://git@192.168.1.206/df-his-backend/lc/df-agg-bingangl.git"},
		{"agg-jianchagl", "ssh://git@192.168.1.206/df-his-backend/lc/df-agg-jiancha.git"},
		{"agg-jianyantmgl", "ssh://git@192.168.1.206/df-his-backend/lc/df-agg-jianyan.git"},
		{"agg-yaokufang", "ssh://git@192.168.1.206/df-his-backend/ykf/df-agg-yaokufang.git"},
		{"agg-zhuyuanfyjf", "ssh://git@192.168.1.206/df-his-backend/cw/df-agg-zhuyuanjf.git"},
		{"agg-zhuyuanyz", "ssh://git@192.168.1.206/df-his-backend/lc/df-agg-zhuyuanyz.git"},
		{"agg-yibaogl", "ssh://git@192.168.1.206/df-his-yb/df-agg-yibaogl.git"},
		{"agg-linchuangmz", "ssh://git@192.168.1.206/df-his-backend/lc/df-agg-linchuangmz.git"},
		{"winbff-binglizk", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-binglizk.git"},
		{"winbff-guahaosf", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-guahaosf.git"},
		{"winbff-jichufw", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff--jichufw.git"},
		{"winbff-linchuanggy", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-lingchunggy.git"},
		{"winbff-linchuangmz", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-lingchuangmz.git"},
		{"winbff-linchuangzy", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-lingchuangzy.git"},
		{"winbff-yaokufang", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-yaokufang.git"},
		{"winbff-yibaogl", "ssh://git@192.168.1.206/df-his-yb/df-bff-yibaogl.git"},
		{"winbff-zhuyuansf", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-zhuyuansf.git"},
		{"winbff-cunyi", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-cunyi.git"},
		{"ykf-jichuyw", "ssh://git@192.168.1.206/df-his-backend/ykf/df-mic-yaokufang.git"},
		{"hisbff-cdr", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-cdr.git"},
		{"df-zhifu", "ssh://git@192.168.1.206/df-his-backend/jk/df-zhifu.git"},
		{"df-office", "ssh://git@192.168.1.206/df-his-backend/base/df-office.git"},
		{"df-sapi", "ssh://git@192.168.1.206/df-his-backend/jk/df-sapi.git"},
		{"df-adapter", "ssh://git@192.168.1.206/df-his-backend/jk/df-soap-adapter.git"},
		{"df-oapi", "ssh://git@192.168.1.206/df-his-backend/jk/df-oapi.git"},
		{"df-oapi-node1", "ssh://git@192.168.1.206/df-his-backend/jk/df-oapi.git"},
		{"df-oapi-danju", "ssh://git@192.168.1.206/df-his-backend/jk/df-oapi.git"},
		{"df-jcpt", "ssh://git@192.168.1.206/df-his-backend/jk/df-oapi.git"},
		{"yb-service", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao.git"},
		{"yb-yibaogl", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibaogl.git"},
		{"yb-jiangxi", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao-jiangxi.git"},
		{"yb-henan", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao-henan.git"},
		{"yb-hunan", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao-hunan.git"},
		{"yb-anhui", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao-anhui.git"},
		{"yb-zhejiang", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao-zhejiang.git"},
		{"yb-xinjiang", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao-xinjiang.git"},
		{"winbff-bingan", "ssh://git@192.168.1.206/df-his-backend/bff/df-bff-bingan.git"},
		{"yb-shan3xi", "ssh://git@192.168.1.206/df-his-yb/df-mic-yibao-shan3xi.git"},
		// Vue frontend apps
		{"web-zhushujugl", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-zhushujugl.git"},
		{"web-opm", "ssh://git@192.168.1.206/df-his-op/df-his-op-web.git"},
		{"web-cdss", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-guizeyq.git"},
		{"web-yibaogl", "ssh://git@192.168.1.206/df-his-yb/df-web-yibaogl.git"},
		{"web-cdr", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-cdr.git"},
		{"web-binglizk", "ssh://git@192.168.1.206/df-his-frontend/ylgl/df-web-binglizk.git"},
		{"web-chuanranbingbk", "ssh://git@192.168.1.206/df-his-frontend/ylgl/df-web-chuanranbingbk.git"},
		{"web-yaoku", "ssh://git@192.168.1.206/df-his-frontend/ykf/df-web-yaoku.git"},
		{"web-biaodangl", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-biaodangl.git"},
		{"web-biaodanbjq", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-biaodanbjq.git"},
		{"web-buliangsj", "ssh://git@192.168.1.206/df-his-frontend/ylgl/df-web-buliangsj.git"},
		{"web-bingangl", "ssh://git@192.168.1.206/df-his-frontend/ylgl/df-web-bingangl.git"},
		{"web-main", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-main.git"},
		{"web-zhuyuanhsz", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-zhuyuanhsz.git"},
		{"web-guahaosf", "ssh://git@192.168.1.206/df-his-frontend/cw/df-web-guahaosf.git"},
		{"web-report", "ssh://git@192.168.1.206/df-report/df-web-report.git"},
		{"web-message", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-message.git"},
		{"web-mobanbjq", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-mobanbjq.git"},
		{"web-yaofang", "ssh://git@192.168.1.206/df-his-frontend/ykf/df-web-yaofang.git"},
		{"web-menzhenysz", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-menzhenysz.git"},
		{"web-pishi", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-pishisy.git"},
		{"web-shouma", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-shoumagl.git"},
		{"web-yiji", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-yijigl.git"},
		{"web-yewugymk", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-yewugymk.git"},
		{"web-zhuyuanysz", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-zhuyuanysz.git"},
		{"web-zhuyuansf", "ssh://git@192.168.1.206/df-his-frontend/cw/df-web-zhuyuansf.git"},
		{"web-ruyuanzbzx", "ssh://git@192.168.1.206/df-his-frontend/lc/df-web-ruyuanzbzx.git"},
		{"web-gyql", "ssh://git@192.168.1.206/df-his-frontend/cw/df-web-gyql.git"},
		{"web-wuzi", "ssh://git@192.168.1.206/df-his-frontend/ykf/df-web-wuzi.git"},
		{"web-cunyi", "ssh://git@192.168.1.206/df-his-frontend/ylgl/df-web-cunyigzz.git"},
		{"web-kangfu", "ssh://git@192.168.1.206/df-his-frontend/ylgl/df-web-kangfuzl.git"},
		{"web-bui", "ssh://git@192.168.1.206/df-his-frontend/base/df-web-bui.git"},
	}

	inserted := 0
	for _, e := range entries {
		// Check if already exists
		var existing model.Application
		if DB.Where("app_name = ?", e.Name).First(&existing).Error == nil {
			continue // already exists, skip
		}

		app := model.Application{
			AppName: e.Name,
			GitRepo: e.GitRepo,
			Enabled: true,
		}

		// Determine app type
		if len(e.Name) >= 4 && e.Name[:4] == "web-" {
			app.AppType = "vue"

			// Determine vue role
			if mainApps[e.Name] {
				app.VueRole = "main"
			} else if code, ok := zipNameMap[e.Name]; ok {
				app.VueRole = "sub"
				app.AppCode = code
			} else {
				// Unknown web-* app not in zip_name_map, default to main
				app.VueRole = "main"
			}
		} else {
			app.AppType = "java"
		}

		// Derive artifact name
		app.ArtifactName = app.DeriveArtifactName()

		DB.Create(&app)
		inserted++
	}

	if inserted > 0 {
		logger.Log.Infof("Seeded %d new applications (skipped %d existing)", inserted, len(entries)-inserted)
	}
}
