import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { parse, compileScript } from '@vue/compiler-sfc'
import { transformSync } from 'esbuild'
import * as vue from 'vue'

function evaluate(source, imports = {}) {
  const module = { exports: {} }
  const { code } = transformSync(source, { loader: 'ts', format: 'cjs', target: 'es2020' })
  vm.runInNewContext(code, { module, exports: module.exports, Error, require(name) {
    assert.ok(Object.hasOwn(imports, name), `unexpected dependency ${name}`)
    return imports[name]
  } })
  return module.exports
}
const { descriptor } = parse(fs.readFileSync(new URL('../views/PostgreSQLViewDependencyChangeView.vue', import.meta.url), 'utf8'))
const script = compileScript(descriptor, { id: 'view-task-navigation' }).content
const progress = evaluate(fs.readFileSync(new URL('./sql-view-progress.ts', import.meta.url), 'utf8'))
const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve() }
const deferred = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b }); return { promise, resolve, reject } }
const task = (id = 8) => ({ id, alterSql: `ALTER TABLE sql_release.t${id} ALTER COLUMN amount TYPE numeric(18,7)`, status: 'CREATED', sourceFileId: 67, sourceFileName: 'original.sql', sourceSyncStatus: 'WAITING', executionMode: 'TRANSACTION', updatedAt: '2026-10-25T12:00:00Z' })
const result = (id = 8) => ({ task: task(id), items: [], changes: [{ id: 1, taskId: id, schemaName: 'sql_release', tableName: 't', columnName: 'amount', status: 'CREATED' }], steps: [] })

function harness(options = {}) {
  const route = vue.reactive({ query: options.query || {} })
  const navigation = [], reads = [], lists = [], messages = [], mounted = [], unmount = [], stops = []
  let writes = 0
  const router = {
    async push(value) { navigation.push(value); route.query = value.query || {} },
    async replace(value) { navigation.push(value); route.query = value.query || {} },
  }
  const api = {
    async listSQLViewDependencyTasks(page, size) { lists.push({ page, size }); return options.list ? options.list(page, size) : { list: [task()], total: 1 } },
    async getSQLViewDependencyTask(id) { reads.push(id); return options.get ? options.get(id) : result(id) },
    async createSQLViewDependencyTask() { writes++; throw new Error('unexpected task creation') },
    async analyzeSQLViewDependencyTask() { writes++; if (options.analyze) return options.analyze(); throw new Error('unexpected SQL execution') },
    async executeSQLViewDependencyTask() { writes++; throw new Error('unexpected SQL execution') },
  }
  const view = evaluate(script, {
    vue: { ...vue, onMounted: callback => mounted.push(callback), onBeforeUnmount: callback => unmount.push(callback), watch(...args) { const stop = vue.watch(...args); stops.push(stop); return stop } },
    'vue-router': { useRoute: () => route, useRouter: () => router },
    'element-plus': { ElMessage: Object.fromEntries(['success', 'error', 'warning'].map(type => [type, value => messages.push({ type, value })])), ElMessageBox: {} },
    '../api/postgresql': api,
    '../api/settings': { getSettings: async () => ({}) },
    '../utils/sql-view-progress': { ...progress, createViewTaskPoller: (read, apply, failed) => ({ stop() {}, async start(id) { if (options.pollReads) { try { apply(await read(id)) } catch (error) { failed(error) } } } }) },
  }).default.setup({}, { expose() {} })
  return { view, route, navigation, reads, lists, messages, get writes() { return writes }, async mount() { for (const callback of mounted) await callback(); await flush() }, dispose() { unmount.forEach(callback => callback()); stops.forEach(stop => stop()) } }
}

test('menu entry discovers the saved linked task and opening it reads the original SQL without executing', async () => {
  const h = harness()
  try {
    await h.mount()
    assert.equal(h.view.historyVisible.value, true)
    assert.equal(h.view.historyTasks.value[0].id, 8)
    assert.equal(h.view.historyTotal.value, 1)
    assert.equal(h.reads.length, 0, 'list must not implicitly select or execute an arbitrary task')
    await h.view.openSavedTask(8); await flush()
    assert.equal(h.view.currentTask.value.id, 8)
    assert.equal(h.view.form.value.alterSql, task().alterSql)
    assert.equal(h.view.historyVisible.value, false)
    assert.equal(h.writes, 0)
    h.route.query = {}; await flush()
    assert.equal(h.view.currentTask.value, null)
    assert.equal(h.view.historyVisible.value, true, 'returning through the menu must reopen saved tasks')
    assert.equal(h.view.historyTasks.value[0].id, 8)
  } finally { h.dispose() }
})

