import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { ClientSettings } from './api.ts'
import { CalendarsSection } from './CalendarsSection.tsx'
import { calendarRows, entriesText, importedText, importFileError } from './rules.ts'

const client: ClientSettings = {
  server: 'mail.example.com',
  imap_port: 993,
  smtp_port: 465,
  smtp_starttls_port: 587,
  user_name_is_address: true,
  dav_url: 'https://mail.example.com/dav/',
}

describe('calendars and contacts', () => {
  it('shows the settings for calendar and contacts programs only when the server offers them', () => {
    const rows = calendarRows(client, 'anna@example.com', false)
    expect(rows.map((r) => r.value)).toEqual([
      'https://mail.example.com/dav/',
      'anna@example.com',
      'Your mailbox password, or better an app password',
    ])
    expect(calendarRows({ ...client, dav_url: undefined }, 'anna@example.com', false)).toEqual([])
    expect(calendarRows(client, 'anna@example.com', true)[2].value).toBe('An app password')
    const html = renderToStaticMarkup(<CalendarsSection client={client} address="anna@example.com" appPasswordsOnly={false} />)
    expect(html).toContain('Calendars and contacts')
    expect(html).toContain('https://mail.example.com/dav/')
    expect(html).not.toMatch(/CalDAV|CardDAV|WebDAV|vCard|iCalendar/)
    expect(renderToStaticMarkup(<CalendarsSection client={{ ...client, dav_url: undefined }} address="a@b.c" appPasswordsOnly={false} />)).toBe('')
  })

  it('counts entries and describes an import', () => {
    expect(entriesText({ kind: 'calendar', objects: 1 })).toBe('1 entry')
    expect(entriesText({ kind: 'addressbook', objects: 1200 })).toBe('1,200 contacts')
    expect(importedText({ added: 2, replaced: 1, skipped: 0 }, 'calendar')).toBe('2 entries added, 1 entry updated.')
    expect(importedText({ added: 0, replaced: 0, skipped: 3 }, 'addressbook')).toBe("No contacts found in the file. 3 contacts couldn't be imported.")
  })

  it('checks the file before uploading', () => {
    expect(importFileError({ name: 'cal.ICS', size: 10 }, 'calendar', 1000)).toBeNull()
    expect(importFileError({ name: 'cal.vcf', size: 10 }, 'calendar', 1000)).toContain('.ics')
    expect(importFileError({ name: 'people.vcf', size: 2000 }, 'addressbook', 1000)).toContain('larger than')
    expect(importFileError({ name: 'people.vcf', size: 0 }, 'addressbook', 1000)).toBe('The file is empty.')
  })
})
