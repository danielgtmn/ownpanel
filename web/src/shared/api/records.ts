import { request } from './client.ts'
import { encodeSegment } from './path.ts'

export type RecordName = { name: string; type: string }

// The names and types of a domain's DNS records at a connected account, without their values.
export async function listRecordNames(connectionId: number, zone: string): Promise<RecordName[]> {
  const path = `/dns/connections/${encodeSegment(String(connectionId))}/zones/${encodeSegment(zone)}/records`
  const records = await request<RecordName[] | null>(path)
  return (records ?? []).map(({ name, type }) => ({ name, type }))
}
