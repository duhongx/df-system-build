# 192.168.1.150 Jenkins 任务迁移结果

已对照 101 个 Jenkins 任务：97 个应用构建映射到 df-build-system，其中新增 96 个、复用 1 个；94 个启用，3 个因原配置异常停用。另 4 个批量打包或固定历史提交号专项任务完整归档，未转换为可执行的普通应用任务。

平台现有 98 个任务、108 个应用；保留原有 agg-service-prod 和 yb-yibaogl-prod 的 ID 与历史。新建 4 个应用，修正 1 个仓库路径，保留 web-cdr、web-opm 的独立编译命令。

仓库/分支核验使用平台实际 SSH 密钥，95/97 通过；未触发业务构建、镜像推送或滚动更新。原 Jenkins 配置、进程和当前集群镜像未改变。

## 异常任务

- `df-buliangsj-test`：仓库中不存在原分支；原发布目标 Deployment 不存在。
- `web-biaodanbjq-test`：缺少前端部署映射，不能推断子应用路径；原分支没有 build:new，且原脚本未定义其 web-main 合并路径。
- `web-liuguanhsz-test`：Jenkins 应用配置缺失，原任务也报 KeyError；缺少前端部署映射，不能推断子应用路径；Jenkins 的应用配置缺失。

## 专项配置归档

- `his-ops-frontend-release-build`
- `his-ops-release-build`
- `qc-dependency-topology-20261008-compile-only`
- `qc-dependency-topology-20261008-runtime-check`

原始配置与数据库备份在服务器 `/opt/df-build-server/migrations/jenkins-20261015T094847/`，仅 root 可读。

## 配置验证

迁移脚本 6 项测试通过；7 项独立 PostgreSQL 用例通过，覆盖停用拦截、仅镜像执行、前端命令差异及滚动更新。整库副本导入和重复导入通过，重复执行不新增记录、不改写历史；浏览器核对停用任务不能执行或批量选择。业务项目的实际编译、启动和发布未在本次迁移中执行。

## 任务映射

