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
  '!@/components/account-settings-dialog',
  '!@/components/action-table',
  '!@/components/appearance-provider',
  '!@/components/chart',
  '!@/components/chart/cpu-usage-chart',
  '!@/components/chart/disk-io-usage-chart',
  '!@/components/chart/memory-usage-chart',
  '!@/components/chart/network-usage-chart',
  '!@/components/color-theme-provider',
  '!@/components/connection-indicator',
  '!@/components/delete-confirmation-dialog',
  '!@/components/error-message',
  '!@/components/language-toggle',
  '!@/components/mode-toggle',
  '!@/components/network-speed-indicator',
  '!@/components/selector',
  '!@/components/selector/configmap-selector',
  '!@/components/selector/container-selector',
  '!@/components/selector/pod-selector',
  '!@/components/selector/secret-selector',
  '!@/components/settings',
  '!@/components/settings/cluster-dialog',
  '!@/components/simple-yaml-editor',
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
]
