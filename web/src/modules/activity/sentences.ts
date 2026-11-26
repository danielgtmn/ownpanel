import { formatDateTime, formatReleaseDate, parseReleaseDate } from '../../shared/format/index.ts'
import type { Activity } from './types.ts'

export const LIMITS = [50, 100, 200] as const

export function nextLimit(limit: number): number | null {
  return LIMITS.find((candidate) => candidate > limit) ?? null
}

type Entry = Pick<Activity, 'actor' | 'action' | 'target'>

function about(target: string, withTarget: (target: string) => string, without: string): string {
  return target ? withTarget(target) : without
}

const VIA_TOKEN = / via token (.+)$/

export function describeActor(actor: string): string {
  if (!actor) return 'Someone'
  const match = VIA_TOKEN.exec(actor)
  if (!match) return actor
  return `${actor.slice(0, match.index)} (with the access token “${match[1]}”)`
}

const ROLES: Record<string, string> = { admin: 'Administrator', member: 'Member' }

function describeRole(role: string): string {
  return ROLES[role] ?? role
}

function formatMiB(mib: number): string {
  if (mib >= 1024) return `${(mib / 1024).toLocaleString('en', { maximumFractionDigits: 1 })} GB`
  return `${mib} MB`
}

// "quota 5120 MiB, 100 messages per hour, 500 per day" from the panel.
function describeMailLimits(detail: string): string {
  const match = /^(?:quota (\d+) MiB|no quota), (\d+) messages per hour, (\d+) per day$/.exec(detail)
  if (!match) return detail
  const storage = match[1] ? `Storage ${formatMiB(Number(match[1]))}` : 'No storage limit'
  return `${storage} · ${match[2]} messages an hour · ${match[3]} a day`
}

// "a message to a@x was not forwarded to b@y: <reason>" from the mail service.
function describeNotForwarded(detail: string): string {
  const match = /^a (message|delivery report) (?:to|for) \S+ was not (?:forwarded to (.+?)|passed on): (.+)$/.exec(detail)
  if (!match) return detail
  const reason = match[3]
  let why = reason.charAt(0).toUpperCase() + reason.slice(1)
  const limit = /^the domain (\S+) reached its limit of (\d+) messages per hour$/.exec(reason)
  if (limit) {
    why = `${limit[1]} reached its limit of ${limit[2]} messages an hour`
  } else if (/spam|junk|score|dmarc/i.test(reason)) {
    why = 'It looked like spam'
  } else if (reason === 'forwarding is not available') {
    why = "Forwarding isn't available on this server"
  }
  return `${match[2] ? `To ${match[2]}` : 'Delivery report'} · ${why}`
}

const ACCOUNT_CHANGES: Record<string, string> = {
  'password changed': 'New password',
  'with e-mail address': 'With e-mail address',
  'e-mail address changed': 'New e-mail address',
  'e-mail address removed': 'E-mail address removed',
}

function describeAccountChange(part: string): string {
  return ACCOUNT_CHANGES[part] ?? part.replace(/^role (\w+)$/, (_, role: string) => `Role: ${describeRole(role)}`)
}

