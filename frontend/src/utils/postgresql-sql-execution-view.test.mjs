import fs from 'node:fs'
import assert from 'node:assert/strict'
const view = fs.readFileSync(new URL('../views/PostgreSQLSQLExecutionView.vue', import.meta.url), 'utf8')
assert.match(view, /executeSelectedSQLFiles/, 'file execution remains available')
assert.match(view, /cancelSQLFile/, 'background execution can be stopped')
assert.match(view, /schedulePoll/, 'file results are refreshed after starting')
assert.match(view, /importServerSQL/, 'server SQL/ZIP import is available')
assert.match(view, /skipSQLFile/, 'operators can skip files')
assert.match(view, /editSQLStatement/, 'pending/failed SQL can be corrected')
assert.match(view, /originalStatement.value/, 'edits include original SQL for conflict detection')
assert.match(view, /skipSQLStatement/, 'operators can skip statements')
assert.match(view, /getSQLBatch/, 'selected execution sequence can be reopened')
assert.match(view, /listDoneSQLFiles/, 'execution history remains available')
assert.match(view, /selectedStatement.affectedRows/, 'actual affected rows remain visible')
assert.match(view, /selectedStatement.sqlState/, 'original database error code is visible')
assert.match(view, /selectedStatement.executeMessage/, 'full SQL execution error is visible')
assert.match(view, /accept="\.sql,\.zip"/, 'local SQL and ZIP import is available')
for (const removed of ['forceBlockedSql', 'autocompleteSQLStatementSchema', 'executeSQLStatementWithOptions', 'updateSQLStatementContent', 'executeSQLBatch', 'estimatedRows', 'riskLevel']) {
  assert.ok(!view.includes(removed), `ordinary file execution should not include ${removed}`)
}
