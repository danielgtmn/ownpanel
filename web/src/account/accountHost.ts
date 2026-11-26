// The panel marks its page with the mail server's name when it is opened
// under that name (see AccountHandler in internal/webui). Under it, mailbox
// users get their own account area instead of the panel.
export const ACCOUNT_META = 'ownpanel-mail-host'

const bare = (host: string) => host.trim().toLowerCase().replace(/\.$/, '')

// The mail server's name if the page carries it and was opened under it, else null.
export function matchAccountHost(meta: string | null | undefined, hostname: string): string | null {
  const host = bare(meta ?? '')
  return host && bare(hostname) === host ? host : null
}

export function accountHost(): string | null {
  const meta = document.querySelector(`meta[name="${ACCOUNT_META}"]`)?.getAttribute('content')
  return matchAccountHost(meta, window.location.hostname)
}
