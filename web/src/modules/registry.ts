import { accessModule } from './access/index.ts'
import { activityModule } from './activity/index.ts'
import { dnsModule } from './dns/index.ts'
import { sitesModule } from './sites/index.ts'
import type { PanelModule } from './types.ts'
import { usersModule } from './users/index.ts'

export const modules: PanelModule[] = [sitesModule, dnsModule, accessModule, usersModule, activityModule]
