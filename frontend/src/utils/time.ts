/**
 * Format ISO time string to "YYYY-MM-DD HH:mm:ss"
 */
export function formatTime(time: string | null | undefined): string {
  if (!time) return '-'
  try {
    const d = new Date(time)
    if (isNaN(d.getTime())) return time
    const y = d.getFullYear()
    const m = String(d.getMonth() + 1).padStart(2, '0')
    const day = String(d.getDate()).padStart(2, '0')
    const h = String(d.getHours()).padStart(2, '0')
    const min = String(d.getMinutes()).padStart(2, '0')
    const s = String(d.getSeconds()).padStart(2, '0')
    return `${y}-${m}-${day} ${h}:${min}:${s}`
  } catch {
    return time || '-'
  }
}
