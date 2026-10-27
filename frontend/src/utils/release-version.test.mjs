import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { transformSync } from 'esbuild'

const source = fs.readFileSync(new URL('./release-version.ts', import.meta.url), 'utf8')
const { code } = transformSync(source, { loader: 'ts', format: 'cjs', target: 'es2020' })
const module = { exports: {} }
vm.runInNewContext(code, { module, exports: module.exports })
const { releaseVersionContent, matchesReleaseVersion } = module.exports
const plain = value => JSON.parse(JSON.stringify(value))

test('legacy release notes, feature lists and parameter names remain readable', () => {
  const parsed = releaseVersionContent({ releaseManifestJson: JSON.stringify({
    appVersion: '2.15.3', readme: '门诊修复', featureList: ['支持退费', { module: '医保', change: '结算调整' }],
    appParamList: [{ paramName: 'ENABLE_REFUND', paramDesc: '退费开关', paramValue: true }],
    hospital: '滨港医院', business: { scope: ['门诊', '住院'] }, fileList: [{ serviceName: 'web-main' }],
  }) })
  assert.equal(parsed.features[0], '支持退费')
  assert.deepEqual(JSON.parse(parsed.features[1]), { module: '医保', change: '结算调整' })
  assert.deepEqual(plain(parsed.parameters), [{ name: 'ENABLE_REFUND', description: '退费开关', value: 'true' }])
  assert.deepEqual(plain(parsed.businessFields).map(field => field.name), ['hospital', 'business'])
  assert.deepEqual(JSON.parse(parsed.businessFields[1].value), { scope: ['门诊', '住院'] })
})

test('business-only metadata accepts descriptive parameter aliases and custom fields', () => {
  const parsed = releaseVersionContent({ releaseManifestJson: JSON.stringify({
    appVersion: 'v1', featureList: null, appParamList: [{ name: '医保机构', description: '业务说明', value: '机构一' }],
    '业务范围': '门诊结算',
  }) })
  assert.deepEqual(plain(parsed.parameters), [{ name: '医保机构', description: '业务说明', value: '机构一' }])
  assert.deepEqual(plain(parsed.businessFields), [{ name: '业务范围', value: '门诊结算' }])
})

test('search preserves separate uploads of the same business version and finds notes', () => {
  const versions = [
    { versionNo: 'upload-1', appVersion: 'V2.15.3', releaseReadme: '医保结算调整' },
    { versionNo: 'upload-2', appVersion: 'V2.15.3', releaseReadme: '挂号修复' },
    { versionNo: 'upload-3' },
  ]
  assert.equal(versions.filter(version => matchesReleaseVersion(version, ' v2.15.3 ')).length, 2)
  assert.equal(versions.filter(version => matchesReleaseVersion(version, '医保')).length, 1)
  assert.equal(versions.filter(version => matchesReleaseVersion(version, 'upload-3')).length, 1)
  assert.equal(versions.filter(version => matchesReleaseVersion(version, '')).length, 3)
})

test('missing, malformed or unexpected metadata stays readable without breaking record details', () => {
  for (const releaseManifestJson of [undefined, '', '{', 'null', '[]', '{"featureList":123,"appParamList":{}}']) {
    const parsed = releaseVersionContent({ releaseManifestJson })
    assert.deepEqual(plain(parsed), { features: [], parameters: [], businessFields: [] })
  }
})
