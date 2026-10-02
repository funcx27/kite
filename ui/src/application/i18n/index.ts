// Registers the application module's translations (incl. the editors.* keys
// used by the shared env/image editors and common.diff.* used by the shared
// YAML diff viewer) into the shared 'translation' namespace. Imported eagerly
// through the module route definitions in ui/src/routes.tsx.
import i18n from '@/i18n'

import en from './en.json'
import zh from './zh.json'

for (const [lng, resources] of Object.entries({ en, zh })) {
  i18n.addResourceBundle(lng, 'translation', resources, true, true)
}
