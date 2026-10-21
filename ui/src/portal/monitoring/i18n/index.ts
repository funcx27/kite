// Registers the monitoring module's translations into the shared
// 'translation' namespace; imported eagerly through the module route file.
import i18n from '@/i18n'

import en from './en.json'
import zh from './zh.json'

for (const [lng, resources] of Object.entries({ en, zh })) {
  i18n.addResourceBundle(lng, 'translation', resources, true, true)
}
