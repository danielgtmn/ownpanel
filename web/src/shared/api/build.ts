// The panel names its web app in a header on every answer (the build changes with every update). A page that
// sees a different one than it started with is outdated, so it loads itself again before going on: otherwise
// people keep the old interface, or see a sign-in screen the new version has no reason to show.
export const BUILD_HEADER = 'X-Ownpanel-Build'

// Remembers the change of build the page last reloaded for ("old>new"), so a panel that keeps switching between
// two builds can't make it loop.
const RELOADED_KEY = 'ownpanel.reloadedFor'

type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem'>

export type BuildWatchEnv = {
  reload: () => void
  storage: () => Storage | null
}

const browser: BuildWatchEnv = {
  reload: () => window.location.reload(),
  storage: () => window.sessionStorage,
}

// Returns `notice`, which is told the build of every answer and returns true when the page is reloading.
export function createBuildWatch(env: BuildWatchEnv = browser): (build: string | null) => boolean {
  let loaded: string | null = null
  let reloading = false
  return (build) => {
    if (reloading) return true
    if (!build) return false
    if (loaded === null) loaded = build
    if (build === loaded) return false
    const change = `${loaded}>${build}`
    try {
      const storage = env.storage()
      // Without storage a reload couldn't be told from a loop; keep the page as it is.
      if (!storage || storage.getItem(RELOADED_KEY) === change) return false
      storage.setItem(RELOADED_KEY, change)
    } catch {
      return false
    }
    reloading = true
    env.reload()
    return true
  }
}
