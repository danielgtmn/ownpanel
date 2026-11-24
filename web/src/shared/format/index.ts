const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const dateOnly = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' })

function parse(value: string): Date | null {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

export function formatDateTime(value: string): string {
  const date = parse(value)
  return date ? dateTime.format(date) : 'Unknown date'
}

export function formatDate(value: string): string {
  const date = parse(value)
  return date ? dateOnly.format(date) : 'Unknown date'
}

export function isoTimestamp(value: string): string | undefined {
  return parse(value)?.toISOString()
}

const VERSION_ID = /^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})(?:\.(\d{1,3}))?Z(?:-[0-9a-z]+)?$/i

export function parseReleaseDate(id: string): Date | null {
  const match = VERSION_ID.exec(id)
  if (!match) return null
  const [, year, month, day, hour, minute, second, millis] = match
  const date = new Date(
    Date.UTC(
      Number(year),
      Number(month) - 1,
      Number(day),
      Number(hour),
      Number(minute),
      Number(second),
      millis ? Number(millis.padEnd(3, '0')) : 0,
    ),
  )
  return Number.isNaN(date.getTime()) ? null : date
}

export function formatReleaseDate(id: string): string {
  const date = parseReleaseDate(id)
  return date ? dateTime.format(date) : 'Unknown date'
}
