// Registers the workflow module's translations into the shared 'translation'
// namespace (deep merge), following the application/kcross module pattern.
// Imported eagerly through the module route definitions and the credentials
// settings section.
import i18n from '@/i18n'

import en from './en.json'
import zh from './zh.json'

for (const [lng, resources] of Object.entries({ en, zh })) {
  i18n.addResourceBundle(lng, 'translation', resources, true, true)
}