| Jenkins 任务 | 平台任务 ID | 平台任务 | 分支 | 状态 |
|---|---:|---|---|---|
| agg-bingangl-test | 3 | agg-bingangl-prod | RC_2.15.2 | 启用 |
| agg-feiyongkz-test | 4 | agg-feiyongkz-prod | RC_2.16.1_250514 | 启用 |
| agg-jianchagl-test | 5 | agg-jianchagl-prod | RC_2.16.1_250514 | 启用 |
| agg-jianyantmgl-test | 6 | agg-jianyantmgl-prod | RC_2.16.1_250514 | 启用 |
| agg-linchuangmz-test | 7 | agg-linchuangmz-prod | RC_2.16.1_250514 | 启用 |
| agg-shenqingdan-test | 8 | agg-shenqingdan-prod | RC_2.16.1_250514 | 启用 |
| agg-yaokufang-test | 9 | agg-yaokufang-prod | RC_2.16.1_250514 | 启用 |
| agg-yibaogl-test | 10 | agg-yibaogl-prod | RC_2.15.3_250303 | 启用 |
| agg-zhuyuancwgl-test | 11 | agg-zhuyuancwgl-prod | RC_2.16.1_250514 | 启用 |
| agg-zhuyuanfyjf-test | 12 | agg-zhuyuanfyjf-prod | RC_2.16.1_250514 | 启用 |
| agg-zhuyuanyz-test | 13 | agg-zhuyuanyz-prod | RC_2.16.1_250514 | 启用 |
| cdr-test | 14 | cdr-prod | RC_2.16.1_250514 | 启用 |
| cdss-test | 15 | cdss-prod | RC_2.16.1_250514 | 启用 |
| df-adapter-test | 16 | df-adapter-prod | dev | 启用 |
| df-authorization-test | 17 | df-authorization-prod | release_2.15.3_250515 | 启用 |
| df-buliangsj-test | 18 | df-buliangsj-prod | RC_2.16.1_250514 | 停用 |
| df-cdc-test | 19 | df-cdc-prod | cdc-pg | 启用 |
| df-dataforge-test | 20 | df-dataforge-prod | dev | 启用 |
| df-gongzuoliu-test | 21 | df-gongzuoliu-prod | RC_2.16.1_250514 | 启用 |
| df-oapi-danju-test | 22 | df-oapi-danju-prod | dev | 启用 |
| df-oapi-test | 23 | df-oapi-prod | dev_node2_ny | 启用 |
| df-oapi-test-test | 24 | df-oapi-test-prod | dev_node2_ny | 启用 |
| df-office-test | 25 | df-office-prod | dev | 启用 |
| df-sapi-test | 26 | df-sapi-prod | RC_2.16.1_250514 | 启用 |
| df-wuzi-test | 27 | df-wuzi-prod | RC_2.16.1_250514 | 启用 |
| df-zhifu-test | 28 | df-zhifu-prod | RC_2.16.1_250514 | 启用 |
| dfmessage-service-test | 29 | dfmessage-service-prod | RC_2.16.1_250514 | 启用 |
| gy-biaodan-test | 30 | gy-biaodan-prod | RC_2.16.1_250514 | 启用 |
| gy-bingan-test | 31 | gy-bingan-prod | RC_2.16.1_250514 | 启用 |
| gy-bingrenzsy-test | 32 | gy-bingrenzsy-prod | RC_2.16.1_250514 | 启用 |
| gy-jichufw-test | 33 | gy-jichufw-prod | RC_2.16.1_250514 | 启用 |
| gy-mobanku-test | 34 | gy-mobanku-prod | RC_2.16.1_250514 | 启用 |
| gy-renwugl-test | 35 | gy-renwugl-prod | release_2.14.3_240829 | 启用 |
| gy-report-test | 36 | gy-report-prod | RC_2.16.1_250514 | 启用 |
| gy-yiliaogl-test | 37 | gy-yiliaogl-prod | RC_2.16.1_250514 | 启用 |
| his-gateway-test | 38 | his-gateway-prod | RC_2.16.1_250514 | 启用 |
| hisbff-cdr-test | 39 | hisbff-cdr-prod | RC_2.16.1_250514 | 启用 |
| jd-api-test | 40 | jd-api-prod | feature-DFHIS-32451 | 启用 |
| jj-guahao-test | 41 | jj-guahao-prod | RC_2.16.1_250514 | 启用 |
| jj-piaojugl-test | 42 | jj-piaojugl-prod | RC_2.16.1_250514 | 启用 |
| jj-zhuyuan-test | 43 | jj-zhuyuan-prod | RC_2.16.1_250514 | 启用 |
| lc-bingliku-test | 44 | lc-bingliku-prod | RC_2.16.1_250514 | 启用 |
| lc-binglizk-test | 45 | lc-binglizk-prod | RC_2.16.1_250514 | 启用 |
| lc-jibingbk-test | 46 | lc-jibingbk-prod | release_2.14.3_240829 | 启用 |
| lc-menzhenjz-test | 47 | lc-menzhenjz-prod | RC_2.16.1_250514 | 启用 |
| lc-shenqingdanzx-test | 48 | lc-shenqingdanzx-prod | feature-DFHIS-32607 | 启用 |
| lc-zhuyuan-test | 49 | lc-zhuyuan-prod | RC_2.16.1_250514 | 启用 |
| onecenter-service-test | 50 | onecenter-service-prod | RC_2.16.1_250514 | 启用 |
| oss-test | 51 | oss-prod | RC_2.16.1_250514 | 启用 |
| web-biaodanbjq-test | 52 | web-biaodanbjq-prod | RC_2.15.2 | 停用 |
| web-biaodangl-test | 53 | web-biaodangl-prod | RC_2.16.1_250514 | 启用 |
| web-bingangl-butuo-yanshou | 98 | web-bingangl-prod-feature-butuo-yanshou | feature-butuo-yanshou | 启用，仅制作镜像 |
| web-bingangl-test | 54 | web-bingangl-prod | RC_2.16.1_250514 | 启用 |
| web-binglizk-test | 55 | web-binglizk-prod | RC_2.16.1_250514 | 启用 |
| web-bui-test | 56 | web-bui-prod | RC_2.16.1_250514 | 启用 |
| web-buliangsj-test | 57 | web-buliangsj-prod | RC_2.16.1_250514 | 启用 |
| web-cdr-test | 58 | web-cdr-prod | RC_2.16.1_250514 | 启用 |
| web-cdss-test | 59 | web-cdss-prod | RC_2.16.1_250514 | 启用 |
| web-chuanranbingbk-test | 60 | web-chuanranbingbk-prod | RC_2.16.1_250514 | 启用 |
| web-cunyi-test | 61 | web-cunyi-prod | feature-DFHIS-32288 | 启用 |
| web-guahaosf-test | 62 | web-guahaosf-prod | RC_2.16.1_250514 | 启用 |
| web-kangfu-test | 63 | web-kangfu-prod | release_2.15.3_250515 | 启用 |
| web-liuguanhsz-test | 64 | web-liuguanhsz-prod | release_2.2405.12 | 停用 |
| web-main-test | 65 | web-main-prod | RC_2.16.1_250514 | 启用 |
| web-menzhenysz-test | 66 | web-menzhenysz-prod | RC_2.16.1_250514 | 启用 |
| web-message-test | 67 | web-message-prod | RC_2.16.1_250514 | 启用 |
| web-mobanbjq-test | 68 | web-mobanbjq-prod | RC_2.16.1_250514 | 启用 |
| web-opm-test | 69 | web-opm-prod | release_2.15.3_250515 | 启用 |
| web-pishi-test | 70 | web-pishi-prod | RC_2.16.1_250514 | 启用 |
| web-report-test | 71 | web-report-prod | RC_2.16.1_250514 | 启用 |
| web-ruyuanzbzx-test | 72 | web-ruyuanzbzx-prod | RC_2.16.1_250514 | 启用 |
| web-shouma-test | 73 | web-shouma-prod | RC_2.16.1_250514 | 启用 |
| web-wuzi-test | 74 | web-wuzi-prod | RC_2.16.1_250514 | 启用 |
| web-yaofang-test | 75 | web-yaofang-prod | RC_2.16.1_250514 | 启用 |
| web-yaoku-test | 76 | web-yaoku-prod | RC_2.16.1_250514 | 启用 |
| web-yewugymk-test | 77 | web-yewugymk-prod | RC_2.16.1_250514 | 启用 |
| web-yibaogl-test | 78 | web-yibaogl-prod | RC_2.16.1_250514 | 启用 |
| web-yiji-test | 79 | web-yiji-prod | RC_2.16.1_250514 | 启用 |
| web-zhushujugl-test | 80 | web-zhushujugl-prod | RC_2.16.1_250514 | 启用 |
| web-zhuyuanhsz-test | 81 | web-zhuyuanhsz-prod | RC_2.16.1_250514 | 启用 |
| web-zhuyuansf-test | 82 | web-zhuyuansf-prod | RC_2.16.1_250514 | 启用 |
| web-zhuyuanysz-test | 83 | web-zhuyuanysz-prod | RC_2.16.1_250514 | 启用 |
| winbff-bingan-test | 84 | winbff-bingan-prod | RC_2.16.1_250514 | 启用 |
| winbff-binglizk-test | 85 | winbff-binglizk-prod | RC_2.16.1_250514 | 启用 |
| winbff-cunyi-test | 86 | winbff-cunyi-prod | release_2.15.3_250515 | 启用 |
| winbff-guahaosf-test | 87 | winbff-guahaosf-prod | hotfix-DFHIS-32414 | 启用 |
| winbff-jichufw-test | 88 | winbff-jichufw-prod | RC_2.16.1_250514 | 启用 |
| winbff-linchuanggy-test | 89 | winbff-linchuanggy-prod | RC_2.16.1_250514 | 启用 |
| winbff-linchuangmz-test | 90 | winbff-linchuangmz-prod | RC_2.16.1_250514 | 启用 |
| winbff-linchuangzy-test | 91 | winbff-linchuangzy-prod | RC_2.16.1_250514 | 启用 |
| winbff-yaokufang-test | 92 | winbff-yaokufang-prod | RC_2.16.1_250514 | 启用 |
| winbff-yibaogl-test | 93 | winbff-yibaogl-prod | RC_2.16.1_250514 | 启用 |
| winbff-zhuyuansf-test | 94 | winbff-zhuyuansf-prod | RC_2.16.1_250514 | 启用 |
| yb-moni-test | 95 | yb-moni-prod | feat-DFHIS-32329 | 启用 |
| yb-service-test | 96 | yb-service-prod | release_2.15.3_250515 | 启用 |
| yb-yibaogl-test | 1 | yb-yibaogl-prod | RC_2.16.1_250514 | 启用 |
| ykf-jichuyw-test | 97 | ykf-jichuyw-prod | RC_2.16.1_250514 | 启用 |

## 页面核对

- [任务列表](task-list.png)
- [停用任务](disabled-task.png)
- [CSV 映射清单](task-mapping.csv)
