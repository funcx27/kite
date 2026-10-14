// Boundary guardrails (split plan P4): the five feature modules under
// src/{application,workflow,buildfleet,kcross,platform} must not reach into
// host pages / shared components / App, and the host side must not reach into
// the modules. Negated entries below (!…) are the shared surface verified to
// be used on both sides today; extend this list only as a deliberate decision.
//
// Kept in this side file (appended from eslint.config.js) so the main config
// stays byte-aligned with upstream kite.
const MODULE_FILES =
  'src/{application,workflow,buildfleet,kcross,platform}/**/*.{ts,tsx}'
const HOST_FILES = [
  'src/App.tsx',
  'src/{pages,components,contexts,lib}/**/*.{ts,tsx}',
]
// pairs: a nested allow needs its parent dir re-included first (gitignore
// semantics — a negation cannot re-include under an excluded directory)
const SHARED_COMPONENT_ALLOWS = [
  '!@/components/ui',
  '!@/components/ui/**',
  '!@/components/ai-chat',
  '!@/components/ai-chat/ai-chat-stream',
  '!@/components/appearance-provider',
  '!@/components/connection-indicator',
  '!@/components/delete-confirmation-dialog',
  '!@/components/editors',
  '!@/components/editors/image-editor',
  '!@/components/header-shell',
  '!@/components/language-toggle',
  '!@/components/log-viewer',
  '!@/components/mode-toggle',
  '!@/components/pod-file-browser',
  '!@/components/pod-monitoring',
  '!@/components/settings',
  '!@/components/settings/cluster-dialog',
  '!@/components/simple-yaml-editor',
  '!@/components/terminal-content',
  '!@/components/user-menu',
  '!@/components/yaml-diff-viewer',
]
const MODULE_IMPORT_PATTERNS = [
  '@/application',
  '@/application/**',
  '@/workflow',
  '@/workflow/**',
  '@/buildfleet',
  '@/buildfleet/**',
  '@/kcross',
  '@/kcross/**',
  '@/platform',
  '@/platform/**',
]

export const boundaryConfigs = [
  { ignores: ['src/.zcode'] },
  {
    files: [MODULE_FILES],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: [
                '@/pages',
                '@/pages/**',
                '@/components/**',
                '@/App',
                ...SHARED_COMPONENT_ALLOWS,
              ],
              message:
                'Module code must not import host pages, shared components, or App. Shared components need an explicit allowlist entry in eslint-boundaries.js; scripts/check-ui-boundaries.sh also resolves relative imports.',
            },
          ],
        },
      ],
    },
  },
  {
    files: HOST_FILES,
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: MODULE_IMPORT_PATTERNS,
              message:
                'Host code (App, pages, shared components, contexts, lib) must not import the feature modules (application/workflow/buildfleet/kcross/platform).',
            },
          ],
        },
      ],
    },
  },
  // The platform SPA entry composes shared infrastructure (login,
  // initialization, error boundaries, providers) and module routes, mirroring
  // what src/main.tsx does for the console; it is exempt from rule (a).
  {
    files: ['src/platform/main.tsx'],
    rules: { 'no-restricted-imports': 'off' },
  },
  // The console settings aggregator renders module settings panels; the kcross
  // panel was wired here deliberately by split plan P3.
  {
    files: ['src/components/settings/settings-sections.tsx'],
    rules: {
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: [
                ...MODULE_IMPORT_PATTERNS,
                '!@/kcross',
                '!@/kcross/kcross-management',
              ],
              message:
                'Host code must not import the feature modules; settings-sections may only reach the kcross settings panel.',
            },
          ],
        },
      ],
    },
  },
]
