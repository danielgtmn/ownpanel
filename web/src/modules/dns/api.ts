import { encodeSegment, request } from '../../shared/api/index.ts'
import { connectionPath } from '../../shared/dns/index.ts'
import type { RecordBody, RecordSet, RecordTemplate, TemplateResult } from './types.ts'

export {
  addDomain,
  adoptZone,
  createZone,
  deleteZone,
  listAvailableZones,
  listConnections,
  listZones,
  releaseZone,
} from '../../shared/dns/index.ts'

export const recordsPath = (connectionId: string, zone: string) =>
  `${connectionPath(connectionId)}/zones/${encodeSegment(zone)}/records`

export const recordPath = (connectionId: string, zone: string, name: string, type: string) =>
  `${recordsPath(connectionId, zone)}/${encodeSegment(name)}/${encodeSegment(type)}`

export async function listRecords(connectionId: string, zone: string): Promise<RecordSet[]> {
  return (await request<RecordSet[] | null>(recordsPath(connectionId, zone))) ?? []
}

export function saveRecord(
  connectionId: string,
  zone: string,
  name: string,
  type: string,
  body: RecordBody,
): Promise<RecordSet> {
  return request<RecordSet>(recordPath(connectionId, zone, name, type), { method: 'PUT', body })
}

export function deleteRecord(connectionId: string, zone: string, name: string, type: string): Promise<void> {
  return request<void>(recordPath(connectionId, zone, name, type), { method: 'DELETE' })
}

export function listTemplates(): Promise<RecordTemplate[]> {
  return request<RecordTemplate[]>('/dns/templates')
}

export const templatePath = (connectionId: number, zone: string, template: string) =>
  `${connectionPath(connectionId)}/zones/${encodeSegment(zone)}/templates/${encodeSegment(template)}`

// Without confirm nothing is written: the answer is the preview.
export function applyTemplate(
  connectionId: number,
  zone: string,
  template: string,
  params: Record<string, string>,
  confirm: boolean,
): Promise<TemplateResult> {
  return request<TemplateResult>(templatePath(connectionId, zone, template), { method: 'POST', body: { params, confirm } })
}
