import type { SQLChangeStatement } from '../api/postgresql'

export const transactionOutcomeText = (outcome?: string) => ({
  PENDING_COMMIT: '等待事务提交', COMMITTED: '已提交', ROLLED_BACK: '已回滚',
  FAILED: '失败位置', NOT_EXECUTED: '未执行', UNKNOWN: '结果待核实',
  MANUAL_FAILED: '整组暂缓', SKIPPED: '整组不执行',
}[outcome || ''] || '—')

export const transactionScope = (row: SQLChangeStatement) => row.transactionStartLine
  ? `第 ${row.transactionStartLine}-${row.transactionEndLine} 行事务组` : ''

export function transactionStatementStatus(row: SQLChangeStatement): string | undefined {
  if (!row.transactionStartLine || row.executeStatus === 'UNKNOWN') return undefined
  if (row.manualFailure) return '已暂缓'
  if (row.executeStatus === 'SKIPPED' && row.skipKind === 'MANUAL') return '整组不执行'
  if (row.transactionOutcome === 'ROLLED_BACK') return '已回滚'
  if (row.executeStatus === 'FAILED' && row.transactionOutcome === 'NOT_EXECUTED') return '组内未执行'
  if (row.executeStatus === 'RUNNING') return row.transactionOutcome === 'PENDING_COMMIT' ? '待提交' : '事务执行中'
  return undefined
}
