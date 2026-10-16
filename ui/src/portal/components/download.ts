// Client-side text download: Blob → object URL → synthetic <a download> click
// → revoke (same pattern as the log viewer's downloadLogs).
export function downloadTextFile(
  filename: string,
  content: string,
  mime = 'text/plain'
) {
  const blob = new Blob([content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
