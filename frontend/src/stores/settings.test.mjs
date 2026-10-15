import assert from 'node:assert/strict'
import fs from 'node:fs'
import ts from 'typescript'
import { createPinia, setActivePinia } from 'pinia'

// Execute the real store with only its HTTP dependency replaced.
let source = fs.readFileSync(new URL('./settings.ts', import.meta.url), 'utf8')
source = source.replace("import { getSettings } from '../api/settings'", `const getSettings = async () => {
  if (globalThis.settingsTestResponse instanceof Error) throw globalThis.settingsTestResponse
  return globalThis.settingsTestResponse
}`)
for (const dependency of ['vue', 'pinia']) {
  source = source.replace(`from '${dependency}'`, `from '${import.meta.resolve(dependency)}'`)
}
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText
const { useSettingsStore } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)
setActivePinia(createPinia())
const settings = useSettingsStore()
assert.equal(settings.showTaskList, false)
for (const [response, enabled] of [
  [{ customer_env: '本地', deploy_source: 'artifact' }, true],
  [{ customer_env: ' 本地 ', deploy_source: 'invalid' }, true],
  [{ customer_env: '浙江', deploy_source: 'both' }, false],
  [{ customer_env: 'local', deploy_source: 'source' }, false],
  [{ customer_env: '', deploy_source: 'both' }, false],
  [{ deploy_source: 'both' }, false],
  [{ customer_env: '本地' }, true],
  [new Error('settings unavailable'), false],
]) {
  globalThis.settingsTestResponse = response
  await settings.fetchSettings()
  assert.equal(settings.showTaskList, enabled, JSON.stringify(response))
  assert.equal(settings.autoRollout, enabled, JSON.stringify(response))
}
delete globalThis.settingsTestResponse
console.log('Environment-driven source entry and failure handling passed')
