import fs from 'node:fs'
import assert from 'node:assert/strict'

const router = fs.readFileSync(new URL('../router/index.ts', import.meta.url), 'utf8')
const layout = fs.readFileSync(new URL('../layouts/DefaultLayout.vue', import.meta.url), 'utf8')
const viewPath = new URL('../views/SettingsSQLExecutionView.vue', import.meta.url)
const view = fs.existsSync(viewPath) ? fs.readFileSync(viewPath, 'utf8') : ''

assert.match(router, /settings\/sql-execution/, 'router should expose SQL execution settings page')
assert.match(router, /SettingsSQLExecutionView/, 'router should lazy-load SQL execution settings view')
assert.match(layout, /\/settings\/sql-execution/, 'settings menu should link to SQL execution settings')

assert.match(view, /SQL执行配置/, 'settings page should be titled SQL execution configuration')
assert.match(view, /锁等待超时/, 'settings page should configure lock timeout')
assert.match(view, /语句执行超时/, 'settings page should configure statement timeout')
assert.match(view, /已存在字段跳过/, 'settings page should configure existing column skip')
assert.match(view, /对象已存在跳过/, 'settings page should configure existing table/object skip')
assert.match(view, /唯一冲突跳过/, 'settings page should configure unique conflict skip')
assert.match(view, /强制执行高风险 SQL/, 'settings page should configure high-risk force execution')
assert.match(view, /白名单/, 'settings page should configure force whitelist')
assert.match(view, /getSettings/, 'settings page should load settings')
assert.match(view, /updateSettings/, 'settings page should save settings')
assert.match(view, /getSQLForceWhitelist/, 'settings page should load force whitelist')
assert.match(view, /saveSQLForceWhitelist/, 'settings page should save force whitelist')
assert.match(view, /sql_execute_lock_timeout/, 'settings should persist lock timeout with a dedicated key')
assert.match(view, /sql_execute_statement_timeout/, 'settings should persist statement timeout with a dedicated key')
