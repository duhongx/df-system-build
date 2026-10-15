import assert from 'node:assert/strict'
import fs from 'node:fs'

const view = fs.readFileSync(new URL('../views/BatchDeployView.vue', import.meta.url), 'utf8')
const router = fs.readFileSync(new URL('../router/index.ts', import.meta.url), 'utf8')
const layout = fs.readFileSync(new URL('../layouts/DefaultLayout.vue', import.meta.url), 'utf8')

assert.match(view, /制品来源/, 'batch deploy page should expose artifact source selection')
assert.match(view, /离线上传/, 'batch deploy page should support local upload in the same workflow')
assert.match(view, /服务器下载/, 'batch deploy page should support server download in the same workflow')
assert.match(view, /应用匹配/, 'batch deploy page should keep application matching in the same workflow')
assert.match(view, /镜像制作与更新/, 'batch deploy page should expose build and deploy actions in the same workflow')
assert.match(view, /startDownloadRemoteDir/, 'batch deploy page should start server download without leaving the page')
assert.match(view, /executeBatchDeploy/, 'batch deploy page should create deploy pipelines from the same page')
assert.doesNotMatch(view, /制品上传和服务器下载在制品管理中处理/, 'batch deploy page should no longer tell users to switch pages for import')

assert.match(router, /path: 'batch-deploy'.+meta: \{ title: '新增更新任务'/s, 'batch deploy route should be renamed to the integrated update task')
assert.match(layout, /index="\/batch-deploy">新增更新任务</, 'sidebar should expose the integrated update task as the primary entry')
