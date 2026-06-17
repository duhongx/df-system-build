import fs from 'node:fs'
import assert from 'node:assert/strict'

const api = fs.readFileSync(new URL('../api/postgresql.ts', import.meta.url), 'utf8')
const router = fs.readFileSync(new URL('../router/index.ts', import.meta.url), 'utf8')
const layout = fs.readFileSync(new URL('../layouts/DefaultLayout.vue', import.meta.url), 'utf8')
const viewPath = new URL('../views/PostgreSQLViewDependencyChangeView.vue', import.meta.url)
const view = fs.existsSync(viewPath) ? fs.readFileSync(viewPath, 'utf8') : ''

assert.match(api, /SQLViewDependencyTask/, 'PostgreSQL API should expose view dependency task types')
assert.match(api, /executionMode/, 'PostgreSQL API should expose view dependency execution mode')
assert.match(api, /createSQLViewDependencyTask/, 'PostgreSQL API should create view dependency tasks')
assert.match(api, /analyzeSQLViewDependencyTask/, 'PostgreSQL API should analyze view dependency tasks')
assert.match(api, /precheckSQLViewDependencyTask/, 'PostgreSQL API should precheck view dependency tasks')
assert.match(api, /executeSQLViewDependencyTask/, 'PostgreSQL API should execute view dependency tasks')
assert.match(api, /restoreSQLViewDependencyTask/, 'PostgreSQL API should restore view dependency tasks')
assert.match(api, /exportSQLViewDependencyPlan/, 'PostgreSQL API should export view dependency plans')
assert.match(api, /exportSQLViewDependencyRestorePlan/, 'PostgreSQL API should export view dependency restore plans')

assert.match(router, /postgresql\/view-dependency-change/, 'router should expose PostgreSQL view dependency change page')
assert.match(router, /PostgreSQLViewDependencyChangeView/, 'router should load PostgreSQL view dependency change view')
assert.match(layout, /\/postgresql\/view-dependency-change/, 'PostgreSQL menu should link to view dependency change page')

assert.match(view, /字段变更助手/, 'page should be titled around the field-change workflow')
assert.match(view, /事务执行/, 'page should expose transaction execution mode')
assert.match(view, /分步执行/, 'page should expose step execution mode')
assert.match(view, /分析依赖/, 'page should expose dependency analysis action')
assert.match(view, /执行预检/, 'page should expose precheck action')
assert.match(view, /执行变更/, 'page should expose a clear field-change execution action')
assert.doesNotMatch(view, /短锁执行/, 'page should not expose the unclear short-lock execution wording')
assert.match(view, /恢复视图/, 'page should expose restore action')
assert.match(view, /导出执行 SQL/, 'page should expose execution SQL export action')
assert.match(view, /导出恢复 SQL/, 'page should expose restore SQL export action')
assert.match(view, /目标 Schema/, 'page should collect schema name with an operator-facing label')
assert.match(view, /目标字段/, 'page should collect column name with an operator-facing label')
assert.match(view, /执行确认/, 'field change execution should require explicit confirmation')
assert.match(view, /refreshCurrentTask/, 'page should refresh selected task after backend state changes')
assert.match(view, /catch \(err\)/, 'page should handle failed execution responses explicitly')
