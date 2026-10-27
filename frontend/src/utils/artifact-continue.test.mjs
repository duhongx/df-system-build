import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { parse, compileScript } from '@vue/compiler-sfc'
import { transformSync } from 'esbuild'
import * as vue from 'vue'

const { descriptor } = parse(fs.readFileSync(new URL('../views/ArtifactVersionDetailView.vue', import.meta.url), 'utf8'))
const script = compileScript(descriptor, { id: 'artifact-continue' }).content
const { code } = transformSync(script, { loader: 'ts', format: 'cjs', target: 'es2020' })
function harness() {
  const navigation = [], module = { exports: {} }
  const imports = {
    '../components/ReleaseVersionInfo.vue': {},
    vue: { ...vue, onMounted() {}, onUnmounted() {} },
    'vue-router': { useRoute: () => ({ params: { versionNo: 'version-1' } }), useRouter: () => ({ push: route => navigation.push(route) }) },
    'element-plus': {}, '../api/batch-deploy': {}, '../utils/time': {},
  }
  vm.runInNewContext(code, { module, exports: module.exports, require: name => {
    assert.ok(Object.hasOwn(imports, name), name)
    return imports[name]
  } })
  const view = module.exports.default.setup({}, { expose() {} })
  view.version.value = { versionNo: 'version-1', status: 'ready' }
  view.items.value = [{ id: 1, deployable: true, updateUsed: false }]
  return { view, navigation }
}

test('continue unused uploaded artifacts opens matching without submitting a build', () => {
  const { view, navigation } = harness()
  assert.equal(view.canContinueUpdate.value, true)
  view.continueUpdate()
  assert.equal(navigation.length, 1)
  assert.equal(navigation[0].path, '/batch-deploy')
  assert.equal(navigation[0].query.batchId, 'version-1')
})

test('used, incomplete or deployed records cannot start another update', () => {
  for (const mutate of [
    view => { view.items.value[0].updateUsed = true },
    view => { view.items.value.push({ id: 2, deployable: true, updateUsed: true }) },
    view => { view.items.value[0].deployable = false },
    view => { view.version.value.status = 'uploading' },
    view => { view.deployBatch.value = { id: 2 } },
  ]) {
    const { view, navigation } = harness()
    mutate(view)
    assert.equal(view.canContinueUpdate.value, false)
    view.continueUpdate()
    assert.equal(navigation.length, 0)
  }
})
