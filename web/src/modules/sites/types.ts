export type Site = {
  name: string
  owner: string
  domains: string[]
  current_release: string
  releases: string[]
  /** Kept versions, newest first. Older panels don't send it. */
  versions?: Version[]
  /** Versions can be opened on an address of their own. */
  preview_addresses?: boolean
}

export type Version = {
  id: string
  live: boolean
  /** Published without being made live, and not live since. */
  pending: boolean
  /** Opens this version, live or not; empty when versions have no address. */
  preview_url: string
}

/** The answer to publishing: the site and the new version. */
export type Publication = Site & {
  published_release: string
  published_live: boolean
  preview_url: string
}
