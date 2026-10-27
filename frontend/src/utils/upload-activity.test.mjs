import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import { transform } from 'esbuild'

const source = await fs.readFile(new URL('./upload-activity.ts', import.meta.url), 'utf8')
const { code } = await transform(source, { loader: 'ts', format: 'esm' })
const { createUploadActivity, recordUploadActivity, uploadActivityStats, uploadPercentage, formatUploadBytes, formatUploadDuration, formatUploadSpeed } = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))

test('large-file byte text changes even if coarse GB and integer percentage would not', () => {
  const bytes = 3.6 * 1024 ** 3
  assert.equal(formatUploadBytes(bytes), '3686.40 MB')
  assert.equal(formatUploadBytes(bytes + 1024 ** 2), '3687.40 MB')
  assert.equal(formatUploadSpeed(0), '0 B/s')
  assert.equal(formatUploadDuration(610), '10 分 10 秒')
})

test('rolling speed decays to zero without callbacks and resumes when new bytes arrive', () => {
  const state = createUploadActivity(0)
  recordUploadActivity(state, 1000, 10_000, 1000)
  recordUploadActivity(state, 2000, 10_000, 2000)
  assert.equal(uploadActivityStats(state, 2000).recentBps, 1000)
  assert.equal(uploadActivityStats(state, 12_000).recentBps, 0)
  assert.equal(uploadActivityStats(state, 32_000).stalled, true)
  recordUploadActivity(state, 2100, undefined, 33_000)
  assert.equal(state.total, 10_000)
  assert.equal(uploadActivityStats(state, 33_000).stalled, false)
})

test('duplicate and stale progress events do not hide a stall or move bytes backward', () => {
  const state = createUploadActivity(0)
  recordUploadActivity(state, 3100, 10_000, 1000)
  recordUploadActivity(state, 3100, undefined, 35_000)
  recordUploadActivity(state, 3000, 10_000, 36_000)
  assert.equal(state.loaded, 3100)
  assert.equal(state.lastProgressAt, 1000)
  assert.equal(uploadActivityStats(state, 36_000).stalled, true)
})

test('percentage reaches 100 only when transmission is actually complete', () => {
  const state = createUploadActivity(0)
  recordUploadActivity(state, 9999.9, 10_000, 1000)
  assert.equal(uploadPercentage(state), 99.9)
  recordUploadActivity(state, 10_000, 10_000, 2000)
  assert.equal(uploadPercentage(state), 100)
  assert.equal(uploadActivityStats(state, 600_000).stalled, false)
})
