// Boundary guardrails (split plan P4): the portal SPA occupies a single
// directory, src/portal — the shell (entry, layout, providers) plus the four
// feature modules application/workflow/buildfleet/kcross. Portal code must
// not reach into host pages / shared components / App, and the host side
// must not reach into the portal. Negated entries below (!…) are the shared
// surface verified to be used on both sides today; extend this list only as
// a deliberate decision.
//
// Kept in this side file (appended from eslint.config.js) so the main config
// stays byte-aligned with upstream kite.
const MODULE_FILES = 'src/portal/**/*.{ts,tsx}'
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
  '!@/components/chart/chart-utils',
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
const MODULE_IMPORT_PATTERNS = ['@/portal', '@/portal/**']

// Secondary guardrail: the four portal modules must not import each other;
// cross-module sharing goes through @/portal/components/** and
// @/portal/lib/**. Allowlisted exceptions are pre-existing product seams
// (2026-10 portal move; moving them needs its own refactor unit):
//   - application ⇄ workflow data hooks: the deploy dialog lists workflow
//     credentials, the workflow editor / property drawer pick applications
//     and space quotas
//   - the application settings page composes the workflow build/credential
//     settings sections and the kcross management section
//   - the application detail page's related-workflows hub renders workflow
//     rows (WorkflowListItem) with the latest run's status chip and triggers
//     inline runs (RunParamsDialog)
//   - workflow settings reuse the application brand icons (DockerIcon,
//     GitIcon); a move into @/portal/components would remove this entry
const PORTAL_MODULES = ['application', 'workflow', 'buildfleet', 'kcross']
const CROSS_MODULE_WHITELIST = {
  application: [
    '@/portal/workflow/api',
    '@/portal/workflow/components/build-providers-management',
    '@/portal/workflow/components/build-runtime-settings',
    '@/portal/workflow/components/credentials-management',
    '@/portal/workflow/components/run-dialogs',
    '@/portal/workflow/components/space-build-settings',
    '@/portal/workflow/components/status-chip',
    '@/portal/workflow/types',
    '@/portal/kcross/kcross-management',
  ],
  workflow: [
    '@/portal/application/api',
    '@/portal/application/components/brand-icons',
  ],
  buildfleet: [],
  kcross: [],
}

// no-restricted-imports matches groups with gitignore semantics (the
// `ignore` package): a negation cannot re-include a path whose parent
// directory is excluded, so each allowlist entry expands to its parent
// chain inside the target module, outermost first. Bare module roots
// (e.g. !@/portal/workflow) have no index module and are inert.
const allowChain = (spec) => {
  const parts = spec.split('/')
  return parts
    .map((_, i) => parts.slice(0, i + 1).join('/'))
    .filter((prefix) => prefix.split('/').length >= 3)
    .map((prefix) => `!${prefix}`)
}

const crossModuleConfigs = PORTAL_MODULES.map((module) => ({
  files: [`src/portal/${module}/**/*.{ts,tsx}`],
  rules: {
    'no-restricted-imports': [
      'error',
      {
        patterns: [
          {
            group: [
              ...PORTAL_MODULES.filter((m) => m !== module).flatMap((m) => [
                `@/portal/${m}`,
                `@/portal/${m}/**`,
              ]),
              ...new Set(CROSS_MODULE_WHITELIST[module].flatMap(allowChain)),
            ],
            message: `Portal module "${module}" must not import the other portal modules; share via @/portal/components/** or @/portal/lib/**, or extend CROSS_MODULE_WHITELIST (mirrored in scripts/check-ui-boundaries.sh).`,
          },
        ],
      },
    ],
  },
}))

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
                'Portal code must not import host pages, shared components, or App. Shared components need an explicit allowlist entry in eslint-boundaries.js; scripts/check-ui-boundaries.sh also resolves relative imports.',
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
                'Host code (App, pages, shared components, contexts, lib) must not import the portal (src/portal/**).',
            },
          ],
        },
      ],
    },
  },
  // The portal SPA entry composes shared infrastructure (login,
  // initialization, error boundaries, providers) and module routes, mirroring
  // what src/main.tsx does for the console; it is exempt from rule (a).
  {
    files: ['src/portal/main.tsx'],
    rules: { 'no-restricted-imports': 'off' },
  },
  ...crossModuleConfigs,
]
