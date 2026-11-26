import { request } from '../../shared/api/index.ts'
import { normalizeDomain } from '../../shared/settings/index.ts'

export type HealthState = 'ok' | 'warning' | 'error' | 'unknown'

export type HealthCheck = {
  id: string
  state: HealthState | string
  found: string[]
  expected: string[]
  detail: string
  // Only for mail_host_spf: the panel can add the record through your DNS account.
  can_apply?: boolean
}

export type MailHealth = { mail_host: string; checks: HealthCheck[] }

export async function getMailHealth(): Promise<MailHealth> {
  const data = await request<MailHealth>('/mail/health')
  return {
    mail_host: data.mail_host ?? '',
    checks: (data.checks ?? []).map((check) => ({
      ...check,
      found: check.found ?? [],
      expected: check.expected ?? [],
      detail: check.detail ?? '',
      can_apply: check.can_apply ?? false,
    })),
  }
}

export const setUpMailHostSPF = () => request<unknown>('/mail/host-spf', { method: 'POST' })

// ownDetail: the hint already says what the server's detail says, in plainer words.
export type HealthLine = {
  tone: 'positive' | 'warning' | 'neutral'
  badge: string
  title: string
  hint?: string
  ownDetail?: boolean
}

const list = (values: string[]) => values.join(', ')

const BADGE: Record<string, { tone: HealthLine['tone']; badge: string }> = {
  ok: { tone: 'positive', badge: 'Fine' },
  warning: { tone: 'warning', badge: 'Check this' },
  error: { tone: 'warning', badge: 'Needs fixing' },
  unknown: { tone: 'neutral', badge: "Couldn't check" },
}

const TOPIC: Record<string, string> = {
  service: 'whether the mail service runs',
  mail_host_dns: 'where the mail server name points',
  mail_host_spf: 'whether other servers trust bounces and forwarded mail from here',
  ptr: "which name the server's addresses carry",
  outbound_port: 'whether this server can deliver mail to others',
  certificate: 'the certificate of the mail server',
  listeners: 'whether the mail server accepts connections',
  postmaster: 'where reports about mail problems go',
  blocklists: 'the lists of known spam senders',
  self_service: 'where mailbox users sign in',
}

function sentence(check: HealthCheck, host: string): { title: string; hint?: string; ownDetail?: boolean } {
  const ok = check.state === 'ok'
  const name = host || 'the mail server'
  switch (check.id) {
    case 'service':
      return ok
        ? { title: 'The mail service runs.' }
        : {
            title: "The mail service isn't running or can't be reached.",
            hint: 'No mail is received or sent until it runs again.',
          }
    case 'mail_host_dns':
      if (!host) return { title: 'The mail server has no name yet.', hint: 'Enter one above, like mail.example.com.' }
      return ok
        ? { title: `${host} points to this server.` }
        : {
            title: `${host} doesn't point to this server yet.`,
            hint: [
              check.expected.length > 0 ? `Create records for ${host} with ${list(check.expected)}.` : '',
              check.found.length > 0 ? `Now it points to ${list(check.found)}.` : '',
            ]
              .filter(Boolean)
              .join(' '),
          }
    case 'mail_host_spf':
      if (ok) return { title: `Other mail servers can tell that bounces and forwarded mail from ${name} are genuine.` }
      if (check.state === 'warning' && check.expected.length === 0) {
        return { title: `The record that says which servers may send for ${name} needs a look.` }
      }
      return {
        title: `Bounces and forwarded mail from ${name} may land in spam.`,
        hint: [
          check.expected.length > 0
            ? `Add a TXT record for ${name} with the value ${check.expected[0]}${check.found.length > 0 ? ' in place of the one there now' : ''}.`
            : '',
          check.found.length > 0 ? `Now: ${list(check.found)}.` : '',
        ]
          .filter(Boolean)
          .join(' '),
        ownDetail: check.expected.length > 0,
      }
    case 'ptr':
      return ok
        ? {
            title: `The server's addresses carry the name ${name}.`,
            hint: 'Other mail servers check this before they accept mail from here.',
          }
        : {
            title: `The server's addresses don't carry the name ${name} yet.`,
            hint: `Many mail servers turn mail away without it. Ask your hosting provider to set the reverse name (often called reverse DNS or PTR) of ${check.expected.length > 0 ? list(check.expected) : "this server's addresses"} to ${name}.${check.found.length > 0 ? ` Now: ${list(check.found)}.` : ''}`,
          }
    case 'outbound_port':
      return ok
        ? { title: 'This server can deliver mail to other mail servers.' }
        : {
            title: "This server can't reach other mail servers.",
            hint: 'Many hosting providers block outgoing mail (port 25) on new servers. Ask yours to unblock it.',
          }
    case 'certificate':
      return ok
        ? { title: `${name} has a trusted certificate.` }
        : {
            title: `${name} has no trusted certificate yet.`,
            hint: 'It is requested once the name points to this server. Until then, mail programs warn when they connect.',
          }
    case 'listeners':
      return ok
        ? { title: 'The mail server accepts connections.' }
        : { title: "Parts of the mail server don't accept connections yet." }
    case 'postmaster':
      return ok
        ? {
            title: check.found[0]
              ? `Reports about mail problems go to ${check.found[0]}.`
              : 'Reports about mail problems reach someone.',
          }
        : {
            title: 'No address receives reports about mail problems.',
            hint: 'Enter a postmaster address above.',
          }
    case 'blocklists': {
      if (ok) {
        return check.expected.length > 0
          ? { title: `Incoming mail is checked against ${list(check.expected)}.` }
          : { title: "Incoming mail isn't checked against lists of known spam senders." }
      }
      const one = check.found.length === 1
      return {
        title: `${list(check.found)} ${one ? "doesn't" : "don't"} answer this server, so mail isn't checked against ${one ? 'it' : 'them'}.`,
        hint: 'These lists turn away servers that ask them through big public lookup services. Let the server look up names itself, or get a free key for the Spamhaus Data Query Service and enter it as a list below.',
        ownDetail: true,
      }
    }
    case 'self_service':
      // The server's detail says what is missing (a name of its own, an update or the certificate).
      if (ok) return { title: `Mailbox users sign in at ${name}.` }
      return { title: "Mailbox users don't have their own sign-in page yet." }
    default:
      return { title: ok ? 'Fine.' : 'Something about the mail server needs a look.' }
  }
}

export function healthLine(check: HealthCheck, host: string): HealthLine {
  const badge = BADGE[check.state] ?? BADGE.unknown
  if (check.state === 'unknown') {
    return { ...badge, title: `Couldn't check ${TOPIC[check.id] ?? 'this'}.` }
  }
  return { ...badge, ...sentence(check, host) }
}

export function validateMailHost(input: string): string | null {
  const host = normalizeDomain(input)
  if (!host) return null
  if (/^[0-9.]+$/.test(host) || host.includes(':')) return 'Enter a name like mail.example.com, not an IP address.'
  if (!/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(host) || host.split('.').some((label) => label.startsWith('-') || label.endsWith('-'))) {
    return 'Enter a name like mail.example.com, without spaces or other characters.'
  }
  return null
}
