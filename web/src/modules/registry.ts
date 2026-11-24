import { accessModule } from './access/index.ts'
import { activityModule } from './activity/index.ts'
import { dnsModule } from './dns/index.ts'
import { redirectsModule } from './redirects/index.ts'
import { settingsModule } from './settings/index.ts'
import { sitesModule } from './sites/index.ts'
import type { PanelModule } from './types.ts'
import { usersModule } from './users/index.ts'

// The order is the order of rows in the sidebar and of the tiles in the rail's bottom group.
export const modules: PanelModule[] = [
  sitesModule,
  dnsModule,
  redirectsModule,
  settingsModule,
  usersModule,
  activityModule,
  accessModule,
]
