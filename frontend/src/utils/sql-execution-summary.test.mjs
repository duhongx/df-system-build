import assert from 'node:assert/strict'
import { describeSQLExecution } from './sql-execution-summary.ts'

const batch = { id: 1, totalFiles: 2, successFiles: 2, failedFiles: 0, skippedFiles: 0, executeStatus: 'SUCCESS', outcome: 'COMPLETED', executeMessage: '执行完成' }
const excluded = [
  { id: 3, fileName: 'failed.sql', code: 'NO_PENDING_SQL', reason: '没有未执行语句；失败 SQL 请单独重试' },
  { id: 4, fileName: 'manual.sql', code: 'MISSING_SCHEMA', reason: '无法识别执行账号/schema' },
]
const files = [{ id: 1, fileName: 'first.sql', executeStatus: 'SUCCESS' }, { id: 2, fileName: 'last.sql', executeStatus: 'SUCCESS' }]
let summary = describeSQLExecution(batch, files, excluded)
assert.match(summary.title, /已全部处理/)
assert.match(summary.description, /未入队 2 个/)
assert.equal(summary.type, 'warning')
assert.ok(!summary.title.includes('停止'), 'the screenshot situation is completion, not a new failure')

summary = describeSQLExecution({ ...batch, executeStatus: 'PARTIAL_FAILED', failedFiles: 1, successFiles: 1 }, files, excluded)
assert.match(summary.title, /已全部处理/, 'deferred failures do not mean the queue stopped')

summary = describeSQLExecution({ ...batch, totalFiles: 3, executeStatus: 'PARTIAL_FAILED', outcome: 'ERROR', stoppedFileId: 2, stoppedFileName: 'bad.sql', executeMessage: '第 12 行：permission denied', failedFiles: 1, successFiles: 1 }, [...files.slice(0, 1), { id: 2, executeStatus: 'FAILED' }, { id: 3, executeStatus: 'CANCELED' }], [])
assert.equal(summary.type, 'error')
assert.match(summary.title, /因错误停止/)
assert.match(summary.description, /bad.sql/)
assert.match(summary.description, /第 12 行/)
assert.match(summary.description, /后续 1 个入队文件未执行/)

summary = describeSQLExecution({ ...batch, executeStatus: 'CANCELED', outcome: 'CANCELED', stopUser: 'operator', executeMessage: '执行已停止' }, [{ executeStatus: 'CANCELED' }])
assert.match(summary.description, /operator/)
assert.match(summary.title, /已停止/)

summary = describeSQLExecution({ ...batch, executeStatus: 'FAILED', outcome: 'INTERRUPTED', executeMessage: '服务重启，执行结果待核实' }, files)
assert.equal(summary.type, 'error')
assert.match(summary.title, /需要核实结果/)

summary = describeSQLExecution({ ...batch, executeStatus: 'RUNNING', outcome: undefined }, [{ fileName: 'running.sql', executeStatus: 'RUNNING', executeMessage: '原文件第 163 行' }], excluded)
assert.match(summary.title, /running.sql/)
assert.match(summary.description, /第 163 行/)
assert.match(summary.description, /0\/2/)

summary = describeSQLExecution({ ...batch, executeStatus: 'RUNNING', stopRequested: true }, files)
assert.match(summary.title, /停止请求/)
summary = describeSQLExecution({ ...batch, executeStatus: 'PARTIAL_FAILED', outcome: undefined, executeMessage: '' }, files)
assert.match(summary.description, /旧批次未保存明确/)
console.log('8 SQL execution outcome scenarios passed')
