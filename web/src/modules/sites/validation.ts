export const MAX_NAME_LENGTH = 63

const NAME_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/
const HOSTNAME_PATTERN = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

export function validateSiteName(value: string): string | null {
  const name = value.trim()
  if (!name) return 'Enter a name for the site.'
  if (name.length > MAX_NAME_LENGTH) return `Use at most ${MAX_NAME_LENGTH} characters.`
  if (/[A-Z]/.test(name)) return 'Use lowercase letters only.'
  if (!/^[a-z0-9-]+$/.test(name)) return 'Use only lowercase letters, numbers and hyphens, without spaces.'
  if (!NAME_PATTERN.test(name)) return 'Start and end the name with a letter or number.'
  return null
}

export function parseDomainInput(value: string): string[] {
  const seen = new Set<string>()
  const domains: string[] = []
  for (const part of value.split(/[\n,;\s]+/)) {
    const domain = part.trim().toLowerCase()
    if (domain && !seen.has(domain)) {
      seen.add(domain)
      domains.push(domain)
    }
  }
  return domains
}

export function validateDomains(domains: string[]): string | null {
  for (const domain of domains) {
    if (/^[a-z]+:\/\//i.test(domain) || domain.includes('/')) {
      return `"${domain}" should be just the domain, like example.com, without https:// or a path.`
    }
    if (!HOSTNAME_PATTERN.test(domain)) {
      return `"${domain}" isn't a valid domain. Use something like example.com.`
    }
  }
  return null
}

export function domainsToText(domains: string[]): string {
  return domains.join('\n')
}
