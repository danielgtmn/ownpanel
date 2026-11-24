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

export type RecordSet = {
  name: string
  type: string
  ttl: number | null
  values: string[]
  locked: boolean
}

export type RecordBody = {
  ttl: number | null
  values: string[]
}
