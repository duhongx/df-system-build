import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import { transform } from 'esbuild'
const { code } = await transform(await fs.readFile(new URL('./sql-view-progress.ts', import.meta.url), 'utf8'), { loader: 'ts', format: 'esm' })
const { viewTaskFailureDetails, viewStageProgress } = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))

test('rollback does not hide the original failed SQL and PostgreSQL error', () => {
  const steps = [
    { id: 1, stepOrder: 1, stepName: 'ALTER_TABLE', status: 'ROLLED_BACK', sqlContent: 'ALTER TABLE t ALTER COLUMN a TYPE bigint', errorMessage: '事务已回滚，本步骤未提交' },
    { id: 2, stepOrder: 2, stepName: 'ALTER_TABLE', status: 'FAILED', sqlContent: 'ALTER TABLE t ALTER COLUMN b TYPE integer', errorMessage: 'invalid input syntax for integer\nSQLSTATE: 22P02\nDETAIL: original detail' },
    { id: 3, stepOrder: 3, stepName: 'ROLLBACK', status: 'SUCCESS', sqlContent: 'ROLLBACK' },
  ]
  assert.match(viewStageProgress(steps, ['ALTER_TABLE']).error, /22P02/)
  const change = { executionStepId: 2, lineNumber: 879, schemaName: 's', tableName: 't', columnName: 'b', alterSql: steps[1].sqlContent }
  const details = viewTaskFailureDetails({ id: 9, status: 'FAILED', executionMode: 'TRANSACTION', executeMessage: '事务执行失败' }, steps, [change])
  assert.match(details.report, /#9/)
  assert.match(details.outcome, /已记录事务回滚/)
  for (const text of ['SQLSTATE: 22P02', 'DETAIL: original detail', 's.t.b', '879', steps[1].sqlContent]) assert.ok(details.report.includes(text), text)
  assert.equal(details.failed.length, 1)
})

test('precheck errors remain visible without any execution step', () => {
  const details = viewTaskFailureDetails({ id: 9, status: 'PRECHECK_FAILED', executeMessage: '执行前校验依赖视图备份失败：分析 s.t.c 依赖失败: context deadline exceeded' }, [], [])
  assert.equal(details.phase, '执行前预检')
  assert.match(details.report, /context deadline exceeded/)
  assert.match(details.outcome, /尚未生成执行步骤/)
})

test('automatic restoration and uncertain outcomes keep failure evidence visible', () => {
  const failed = [{ id: 1, stepOrder: 1, stepName: 'RESTORE_OBJECT', status: 'FAILED', sqlContent: 'CREATE VIEW broken AS SELECT 1', errorMessage: 'permission denied' }]
  const restored = viewTaskFailureDetails({ id: 9, status: 'RESTORED', executeMessage: '字段变更失败，已按备份恢复视图' }, failed, [])
  assert.match(restored.report, /permission denied/)
  assert.match(restored.outcome, /按备份恢复/)
  const unknown = viewTaskFailureDetails({ id: 9, status: 'UNKNOWN', outcomeUncertain: true }, [...failed, { id: 2, stepOrder: 2, stepName: 'ROLLBACK', status: 'SUCCESS', sqlContent: 'ROLLBACK' }], [])
  assert.match(unknown.outcome, /禁止直接重复执行/)
  assert.doesNotMatch(unknown.outcome, /已记录事务回滚/)
})

test('a failed request remains visible even if reloading task detail is unavailable', () => {
  const details = viewTaskFailureDetails({ id: 9, status: 'ANALYZED' }, [], [], 'connection reset by peer', '执行变更')
  assert.equal(details.message, 'connection reset by peer')
  assert.equal(viewTaskFailureDetails({ id: 9, status: 'SUCCESS' }, [], []), null)
})
