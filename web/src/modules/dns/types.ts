export type { Connection, NewConnection, Zone, ZoneList, ZoneProblem } from '../../shared/dns/index.ts'

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
