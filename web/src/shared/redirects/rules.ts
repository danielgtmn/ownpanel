import type { Redirect, RedirectCode, RedirectInput } from '../api/index.ts'

export const MAX_PATH = 1024
export const MAX_TARGET = 2048

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/
const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/

export type RedirectKind = 'domain' | 'path'

export type RedirectDraft = {
  domain: string
  path: string
  target: string
  permanent: boolean
  keepMethod: boolean
  keepPath: boolean
  keepQuery: boolean
}

export type RedirectErrors = { domain?: string; path?: string; target?: string }

export function normalizeDomain(value: string): string {
  return value.trim().toLowerCase().replace(/\.$/, '')
}

const hasBadChar = (value: string) => /[\s\p{Cc}{}]/u.test(value)

export function validateSourceDomain(value: string): string | null {
  const raw = value.trim()
  if (!raw) return 'Enter the domain to redirect from.'
  if (/^[a-z]+:\/\//i.test(raw)) return `Enter just the domain, like www.example.com, without ${raw.split('//')[0]}//.`
  const bare = raw.replace(/^\[|\]$/g, '')
  if (IPV4.test(bare) || (bare.includes(':') && /^[0-9a-f:.]+$/i.test(bare))) {
    return 'A redirect needs a domain name like www.example.com, not an IP address.'
  }
  if (raw.includes('/')) return 'Enter just the domain, like www.example.com, without a path.'
  const domain = normalizeDomain(raw)
  const labels = domain.split('.')
  if (domain.length > 253 || labels.length < 2 || !labels.every((label) => LABEL.test(label))) {
    return `"${raw}" isn't a valid domain name. Enter one like www.example.com.`
  }
  if (/^\d+$/.test(labels[labels.length - 1])) {
    return 'A redirect needs a domain name like www.example.com, not an IP address.'
  }
  return null
}

export function isPrefixPath(path: string): boolean {
  return path.trim().endsWith('/*')
}

export function validateSourcePath(value: string): string | null {
  const path = value.trim()
  if (!path) return 'Enter the path to redirect, like /old or /blog/*.'
  if (!path.startsWith('/')) return `The path "${path}" must start with /.`
  if (path.length > MAX_PATH) return `The path can be at most ${MAX_PATH} characters long.`
  if (hasBadChar(path)) return `The path "${path}" must not contain spaces, line breaks or braces.`
  if (/[?#]/.test(path)) return `The path "${path}" must not contain ? or #. A redirect applies to every query.`
  if (path.includes('%')) return `Enter the path "${path}" without percent signs. Write special characters as they are.`
  const base = path.endsWith('/*') ? path.slice(0, -2) : path
  if (base.includes('*')) return `The path "${path}" can only end in /* (for everything below a path). * isn't allowed elsewhere.`
  if (base.split('/').slice(1).some((segment) => segment === '.' || segment === '..')) {
    return `The path "${path}" must not contain . or .. parts.`
  }
  if (base.includes('//')) return `The path "${path}" must not contain //.`
  return null
}

type ParsedTarget = { host: string; path: string }

function parseTarget(target: string, kind: RedirectKind): ParsedTarget | string {
  if (!target) return 'Enter where the redirect should lead.'
  if (target.length > MAX_TARGET) return `The target can be at most ${MAX_TARGET} characters long.`
  if (hasBadChar(target)) return 'The target must not contain spaces, line breaks or braces.'
  if (target.startsWith('/')) {
    if (kind === 'domain') return 'A whole domain must lead to a full address like https://example.com, not to a path.'
    if (target.startsWith('//')) return 'A target path must not start with //. Enter a full address like https://example.com instead.'
    return { host: '', path: target.split(/[?#]/)[0] }
  }
  const invalid =
    kind === 'path'
      ? `"${target}" isn't a valid target. Enter a full address starting with https:// or http://, or a path starting with /.`
      : `"${target}" isn't a valid target. Enter a full address starting with https:// or http://.`
  if (!/^https?:\/\/[^/]/i.test(target)) return invalid
  let url: URL
  try {
    url = new URL(target)
  } catch {
    return invalid
  }
  if (url.username || url.password) return 'The target must not contain a user name or password.'
  if (!url.hostname) return `"${target}" has no host name.`
  const host = url.port ? `${url.hostname.toLowerCase()}:${url.port}` : url.hostname.toLowerCase()
  return { host, path: url.pathname || '/' }
}

export function leadsBack(domain: string, path: string, parsed: ParsedTarget): boolean {
  if (parsed.host !== '' && parsed.host !== domain) return false
  if (path === '') return true
  if (path.endsWith('/*')) {
    const base = path.slice(0, -2)
    return base === '' || parsed.path === base || parsed.path.startsWith(base + '/')
  }
  return parsed.path === path
}

export function validateRedirect(draft: RedirectDraft, kind: RedirectKind): RedirectErrors {
  const errors: RedirectErrors = {}
  const domainError = validateSourceDomain(draft.domain)
  if (domainError) errors.domain = domainError
  const path = kind === 'path' ? draft.path.trim() : ''
  if (kind === 'path') {
    const pathError = validateSourcePath(draft.path)
    if (pathError) errors.path = pathError
  }
  const target = draft.target.trim()
  const parsed = parseTarget(target, kind)
  if (typeof parsed === 'string') {
    errors.target = parsed
    return errors
  }
  const keepsPath = draft.keepPath && (path === '' || path.endsWith('/*'))
  if (/[?#]/.test(target) && (keepsPath || draft.keepQuery)) {
    errors.target =
      'A target with ? or # can’t keep the path or the query. Remove them from the target or turn off keeping them.'
    return errors
  }
  if (!errors.domain && !errors.path) {
    const domain = normalizeDomain(draft.domain)
    if (leadsBack(domain, path, parsed)) {
      errors.target = `This leads back to ${domain}${path}, which would redirect again forever.`
    }
  }
  return errors
}

export function hasRedirectErrors(errors: RedirectErrors): boolean {
  return Boolean(errors.domain || errors.path || errors.target)
}

export function codeFor(permanent: boolean, keepMethod: boolean): RedirectCode {
  if (permanent) return keepMethod ? 308 : 301
  return keepMethod ? 307 : 302
}

export const isPermanent = (code: RedirectCode) => code === 301 || code === 308
export const keepsMethod = (code: RedirectCode) => code === 307 || code === 308

export function codeLabel(code: RedirectCode): string {
  switch (code) {
    case 301:
      return 'Permanent (301)'
    case 302:
      return 'Temporary (302)'
    case 307:
      return 'Temporary, keeps the method (307)'
    case 308:
      return 'Permanent, keeps the method (308)'
  }
}

export function routeRedirectError(message: string, kind: RedirectKind): RedirectErrors & { form?: string } {
  const text = message.toLowerCase()
  if (text.includes('target') || text.includes('leads back') || text.includes('full address')) return { target: message }
  if (kind === 'path' && text.includes('the path')) return { path: message }
  if (text.includes('domain')) return { domain: message }
  return { form: message }
}

export function draftFrom(redirect: Redirect | null, defaults: Partial<RedirectDraft> = {}): RedirectDraft {
  if (!redirect) {
    return {
      domain: '',
      path: '',
      target: '',
      permanent: true,
      keepMethod: false,
      keepPath: true,
      keepQuery: true,
      ...defaults,
    }
  }
  return {
    domain: redirect.source_domain,
    path: redirect.source_path,
    target: redirect.target,
    permanent: isPermanent(redirect.code),
    keepMethod: keepsMethod(redirect.code),
    keepPath: redirect.keep_path,
    keepQuery: redirect.keep_query,
  }
}

export function inputFrom(draft: RedirectDraft, kind: RedirectKind): RedirectInput {
  return {
    source_domain: normalizeDomain(draft.domain),
    source_path: kind === 'path' ? draft.path.trim() : '',
    target: draft.target.trim(),
    code: codeFor(draft.permanent, draft.keepMethod),
    keep_path: draft.keepPath,
    keep_query: draft.keepQuery,
  }
}

export function redirectFrom(redirect: Pick<Redirect, 'source_domain' | 'source_path'>): string {
  return redirect.source_domain + redirect.source_path
}

export function keepsSummary(redirect: Pick<Redirect, 'source_path' | 'keep_path' | 'keep_query'>): string[] {
  const out: string[] = []
  const pathMatters = redirect.source_path === '' || redirect.source_path.endsWith('/*')
  if (redirect.keep_path && pathMatters) out.push('Keeps the rest of the path')
  if (redirect.keep_query) out.push('Keeps the query')
  return out
}

export function sortRedirects<T extends Pick<Redirect, 'id' | 'source_domain' | 'source_path'>>(list: T[]): T[] {
  return [...list].sort(
    (a, b) =>
      a.source_domain.localeCompare(b.source_domain, 'en') ||
      a.source_path.localeCompare(b.source_path, 'en') ||
      a.id - b.id,
  )
}

export function domainRedirects<T extends Pick<Redirect, 'source_path'>>(list: T[]): T[] {
  return list.filter((redirect) => redirect.source_path === '')
}
