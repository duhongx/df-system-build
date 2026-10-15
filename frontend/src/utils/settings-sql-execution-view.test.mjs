import fs from 'node:fs'
import assert from 'node:assert/strict'
const view = fs.readFileSync(new URL('../views/SettingsSQLExecutionView.vue', import.meta.url), 'utf8')
for (const key of ['sql_execute_skip_exists_column', 'sql_execute_skip_exists_table', 'sql_execute_skip_unique_constraint']) {
 assert.match(view, new RegExp(key + ': false'), `${key} should default to disabled`)
}
assert.match(view, /getSettings/)
assert.match(view, /updateSettings/)
assert.match(view, /60 秒/)
assert.doesNotMatch(view, /白名单|forceBlockedSql|sql_execute_lock_timeout|sql_execute_statement_timeout/)
