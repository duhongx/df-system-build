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
function component(name, props, api, events = [], navigation = []) {
  const { descriptor } = parse(fs.readFileSync(new URL(`../components/${name}.vue`, import.meta.url), 'utf8'))
  const script = compileScript(descriptor, { id: 'whole-file-test' }).content
  return evaluate(script, {
    vue: { ...vue, onMounted() {}, onBeforeUnmount() {}, watch() {} },
    'vue-router': { useRouter: () => ({ push: async route => { navigation.push(route) } }) },
    'element-plus': { ElMessage: { success() {} } },
    '../api/postgresql': api,
    './SQLRetryAccountDialog.vue': {}, './SQLViewDependencyTransferDialog.vue': {},
  }).default.setup(props, { expose() {}, emit: (...event) => events.push(event) })
}

test('100 displayed failures can open the whole 879-statement file across pages', async () => {
  const props = { options: {}, busy: false }
  const v = component('SQLCurrentFailures', props, {
    listCurrentFailedSQL: async () => ({ list: Array.from({ length: 100 }, (_, i) => ({ id: i + 1, fileId: 67 })), total: 879 }),
  })
  await v.load()
  assert.equal(v.rows.value.length, 100)
  assert.equal(v.total.value, 879)
  v.transferSelection.value = v.rows.value.slice()
  v.transferWholeFile()
  assert.equal(v.transferTarget.value.fileId, 67)
  assert.equal(v.transferTarget.value.all, true)
  assert.equal(v.transferTarget.value.statementIds, undefined, 'whole file cannot be limited to the 100 visible IDs')
  v.transferTarget.value = null
  v.transferSelection.value = [{ id: 1, fileId: 67 }, { id: 2, fileId: 202 }]
  v.transferWholeFile()
  assert.equal(v.transferTarget.value, null, 'different files must not be silently merged')
  v.transferWholeFile(67)
  assert.equal(v.transferTarget.value.all, true, 'row action works without selecting the current page')
  v.transferTarget.value = null
  props.busy = true
  v.transferWholeFile(67)
  assert.equal(v.transferTarget.value, null)
})

test('file scope keeps the whole-file action available after changing pages', async () => {
  const v = component('SQLCurrentFailures', { options: {} }, {
    listCurrentFailedSQL: async (_page, _size, filter) => {
      assert.equal(filter.fileId, 67)
      return { list: [], total: 879 }
    },
  })
  await v.showFile(67, 'large.sql')
  v.page.value = 5
  await v.load()
  assert.equal(v.transferSelection.value.length, 0)
  v.transferWholeFile()
  assert.equal(v.transferTarget.value.fileId, 67)
  assert.equal(v.transferTarget.value.all, true)
})

for (const excluded of [0, 100]) {
  test(`whole-file preview submits ${879 - excluded} eligible changes once and excludes ${excluded} linked rows`, async () => {
    const units = Array.from({ length: 879 - excluded }, (_, i) => ({ id: excluded + i + 1, changeCount: 1, tables: ['bulk'], transaction: false, contextKey: 'same-context' }))
    const requests = [], events = []
    const apiSource = fs.readFileSync(new URL('../api/postgresql.ts', import.meta.url), 'utf8')
    const api = evaluate(apiSource, { './request': { post: async (path, body, config) => {
      requests.push({ path, body })
      assert.equal(config.timeout, 0, 'large previews/transfers must not time out at the default 30 seconds')
      assert.equal(body.all, true)
      assert.equal(body.statementIds, undefined)
      if (path.endsWith('/preview')) return { fileId: 67, fileName: 'large.sql', units, excluded: Array(excluded).fill({ reason: 'already linked' }), previewHash: 'reviewed-source' }
      return { id: 9 }
    } } })
    const v = component('SQLViewDependencyTransferDialog', { target: { fileId: 67, all: true } }, api, events)
    v.table.value = { toggleAllSelection() { v.selection.value = v.preview.value.units.slice() } }
    await v.load()
    assert.equal(v.changeCount.value, 879 - excluded)
    assert.equal(v.canCreate.value, true)
    await v.create()
    assert.equal(requests.length, 2, 'one preview plus one creation, not one operation per page')
    assert.equal(requests[1].body.selectedIds.length, 879 - excluded)
    assert.equal(requests[1].body.selectedIds[0], excluded + 1)
    assert.equal(requests[1].body.previewHash, 'reviewed-source')
    assert.equal(events[0][0], 'created')
  })
}

test('whole-file selection retains original transaction and session-context boundaries', async () => {
  const v = component('SQLViewDependencyTransferDialog', { target: { fileId: 67, all: true } }, {})
  v.preview.value = { units: [], excluded: [], previewHash: 'fixture' }
  v.selection.value = [{ transaction: true, contextKey: 'a', tables: [], changeCount: 1 }, { transaction: false, contextKey: 'a', tables: [], changeCount: 1 }]
  assert.equal(v.canCreate.value, false)
  v.selection.value = [{ transaction: false, contextKey: 'a', tables: [], changeCount: 1 }, { transaction: false, contextKey: 'b', tables: [], changeCount: 1 }]
  assert.equal(v.canCreate.value, false)
})

test('already-linked preview exposes the original tasks and opens them without another transfer', async () => {
  const events = [], navigation = []
  const v = component('SQLViewDependencyTransferDialog', { target: { fileId: 67, all: true } }, {
    previewSQLFileViewTransfer: async () => ({ fileId: 67, fileName: 'linked.sql', units: [], excluded: [{ id: 1, taskIds: [8] }, { id: 2, taskIds: [8, 9] }], previewHash: 'unchanged' }),
    transferSQLFileToViewTask: async () => { throw new Error('must not create another task') },
  }, events, navigation)
  await v.load()
  assert.deepEqual(Array.from(v.linkedTaskIDs.value), [8, 9])
  assert.deepEqual(Array.from(v.excludedExpanded.value), ['excluded'])
  assert.equal(v.canCreate.value, false)
  await v.openLinkedTask(8)
  assert.equal(navigation[0].query.taskId, '8')
  assert.equal(events[0][0], 'close')
})