// "import 3 from me@x.org at imap.x.org (continued)" from the panel, or
// "import 3 from old.mbox: 5 copied, 1 already there, 0 failed: reason"
// from the mail service.
function describeImport(detail: string): string {
  const match = /^import \d+ from (?:file (.+?)|(.+?)(?: at (\S+))?)( \(continued\))?(?:: (\d+) copied, (\d+) already there, (\d+) failed(?:: (.+))?)?$/.exec(
    detail,
  )
  if (!match) return detail
  const [, file, user, host, continued, copied, there, failed, reason] = match
  const parts = [file ? `From the file ${file}` : host ? `From ${user} on ${host}` : `From ${user}`]
  if (continued) parts.push('Continued')
  if (copied !== undefined) {
    parts.push(`${copied} copied`)
    if (there !== '0') parts.push(`${there} already there`)
    if (failed !== '0') parts.push(`${failed} not copied`)
  }
  if (reason) parts.push(reason.charAt(0).toUpperCase() + reason.slice(1))
  return parts.join(' · ')
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

// "stands alone" or "into Acme with a.org, b.org (domains of different owners)" from the panel.
function describeDomainOrganisation(detail: string): string {
  if (detail === 'stands alone') return 'Stands alone'
  const match = /^into (.+?)(?: with (.+?))?( \(domains of different owners\))?$/.exec(detail)
  if (!match) return capitalize(detail)
  const parts = [`Now part of ${match[1]}`]
  if (match[2]) parts.push(`Together with ${match[2]}`)
  if (match[3]) parts.push('Domains of different owners')
  return parts.join(' · ')
}

// A change the mailbox reported may end with "(not taken: <why>)": the
// panel changed the same thing at the same time and its change won.
function describeMailboxReport(detail: string, describe: (main: string) => string): string {
  const match = /^(.*?)\s*\(not taken: (.+)\)$/.exec(detail)
  const main = describe(match ? match[1] : detail)
  if (!match) return main
  const why = match[2] === 'changed in the panel at the same time' ? 'it was changed in the panel at the same time' : match[2]
  return main ? `${main} · Not taken: ${why}` : `Not taken: ${why}`
}

// "now Anna Muster" or "removed".
function describeDisplayName(main: string): string {
  if (main === 'removed') return 'Removed'
  return main.replace(/^now /, 'Now ')
}

function describeAppPasswordsOnly(main: string): string {
  if (main === 'on') return 'Only app passwords work in mail programs'
  if (main === 'off') return 'The mailbox password works in mail programs again'
  return main
}

export function describeDetail({ action, detail }: Pick<Activity, 'action' | 'detail'>): string {
  if (!detail) return ''
  switch (action) {
    case 'site.publish':
    case 'site.publish-not-live':
    case 'site.make-live':
      return parseReleaseDate(detail) ? `Version from ${formatReleaseDate(detail)}` : detail
    case 'site.create':
    case 'site.domains':
      return `Domains: ${detail}`
    case 'site.source': {
      const match = /^(.+), branch (\S+), publish on push (on|off)$/.exec(detail)
      if (!match) return detail
      return `${match[1]}, branch ${match[2]}, ${match[3] === 'on' ? 'publish on push' : 'no publishing on push'}`
    }
    case 'user.create':
    case 'user.update':
    case 'account.email':
      return detail.split(', ').map(describeAccountChange).join(' · ')
    case 'token.create': {
      const match = /^(read|write) access, (?:all sites|sites (.+))$/.exec(detail)
      if (!match) return detail
      const rights = match[1] === 'read' ? 'Read only' : 'Read and change'
      return `${rights} · ${match[2] ? `Only ${match[2]}` : 'All sites'}`
    }
    case 'setup':
    case 'account.password_reset_failed':
      return ''
    case 'update.request': {
      const match = /^(\S+) to (\S+) \((stable|nightly)\)(, an older version)?$/.exec(detail)
      if (!match) return detail
      const channel = match[3] === 'nightly' ? 'Nightly builds' : 'Stable releases'
      return `From ${match[1]} to ${match[2]} · ${channel}${match[4] ? ' · Older version' : ''}`
    }
    case 'update.auto':
      return detail === 'off' ? 'Off' : `On: ${detail}`
    case 'update.result':
    case 'setup.complete':
      return detail.charAt(0).toUpperCase() + detail.slice(1)
    case 'settings.update':
      return detail.charAt(0).toUpperCase() + detail.slice(1).replaceAll('; ', ' · ')
    case 'site.upload-link': {
      const match = /^valid until (\S+?)(, not made live)?$/.exec(detail)
      if (!match) return detail
      return `Valid until ${formatDateTime(match[1])}${match[2] ? ' · Publishes without making it live' : ''}`
    }
    case 'mail.domain.enable':
      return ''
    case 'mail.domain.activate': {
      const match = /^MX points to (\S+)$/.exec(detail)
      return match ? `Its mail records point to ${match[1]}` : detail
    }
    case 'mail.domain.delete':
      return detail.replace(/(\d+) aliases/, (_, count: string) =>
        count === '1' ? '1 forwarding address' : `${count} forwarding addresses`,
      )
    case 'mail.mailbox.create':
    case 'mail.mailbox.limits':
      return describeMailLimits(detail)
    case 'mail.blocklist.refused':
      return 'Incoming mail is not checked against it until it answers again. See how the mail server is doing in the settings.'
    case 'mail.delivery.not-forwarded':
      return describeNotForwarded(detail)
    case 'mail.mailbox.import-started':
    case 'mail.mailbox.import-cancelled':
    case 'mail.mailbox.import-finished':
    case 'mail.mailbox.import-failed':
    case 'mail.mailbox.import-paused':
    case 'mail.mailbox.import-interrupted':
      return describeImport(detail)
    case 'mail.alias.create':
    case 'mail.alias.update':
    case 'mail.alias.delete':
      return detail
        .replace(/^was (\S+); /, 'Was $1 · ')
        .replace(/(^|· )to /, (_, start: string) => `${start}To `)
        .replace(/\*@/g, 'all other addresses @')
    case 'mail.organisation.rename':
    case 'mail.organisation.delete':
      return capitalize(detail)
    case 'mail.domain.organisation':
      return describeDomainOrganisation(detail)
    case 'mail.mailbox.display-name':
    case 'mail.mailbox.display-name-changed':
      return describeMailboxReport(detail, describeDisplayName)
    case 'mail.mailbox.app-passwords-only':
      return describeMailboxReport(detail, describeAppPasswordsOnly)
    case 'mail.mailbox.password-changed':
    case 'mail.mailbox.app-password-created':
    case 'mail.mailbox.app-password-revoked':
      return describeMailboxReport(detail, (main) => main)
    default:
      return detail
  }
}

// A change the mailbox made itself has the mailbox's address as actor and target.
function bySelf(actor: string, target: string): boolean {
  return actor !== '' && actor === target
}

// "*@example.com" is the forwarding of every other address.
function mailAddress(target: string): string {
  return target.startsWith('*@') ? `all other addresses @${target.slice(2)}` : target
}

export function describeActivity({ actor, action, target }: Entry): string {
  const who = describeActor(actor)
  switch (action) {
    case 'site.create':
      return about(target, (t) => `${who} created the site ${t}`, `${who} created a site`)
    case 'site.domains':
      return about(target, (t) => `${who} changed the domains of ${t}`, `${who} changed the domains of a site`)
    case 'site.publish':
      return about(target, (t) => `${who} published new files for ${t}`, `${who} published new files for a site`)
    case 'site.publish-not-live':
      return about(
        target,
        (t) => `${who} published new files for ${t} without making them live`,
        `${who} published new files for a site without making them live`,
      )
    case 'site.make-live':
      return about(target, (t) => `${who} made another version of ${t} live`, `${who} made another version of a site live`)
    case 'site.delete':
      return about(target, (t) => `${who} removed the site ${t}`, `${who} removed a site`)
    case 'site.source':
      return about(target, (t) => `${who} linked ${t} to a repository`, `${who} linked a site to a repository`)
    case 'site.source-remove':
      return about(target, (t) => `${who} unlinked ${t} from its repository`, `${who} unlinked a site from its repository`)
    case 'user.create':
      return about(target, (t) => `${who} added ${t}`, `${who} added a person`)
    case 'user.update':
      return about(target, (t) => `${who} changed the account of ${t}`, `${who} changed a person's account`)
    case 'user.delete':
      return about(target, (t) => `${who} removed ${t}`, `${who} removed a person`)
    case 'token.create':
      return about(target, (t) => `${who} created the access token ${t}`, `${who} created an access token`)
    case 'token.delete':
      return about(target, (t) => `${who} deleted the access token ${t}`, `${who} deleted an access token`)
    case 'account.password':
      return `${who} changed their password`
    case 'account.login':
      return `${who} signed in`
    case 'account.email':
      return `${who} changed their e-mail address`
    case 'account.password_reset':
      // Entries from before the person was named have the actor "reset link".
      if (actor === 'reset link') return 'Someone chose a new password with a link sent by e-mail'
      return `${who} chose a new password with a link sent by e-mail`
    case 'account.password_reset_failed':
      return 'A link to choose a new password could not be sent'
    case 'setup':
      return `${who} set up the panel`
    case 'update.request':
      return `${who} started an update of the panel`
    case 'update.auto':
      return `${who} changed the automatic updates`
    case 'update.result':
      return 'An update of the panel finished'
    case 'setup.complete':
      return `${who} finished setting up the panel`
    case 'settings.update':
      return `${who} changed the settings`
    case 'site.upload-link':
      return about(target, (t) => `${who} created an upload link for ${t}`, `${who} created an upload link`)
    case 'redirect.create':
      return about(target, (t) => `${who} added a redirect from ${t}`, `${who} added a redirect`)
    case 'redirect.update':
      return about(target, (t) => `${who} changed the redirect from ${t}`, `${who} changed a redirect`)
    case 'redirect.delete':
      return about(target, (t) => `${who} removed the redirect from ${t}`, `${who} removed a redirect`)
    case 'dns.connect':
      return about(target, (t) => `${who} connected ${t}`, `${who} connected a domain provider`)
    case 'dns.disconnect':
      return about(target, (t) => `${who} disconnected ${t}`, `${who} disconnected a domain provider`)
    case 'dns.zone.create':
      return about(target, (t) => `${who} added the domain ${t}`, `${who} added a domain`)
    case 'dns.zone.delete':
      return about(target, (t) => `${who} removed the domain ${t}`, `${who} removed a domain`)
    case 'dns.record.set':
      return about(target, (t) => `${who} changed a DNS record of ${t}`, `${who} changed a DNS record`)
    case 'dns.record.delete':
      return about(target, (t) => `${who} deleted a DNS record of ${t}`, `${who} deleted a DNS record`)
    case 'dns.point':
      return about(target, (t) => `${who} pointed ${t} to this server`, `${who} pointed a domain to this server`)
    case 'mail.domain.enable':
      return about(target, (t) => `${who} turned on e-mail for ${t}`, `${who} turned on e-mail for a domain`)
    case 'mail.domain.activate':
      return about(target, (t) => `${t} started receiving mail here`, 'A domain started receiving mail here')
    case 'mail.domain.delete':
      return about(target, (t) => `${who} deleted the e-mail of ${t}`, `${who} deleted the e-mail of a domain`)
    case 'mail.mailbox.create':
      return about(target, (t) => `${who} created the mailbox ${t}`, `${who} created a mailbox`)
    case 'mail.mailbox.password':
      return about(target, (t) => `${who} changed the password of ${t}`, `${who} changed the password of a mailbox`)
    case 'mail.mailbox.limits':
      return about(target, (t) => `${who} changed the limits of ${t}`, `${who} changed the limits of a mailbox`)
    case 'mail.mailbox.delete':
      return about(target, (t) => `${who} deleted the mailbox ${t}`, `${who} deleted a mailbox`)
    case 'mail.mailbox.unblock':
      return about(target, (t) => `${who} let ${t} send again`, `${who} let a mailbox send again`)
    case 'mail.mailbox.import-started':
      return about(target, (t) => `${who} started importing mail into ${t}`, `${who} started importing mail`)
    case 'mail.mailbox.import-cancelled':
      return about(target, (t) => `${who} cancelled an import into ${t}`, `${who} cancelled an import`)
    case 'mail.mailbox.exported':
      return about(target, (t) => `${who} downloaded all mail of ${t}`, `${who} downloaded all mail of a mailbox`)
    case 'mail.mailbox.import-finished':
      return about(target, (t) => `Importing mail into ${t} finished`, 'Importing mail finished')
    case 'mail.mailbox.import-failed':
      return about(target, (t) => `Importing mail into ${t} failed`, 'Importing mail failed')
    case 'mail.mailbox.import-paused':
      return about(
        target,
        (t) => `Importing mail into ${t} paused until the other server allows more downloads`,
        'Importing mail paused until the other server allows more downloads',
      )
    case 'mail.mailbox.import-interrupted':
      return about(
        target,
        (t) => `Importing mail into ${t} was interrupted by a restart`,
        'Importing mail was interrupted by a restart',
      )
    case 'mail.alias.create':
      return about(target, (t) => `${who} added forwarding for ${mailAddress(t)}`, `${who} added forwarding`)
    case 'mail.alias.update':
      return about(target, (t) => `${who} changed the forwarding of ${mailAddress(t)}`, `${who} changed forwarding`)
    case 'mail.alias.delete':
      return about(target, (t) => `${who} deleted the forwarding of ${mailAddress(t)}`, `${who} deleted forwarding`)
    case 'mail.mailbox.blocked':
      return about(target, (t) => `The mail server stopped ${t} from sending`, 'The mail server stopped a mailbox from sending')
    case 'mail.mailbox.sign-in-failures':
      return about(target, (t) => `Many failed sign-ins to ${t}`, 'Many failed sign-ins to a mailbox')
    case 'mail.mailbox.send-limit':
      return about(target, (t) => `${t} reached its sending limit`, 'A mailbox reached its sending limit')
    case 'mail.delivery.bounced':
      return about(target, (t) => `Mail from ${t} could not be delivered`, 'Mail could not be delivered')
    case 'mail.listener.failed':
      return 'Part of the mail server stopped accepting connections'
    case 'mail.blocklist.refused':
      return about(
        target,
        (t) => `The list of spam senders ${t} stopped answering this server`,
        'A list of spam senders stopped answering this server',
      )
    case 'mail.delivery.not-forwarded':
      return about(target, (t) => `Mail to ${mailAddress(t)} was not forwarded`, 'Mail was not forwarded')
    case 'mail.organisation.create':
      return about(target, (t) => `${who} created the organisation ${t}`, `${who} created an organisation`)
    case 'mail.organisation.rename':
      return about(target, (t) => `${who} renamed an organisation to ${t}`, `${who} renamed an organisation`)
    case 'mail.organisation.delete':
      return about(target, (t) => `${who} deleted the organisation ${t}`, `${who} deleted an organisation`)
    case 'mail.domain.organisation':
      return about(target, (t) => `${who} changed the organisation of ${t}`, `${who} changed the organisation of a domain`)
    case 'mail.mailbox.display-name':
      return about(target, (t) => `${who} changed the display name of ${t}`, `${who} changed the display name of a mailbox`)
    case 'mail.mailbox.app-password-create':
      return about(target, (t) => `${who} created an app password for ${t}`, `${who} created an app password`)
    case 'mail.mailbox.app-password-revoke':
      return about(target, (t) => `${who} revoked an app password of ${t}`, `${who} revoked an app password`)
    case 'mail.mailbox.app-passwords-only':
      if (bySelf(actor, target)) return `${target} changed how mail programs sign in to it`
      return about(
        target,
        (t) => `${who} changed how mail programs sign in to ${t}`,
        `${who} changed how mail programs sign in to a mailbox`,
      )
    // Reported later by the mailbox itself; the actor is its address.
    case 'mail.mailbox.password-changed':
      if (bySelf(actor, target)) return `${target} changed its password`
      return about(target, (t) => `The password of ${t} was changed`, 'The password of a mailbox was changed')
    case 'mail.mailbox.display-name-changed':
      if (bySelf(actor, target)) return `${target} changed its display name`
      return about(target, (t) => `The display name of ${t} was changed`, 'The display name of a mailbox was changed')
    case 'mail.mailbox.app-password-created':
      if (bySelf(actor, target)) return `${target} created an app password`
      return about(target, (t) => `An app password was created for ${t}`, 'An app password was created')
    case 'mail.mailbox.app-password-revoked':
      if (bySelf(actor, target)) return `${target} revoked an app password`
      return about(target, (t) => `An app password of ${t} was revoked`, 'An app password was revoked')
    default:
      return about(target, (t) => `${who} made a change to ${t}`, `${who} made a change`)
  }
}
