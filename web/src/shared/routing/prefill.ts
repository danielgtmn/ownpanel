import { hrefWithQuery } from './routing.ts'

const HOST = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

// Opens the new-site dialog, with the domain filled in when given.
export function newSiteHref(domain?: string): string {
  return hrefWithQuery(['sites'], domain ? { new: '1', domain } : { new: '1' })
}

// Opens the new-redirect dialog, with the domain to redirect filled in when given.
export function newRedirectHref(domain?: string): string {
  return hrefWithQuery(['redirects'], domain ? { new: '1', domain } : { new: '1' })
}

// Opens the dialog that turns on e-mail, with the domain filled in when given.
export function newMailHref(domain?: string): string {
  return hrefWithQuery(['mail'], domain ? { new: '1', domain } : { new: '1' })
}

// The domain an address asks to fill in; anything that isn't a plain domain name is ignored.
export function prefilledDomain(query: URLSearchParams): string | undefined {
  const value = (query.get('domain') ?? '').trim().toLowerCase().replace(/\.$/, '')
  return HOST.test(value) ? value : undefined
}
