export const viewTaskIsBusy = (status?: string) => ['ANALYZING', 'PRECHECKING', 'EXECUTING', 'RESTORING', 'VERIFYING'].includes(status || '')

type Step = { stepName: string; status: string; errorMessage?: string }
export function viewStageProgress(steps: Step[], names: string[]) {
  const matched = steps.filter(step => names.includes(step.stepName))
  const completed = matched.filter(step => step.status === 'SUCCESS').length
  const skipped = matched.filter(step => step.status === 'SKIPPED').length
  const failure = matched.find(step => ['FAILED', 'UNKNOWN', 'ROLLED_BACK'].includes(step.status))
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
