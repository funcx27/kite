// Registers the platform shell's own translations into the shared
// 'translation' namespace (deep-merged at import time, before first render —
// imported eagerly by src/portal/main.tsx). Keys live under
// module-owned prefixes so they never collide with upstream ones.
import i18n from '@/i18n'

import en from './en.json'
import zh from './zh.json'

for (const [lng, resources] of Object.entries({ en, zh })) {
  i18n.addResourceBundle(lng, 'translation', resources, true, true)
}
