import assert from 'node:assert/strict'
import { selectedDeployableFileNames } from './compiled/batch-deploy-selection.js'

const results = [
  { fileName: 'his-gateway.jar', matched: true, valid: true, skipped: false },
  { fileName: 'broken.jar', matched: true, valid: false, skipped: false },
  { fileName: 'unknown.jar', matched: false, valid: true, skipped: false },
  { fileName: '15.1-sql.zip', matched: false, valid: true, skipped: true },
  { fileName: 'df-dataforge.jar', matched: true, valid: true, skipped: false },
]

assert.deepEqual(
  selectedDeployableFileNames(results),
  ['his-gateway.jar', 'df-dataforge.jar'],
)

// Consumed uploads stay unselectable; a fresh upload with the same name can be selected.
assert.deepEqual(selectedDeployableFileNames([
  { fileName: 'used.zip', matched: true, valid: true, updateUsed: true },
  { fileName: 'fresh.zip', matched: true, valid: true, updateUsed: false },
]), ['fresh.zip'])
assert.deepEqual(selectedDeployableFileNames([
  { fileName: 'used.zip', matched: true, valid: true, updateUsed: false },
]), ['used.zip'])

const { buildDeployConfirmation } = await import('./compiled/batch-deploy-selection.js')
const standalone = buildDeployConfirmation([
  { fileName: 'web-cdr.zip', appName: 'web-cdr', appType: 'vue', vueRole: 'standalone' },
  { fileName: 'web-opm.zip', appName: 'web-opm', appType: 'vue', vueRole: 'standalone' },
])
assert.match(standalone, /独立更新/)
assert.doesNotMatch(standalone, /web-main|apps\/\{appCode\}/)
const shared = buildDeployConfirmation([
  { fileName: 'web-main.zip', appName: 'web-main', appType: 'vue', vueRole: 'main' },
  { fileName: '04.zip', appName: 'web-alpha', appType: 'vue', vueRole: 'sub', appCode: '04' },
  { fileName: 'service.jar', appName: 'service', appType: 'java' },
])
assert.match(shared, /独立更新.*\nservice：service.jar/)
assert.match(shared, /合并更新 web-main/)
assert.match(shared, /子应用 04：04.zip/)
assert.match(shared, /同一个子应用不能同时来自主应用包和独立 ZIP/)

const automatic = buildDeployConfirmation([
  { fileName: 'service.jar', appName: 'service', appType: 'java' },
], true)
assert.match(automatic, /自动滚动更新/)
assert.doesNotMatch(automatic, /仅制作并推送镜像/)
assert.match(standalone, /镜像就绪后单独提交滚动更新/)
