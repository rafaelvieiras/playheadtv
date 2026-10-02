export function formatDuration(ms: number): string {
  const totalSeconds = Math.round(ms / 1000)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}min` : `${hours}h`
  if (minutes > 0) return seconds > 0 ? `${minutes}min ${seconds}s` : `${minutes}min`
  return `${seconds}s`
}
