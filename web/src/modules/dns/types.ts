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

export type TemplateParam = {
  id: string
  label: string
  description: string
  placeholder: string
  optional: boolean
}

export type RecordTemplate = {
  id: string
  title: string
  description: string
  params: TemplateParam[]
  own_mail: boolean
}

export type TemplateAction = 'add' | 'change' | 'ok' | 'conflict'

export type TemplateChange = {
  name: string
  type: string
  action: TemplateAction
  current: string[]
  new: string[]
  note: string
}

export type TemplateResult = {
  template: string
  zone: string
  changes: TemplateChange[]
  applied: boolean
}
