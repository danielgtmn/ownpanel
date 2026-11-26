// A connected account at a DNS provider. It only grants access: none of its domains is managed in the panel until
// chosen, or until something (a site, a redirect, e-mail, the panel itself) uses it.
export type Connection = {
  id: number
  provider: 'hetzner'
  name: string
  owner: string
  created_at: string
  last_checked_at: string | null
  last_error: string
  can_write: boolean | null
  zone_count: number | null
}

export type NewConnection = {
  provider: 'hetzner'
  name: string
  token: string
}

export type Zone = {
  connection_id: number
  connection_name: string
  name: string
  read_only: boolean
  nameservers: string[]
  /** The person chose to manage the domain in the panel. */
  adopted: boolean
  /** What uses the domain; while anything does, it stays in the panel. */
  used_by: string[]
}

export type ZoneProblem = {
  connection_id: number
  connection_name: string
  error: string
}

export type ZoneList = {
  zones: Zone[]
  problems: ZoneProblem[]
}

// A domain the person added by hand: its DNS is managed elsewhere (another provider, the registrar).
export type AddedDomain = {
  name: string
  created_at: string
  /** What of the person uses a name in the domain; that keeps it in the panel even without the entry. */
  used_by: string[]
}

export type RemovedDomain = {
  name: string
  /** What of the person still uses a name in the domain, so it stays in the panel. */
  used_by: string[]
}
