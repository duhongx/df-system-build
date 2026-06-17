import fs from 'node:fs'
import assert from 'node:assert/strict'

const view = fs.readFileSync(new URL('../views/PostgreSQLSQLExecutionView.vue', import.meta.url), 'utf8')

assert.match(view, /prop="affectedRows"\s+label="影响行数"/, 'SQL detail table should display affected row count')
assert.match(view, /prop="estimatedRows"\s+label="预估行数"/, 'SQL detail table should display estimated affected row count')
assert.match(view, /forceBlockedSql/, 'SQL execution options should include forceBlockedSql')
assert.match(view, /强制执行阻断 SQL/, 'SQL execution view should expose a force blocked SQL switch')
assert.match(view, /accept="\.sql,\.zip"/, 'SQL upload should accept .sql and .zip files')
assert.match(view, /JSZip\.loadAsync/, 'SQL upload should extract zip files in the browser')
assert.match(view, /entry\.name\.toLowerCase\(\)\.endsWith\('\.sql'\)/, 'SQL upload should ignore non-SQL files inside zip files')
assert.match(view, /白名单配置/, 'SQL execution view should expose force whitelist configuration')
assert.match(view, /getSQLForceWhitelist/, 'SQL execution view should load configured force whitelist')
assert.match(view, /saveSQLForceWhitelist/, 'SQL execution view should save configured force whitelist')
assert.match(view, /const notExecutableCount = computed\(\(\) => statements\.value\.filter\(s => s\.executeStatus === 'NOT_EXECUTABLE'\)\.length\)/, 'not executable count should not double count successful forced BLOCKED risk statements')
assert.match(view, /forceableBlockedStatements/, 'force confirmation should distinguish whitelist-forceable statements')
assert.match(view, /hardBlockedStatements/, 'force confirmation should identify statements that remain blocked')
assert.match(view, /ZIP_MAX_SQL_FILES/, 'zip upload should enforce a SQL file count limit')
assert.match(view, /ZIP_MAX_TOTAL_SIZE/, 'zip upload should enforce a total SQL size limit')
assert.match(view, /try \{[\s\S]*JSZip\.loadAsync/, 'zip upload should report invalid zip errors explicitly')
assert.match(view, /duplicateNames/, 'zip upload should detect duplicate SQL basenames')
assert.match(view, /await handleOpen\(currentFile\.value\)/, 'skipping a statement should reload the current file status after backend recomputation')
