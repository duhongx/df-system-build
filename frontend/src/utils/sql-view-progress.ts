export const viewTaskIsBusy = (status?: string) => ['ANALYZING', 'PRECHECKING', 'EXECUTING', 'RESTORING', 'VERIFYING'].includes(status || '')

type DiagnosticTask = { id: number; status: string; executeMessage?: string; executionMode?: string; outcomeUncertain?: boolean }
type DiagnosticStep = { id: number; stepOrder: number; stepName: string; status: string; sqlContent: string; errorMessage?: string }
type DiagnosticChange = { lineNumber?: number; executionStepId?: number; status?: string; schemaName: string; tableName: string; columnName: string; alterSql: string; errorMessage?: string }
export function viewTaskFailureDetails(task: DiagnosticTask | null, steps: DiagnosticStep[], changes: DiagnosticChange[], requestError = '', action = '') {
  const failed = steps.filter(step => ['FAILED', 'UNKNOWN'].includes(step.status)).sort((a, b) => a.stepOrder - b.stepOrder)
  const terminalFailure = Boolean(task && (['FAILED', 'PRECHECK_FAILED', 'RESTORE_FAILED', 'VERIFY_FAILED', 'UNKNOWN'].includes(task.status) || task.status === 'RESTORED' && failed.length > 0))
  if (!terminalFailure && !requestError && failed.length === 0) return null
  const labels: Record<string, string> = { ALTER_TABLE: '字段变更 SQL', DROP_DEPENDENCY: '删除依赖视图', RESTORE_OBJECT: '重建视图', RESTORE_COMMENT: '恢复注释', RESTORE_GRANT: '恢复权限', REFRESH_MATERIALIZED_VIEW: '刷新物化视图', VERIFY: '校验视图' }
  const phase = task?.status === 'PRECHECK_FAILED' ? '执行前预检'
    : task?.status === 'RESTORE_FAILED' ? '恢复视图'
    : task?.status === 'VERIFY_FAILED' ? '校验视图'
    : labels[failed[0]?.stepName || ''] || failed[0]?.stepName || action || (task?.executeMessage?.includes('分析') ? '依赖分析' : '任务处理')
  const message = terminalFailure && task?.executeMessage || requestError || failed[0]?.errorMessage || '任务未完成，服务端未返回具体错误，请刷新任务记录'
  const targets = changes.filter(change => failed.some(step => step.id === change.executionStepId) ||
    Boolean(change.errorMessage && (failed.length === 0 || ['FAILED', 'UNKNOWN'].includes(change.status || ''))) || message.includes(`${change.schemaName}.${change.tableName}.${change.columnName}`))
  const rollback = [...steps].reverse().find(step => step.stepName === 'ROLLBACK')
  const outcome = task?.outcomeUncertain || task?.status === 'UNKNOWN' ? '数据库结果待核实，禁止直接重复执行。'
    : requestError && !terminalFailure ? '请求返回异常，数据库执行结果以任务记录为准；请先刷新详情，避免重复提交。'
      : rollback?.status === 'SUCCESS' ? '已记录事务回滚。导致回滚的原始错误及 SQL 见下方详情。'
      : task?.status === 'RESTORED' ? '依赖视图已按备份恢复；字段变更的失败原因见下方详情。'
        : steps.length === 0 ? '尚未生成执行步骤；请按下方服务端记录确认失败原因。' : '请结合步骤状态确认已执行、回滚及恢复结果。'
  const report = [
    `视图依赖任务 #${task?.id || '-'} · ${task?.status || '请求失败'}`,
    `阶段：${phase}`, `执行方式：${task?.executionMode || '-'}`, `字段变更：${changes.length} 条`,
    outcome, message,
    ...failed.map(step => `步骤 ${step.stepOrder} · ${step.stepName} · ${step.status}\n${step.errorMessage || ''}\nSQL：\n${step.sqlContent}`),
    ...targets.map(change => `目标：${change.schemaName}.${change.tableName}.${change.columnName} · 源文件行号：${change.lineNumber || '-'}\n${change.errorMessage || ''}\n原始 SQL：\n${change.alterSql}`),
    requestError && requestError !== message ? `接口错误：${requestError}` : '',
  ].filter(Boolean).join('\n\n')
  return { phase, message, outcome, failed, targets, report }
}

type Step = { stepName: string; status: string; errorMessage?: string }
export function viewStageProgress(steps: Step[], names: string[]) {
  const matched = steps.filter(step => names.includes(step.stepName))
  const completed = matched.filter(step => step.status === 'SUCCESS').length
  const skipped = matched.filter(step => step.status === 'SKIPPED').length
  const failure = matched.find(step => ['FAILED', 'UNKNOWN'].includes(step.status)) || matched.find(step => step.status === 'ROLLED_BACK')
  let status: 'pending' | 'running' | 'success' | 'failed' | 'skipped' = 'pending'
  if (failure) status = 'failed'
  else if (matched.some(step => step.status === 'RUNNING') || (completed > 0 && completed + skipped < matched.length)) status = 'running'
  else if (completed > 0 && completed + skipped === matched.length) status = 'success'
  else if (matched.length > 0 && matched.every(step => step.status === 'SKIPPED')) status = 'skipped'
  return { status, count: matched.length, completed, skipped, error: failure?.errorMessage || '' }
}

// One request at a time; responses from an old task or an unmounted page are discarded.
export function createViewTaskPoller<T>(read: (id: number) => Promise<T>, apply: (value: T) => void, failed: (error: unknown) => void, delay = 2500) {
  let generation = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  const stop = () => { generation++; if (timer) clearTimeout(timer); timer = undefined }
  const start = (id: number) => {
    stop()
    const current = generation
    const tick = async () => {
      try { const value = await read(id); if (current === generation) apply(value) }
      catch (error) { if (current === generation) failed(error) }
      if (current === generation) timer = setTimeout(tick, delay)
    }
    return tick()
  }
  return { start, stop }
}
