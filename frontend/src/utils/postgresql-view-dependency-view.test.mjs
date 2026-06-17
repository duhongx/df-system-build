import fs from 'node:fs'
import assert from 'node:assert/strict'

const api = fs.readFileSync(new URL('../api/postgresql.ts', import.meta.url), 'utf8')
const router = fs.readFileSync(new URL('../router/index.ts', import.meta.url), 'utf8')
const layout = fs.readFileSync(new URL('../layouts/DefaultLayout.vue', import.meta.url), 'utf8')
const viewPath = new URL('../views/PostgreSQLViewDependencyChangeView.vue', import.meta.url)
const view = fs.existsSync(viewPath) ? fs.readFileSync(viewPath, 'utf8') : ''

assert.match(api, /SQLViewDependencyTask/, 'PostgreSQL API should expose view dependency task types')
assert.match(api, /createSQLViewDependencyTask/, 'PostgreSQL API should create view dependency tasks')
assert.match(api, /analyzeSQLViewDependencyTask/, 'PostgreSQL API should analyze view dependency tasks')
assert.match(api, /precheckSQLViewDependencyTask/, 'PostgreSQL API should precheck view dependency tasks')
assert.match(api, /executeSQLViewDependencyTask/, 'PostgreSQL API should short-lock execute view dependency tasks')
assert.match(api, /restoreSQLViewDependencyTask/, 'PostgreSQL API should restore view dependency tasks')
assert.match(api, /exportSQLViewDependencyPlan/, 'PostgreSQL API should export view dependency plans')
assert.match(api, /exportSQLViewDependencyRestorePlan/, 'PostgreSQL API should export view dependency restore plans')

assert.match(router, /postgresql\/view-dependency-change/, 'router should expose PostgreSQL view dependency change page')
assert.match(router, /PostgreSQLViewDependencyChangeView/, 'router should load PostgreSQL view dependency change view')
assert.match(layout, /\/postgresql\/view-dependency-change/, 'PostgreSQL menu should link to view dependency change page')

assert.match(view, /视图依赖变更/, 'page should be titled for view dependency changes')
assert.match(view, /分析依赖/, 'page should expose dependency analysis action')
assert.match(view, /执行预检/, 'page should expose precheck action')
assert.match(view, /短锁执行/, 'page should expose explicit short-lock execution action')
assert.match(view, /恢复视图/, 'page should expose restore action')
assert.match(view, /导出执行计划/, 'page should expose execution plan export action')
assert.match(view, /导出恢复 SQL/, 'page should expose restore SQL export action')
assert.match(view, /schemaName/, 'page should collect schema name')
assert.match(view, /columnName/, 'page should collect column name')
assert.match(view, /高风险确认/, 'short-lock execution should require explicit risk confirmation')
assert.match(view, /refreshCurrentTask/, 'page should refresh selected task after backend state changes')
assert.match(view, /catch \(err\)/, 'page should handle failed execution responses explicitly')