test('task 9 shows original failed SQL after rollback without executing anything', async () => {
  const data = result(9)
  data.task.status = 'FAILED'; data.task.analyzedAt = '2026-10-25T13:00:00Z'; data.task.executeMessage = '事务执行失败'
  data.steps = [
    { id: 1, stepOrder: 1, stepName: 'ALTER_TABLE', status: 'ROLLED_BACK', sqlContent: 'ALTER TABLE a ALTER COLUMN x TYPE bigint', errorMessage: '事务已回滚，本步骤未提交' },
    { id: 2, stepOrder: 2, stepName: 'ALTER_TABLE', status: 'FAILED', sqlContent: 'ALTER TABLE a ALTER COLUMN y TYPE integer', errorMessage: 'invalid input syntax\nSQLSTATE: 22P02' },
    { id: 3, stepOrder: 3, stepName: 'ROLLBACK', status: 'SUCCESS', sqlContent: 'ROLLBACK' },
  ]
  const h = harness({ query: { taskId: '9' }, get: async () => data })
  try {
    await h.mount()
    assert.match(h.view.failureDetails.value.report, /SQLSTATE: 22P02/)
    assert.match(h.view.failureDetails.value.report, /ALTER TABLE a ALTER COLUMN y/)
    assert.match(h.view.failureDetails.value.outcome, /已记录事务回滚/)
    assert.match(h.view.executionStages.value.find(stage => stage.key === 'alter').error, /22P02/)
    assert.equal(h.writes, 0)
  } finally { h.dispose() }
})

test('analyze failure reloads persisted status and leaves an error panel after the toast', async () => {
  let failed = false
  const h = harness({ query: { taskId: '9' }, pollReads: true,
    get: async () => { const data = result(9); if (failed) { data.task.status = 'FAILED'; data.task.executeMessage = '分析依赖失败：备份 s.some_view 元数据失败：context deadline exceeded' } return data },
    analyze: async () => { failed = true; throw new Error('analysis request failed') },
  })
  try {
    await h.mount()
    await h.view.handleAnalyze(); await flush()
    assert.equal(h.view.currentTask.value.status, 'FAILED')
    assert.match(h.view.failureDetails.value.report, /s.some_view.*context deadline exceeded/)
    assert.match(h.view.failureDetails.value.report, /analysis request failed/)
    assert.equal(h.view.analyzing.value, false)
    assert.equal(h.writes, 1)
  } finally { h.dispose() }
})

test('direct linked-task navigation loads a saved draft and does not claim a backup already exists', async () => {
  const h = harness({ query: { taskId: '8' } })
  try {
    await h.mount()
    assert.equal(h.view.currentTask.value.id, 8)
    assert.equal(h.view.changeCount.value, 1)
    assert.equal(h.view.executionStages.value[0].status, 'pending')
    assert.equal(h.view.historyVisible.value, false)
    assert.equal(h.writes, 0)
  } finally { h.dispose() }
})

test('detail read errors remain visible, clear stale SQL and prevent duplicate creation until retry succeeds', async () => {
  let fail = true
  const h = harness({ query: { taskId: '8' }, get: async id => { if (fail) throw new Error('record not found'); return result(id) } })
  try {
    h.view.form.value.alterSql = 'stale SQL from another task'
    await h.mount()
    assert.equal(h.view.currentTask.value, null)
    assert.equal(h.view.form.value.alterSql, '')
    assert.match(h.view.routeTaskError.value, /#8.*record not found/)
    assert.equal(h.view.sameTaskBlocked.value, true)
    h.view.form.value.alterSql = task().alterSql
    assert.equal(await h.view.ensureTaskForCurrentSQL(), null)
    assert.equal(h.writes, 0)
    fail = false
    await h.view.openSavedTask(8); await flush()
    assert.equal(h.view.currentTask.value.id, 8)
    assert.equal(h.view.routeTaskError.value, '')
    assert.equal(h.writes, 0)
  } finally { h.dispose() }
})

test('a task list failure is reported as a read error and can be retried', async () => {
  let fail = true
  const h = harness({ list: async () => { if (fail) throw new Error('network down'); return { list: [task()], total: 1 } } })
  try {
    await h.mount()
    assert.match(h.view.historyError.value, /network down/)
    assert.equal(h.view.historyLoaded.value, false)
    fail = false
    await h.view.openTaskHistory()
    assert.equal(h.view.historyError.value, '')
    assert.equal(h.view.historyTasks.value.length, 1)
    assert.equal(h.writes, 0)
  } finally { h.dispose() }
})

test('task pagination discards stale and unmounted responses', async () => {
  const old = deferred(), current = deferred(), leaving = deferred()
  const h = harness({ list: page => page === 1 ? old.promise : page === 2 ? current.promise : leaving.promise })
  try {
    const first = h.view.loadTaskHistory()
    h.view.historyPage.value = 2
    const second = h.view.loadTaskHistory()
    current.resolve({ list: [task(9)], total: 40 }); await second
    old.resolve({ list: [task(8)], total: 40 }); await first
    assert.equal(h.view.historyTasks.value[0].id, 9)
    assert.equal(h.lists[1].page, 2)
    h.view.historyPage.value = 3
    const third = h.view.loadTaskHistory()
    h.dispose()
    leaving.resolve({ list: [task(10)], total: 40 }); await third
    assert.equal(h.view.historyTasks.value[0].id, 9)
  } finally { h.dispose() }
})

test('late detail responses cannot replace the newly selected task', async () => {
  const old = deferred()
  const h = harness({ query: { taskId: '8' }, get: id => id === 8 ? old.promise : Promise.resolve(result(id)) })
  try {
    const loading = h.view.loadRouteTask()
    h.route.query = { taskId: '9' }; await flush()
    assert.equal(h.view.currentTask.value.id, 9)
    old.resolve(result(8)); await loading
    assert.equal(h.view.currentTask.value.id, 9)
    assert.equal(h.view.form.value.alterSql, task(9).alterSql)
    assert.equal(h.writes, 0)
  } finally { h.dispose() }
})
