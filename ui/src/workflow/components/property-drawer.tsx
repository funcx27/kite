// Editor property drawer: per node type, one form following spec.go's props
// structs (BuildProps / DeployProps / ApprovalProps / NotifyProps) plus the
// shared node fields (id / name / onFailure / timeout). Lists that exist on
// the host (clusters, applications, credentials) render as selects and fall
// back to free text when the list cannot serve a pick.
import { useMemo, useRef, useState } from 'react'
import { useApplications } from '@/application/api'
import { useSpaces } from '@/platform/space-context'
import {
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconTrash,
  IconX,
} from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'
import { useCluster } from '@/hooks/use-cluster'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'

import { useCredentials, useUserDirectory } from '../api'
import type { UserDirectoryEntry } from '../api'
import { buildTemplateRefs, isValidNodeNodeId, nodeTypeAccent } from '../lib'
import type { ParamSpec, WorkflowNode } from '../types'
import { TemplateInput } from './template-input'

export interface PropertyDrawerProps {
  node: WorkflowNode | null
  params: ParamSpec[]
  // All current editor nodes: feeds steps.<id>.outputs.<key> template chips.
  nodes: WorkflowNode[]
  onChange: (node: WorkflowNode) => void
  onDelete: () => void
}

// Select sentinel for "no credential": Radix forbids empty item values, and
// credential names are DNS-1123 labels (no underscores) so this cannot
// collide with a real name.
const credentialNoneValue = '__none__'

// CredentialRefField picks one credential ref prop (git fetch or registry
// push). Degrades to a plain input when the kind's credential list cannot
// serve a pick; a saved ref missing from the list still echoes so the saved
// spec never silently loses its reference.
function CredentialRefField({
  propKey,
  saved,
  names,
  label,
  hint,
  noneLabel,
  currentLabel,
  patchProps,
  onFocusField,
}: {
  propKey: string
  saved: string
  names: string[]
  label: string
  hint: string
  noneLabel: string
  currentLabel: string
  patchProps: (key: string, value: unknown) => void
  onFocusField: (field: string) => void
}) {
  const credentialEcho = saved && !names.includes(saved) ? saved : undefined
  return (
    <Field label={label} hint={hint}>
      {names.length > 0 ? (
        <Select
          value={saved || credentialNoneValue}
          onValueChange={(v) =>
            patchProps(propKey, v === credentialNoneValue ? undefined : v)
          }
        >
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={credentialNoneValue}>{noneLabel}</SelectItem>
            {names.map((name) => (
              <SelectItem key={name} value={name}>
                {name}
              </SelectItem>
            ))}
            {credentialEcho && (
              <SelectItem value={credentialEcho}>
                {credentialEcho}{' '}
                <span className="text-muted-foreground">{currentLabel}</span>
              </SelectItem>
            )}
          </SelectContent>
        </Select>
      ) : (
        <Input
          value={saved}
          onChange={(e) => patchProps(propKey, e.target.value || undefined)}
          onFocus={() => onFocusField(propKey)}
        />
      )}
    </Field>
  )
}

export function PropertyDrawer({
  node,
  params,
  nodes,
  onChange,
  onDelete,
}: PropertyDrawerProps) {
  const { t } = useTranslation()
  // The template-var chips insert into the field the user focused last, at
  // that input's current caret position.
  const focusedField = useRef<string | null>(null)
  const focusedInput = useRef<HTMLInputElement | HTMLTextAreaElement | null>(
    null
  )

  // Focus events bubble, so one capture handler on the form container keeps
  // the focused DOM element; its selectionStart/End stay readable at chip
  // click time even though the input has lost focus.
  const recordFocusedInput = (e: React.FocusEvent) => {
    const target = e.target
    if (
      target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement
    ) {
      focusedInput.current = target
    }
  }

  const templateVars = useMemo(
    () => buildTemplateRefs(params, nodes, node?.id),
    [params, nodes, node?.id]
  )

  // Splice token at the focused input's caret; append when the selection is
  // unavailable (e.g. inputs whose selectionStart reports null).
  const spliceAtCursor = (current: string, token: string): string => {
    const el = focusedInput.current
    const start = el?.selectionStart
    const end = el?.selectionEnd
    if (start === null || start === undefined || end === null) {
      return current + token
    }
    return current.slice(0, start) + token + current.slice(end)
  }

  const insertVar = (token: string) => {
    if (!node || !focusedField.current) return
    const field = focusedField.current
    if (field === '__name__') {
      onChange({ ...node, name: spliceAtCursor(node.name, token) })
      return
    }
    if (field.includes('.')) {
      const [group, key] = field.split('.')
      const nested = {
        ...((node.props?.[group] as Record<string, unknown>) ?? {}),
      }
      nested[key] = spliceAtCursor(String(nested[key] ?? ''), token)
      onChange({ ...node, props: { ...(node.props ?? {}), [group]: nested } })
      return
    }
    setStringProp(
      node,
      field,
      spliceAtCursor(readStringProp(node, field), token),
      onChange
    )
  }

  if (!node) {
    return (
      <div className="flex h-full flex-col">
        <DrawerHeader
          title={t('workflow.editor.selectNode')}
          sub={t('workflow.editor.selectNodeHint')}
        />
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground/60">
          {t('workflow.editor.noNodeSelected')}
        </div>
      </div>
    )
  }

  const patchProps = (key: string, value: unknown) =>
    onChange({ ...node, props: { ...(node.props ?? {}), [key]: value } })

  // Selecting a deploy target writes the space+app pair in one change: two
  // separate patchProps calls would race on the same stale node snapshot and
  // drop the first field.
  const patchAppTarget = (space: string, app: string) =>
    onChange({
      ...node,
      props: { ...(node.props ?? {}), space, app },
    })

  const templateInputVars = { params, nodes, currentNodeId: node.id }

  return (
    <div className="flex h-full flex-col">
      <DrawerHeader
        title={t('workflow.editor.selectNode')}
        sub={t(`workflow.nodeTypes.${node.type}`)}
        accent={nodeTypeAccent[node.type]}
      />
      <div
        className="flex-1 space-y-4 overflow-y-auto px-4 py-3"
        onFocusCapture={recordFocusedInput}
      >
        <Field label={t('workflow.editor.nodeId')}>
          <Input
            value={node.id}
            onChange={(e) => onChange({ ...node, id: e.target.value })}
            onFocus={() => (focusedField.current = '__id__')}
            className={
              isValidNodeNodeId(node.id) || node.id === ''
                ? ''
                : 'border-destructive'
            }
          />
          {focusedField.current === '__id__' &&
            !isValidNodeNodeId(node.id) &&
            node.id !== '' && (
              <p className="text-xs text-destructive">[A-Za-z0-9_]</p>
            )}
        </Field>
        <Field label={t('workflow.editor.nodeName')}>
          <Input
            value={node.name}
            onChange={(e) => onChange({ ...node, name: e.target.value })}
            onFocus={() => (focusedField.current = '__name__')}
          />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('workflow.editor.onFailure')}>
            <Select
              value={node.onFailure ?? 'stop'}
              onValueChange={(v) =>
                onChange({
                  ...node,
                  onFailure: v === 'stop' ? undefined : (v as 'continue'),
                })
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="stop">
                  {t('workflow.editor.onFailureStop')}
                </SelectItem>
                <SelectItem value="continue">
                  {t('workflow.editor.onFailureContinue')}
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('workflow.editor.timeout')}
            hint={t('workflow.editor.timeoutHint')}
          >
            <Input
              value={node.timeout ?? ''}
              onChange={(e) =>
                onChange({ ...node, timeout: e.target.value || undefined })
              }
              placeholder="24h"
            />
          </Field>
        </div>

        <Separator />

        {node.type === 'build-image' && (
          <BuildPropsForm
            key={node.id}
            node={node}
            patchProps={patchProps}
            onFocusField={(f) => (focusedField.current = f)}
            templateVars={templateInputVars}
          />
        )}
        {node.type === 'deploy-app' && (
          <DeployPropsForm
            key={node.id}
            node={node}
            patchProps={patchProps}
            onSelectApp={patchAppTarget}
            onFocusField={(f) => (focusedField.current = f)}
            templateVars={templateInputVars}
          />
        )}
        {node.type === 'approval' && (
          <ApprovalPropsForm
            node={node}
            patchProps={patchProps}
            onFocusField={(f) => (focusedField.current = f)}
            templateVars={templateInputVars}
          />
        )}
        {node.type === 'notify' && (
          <NotifyPropsForm
            node={node}
            patchProps={patchProps}
            onFocusField={(f) => (focusedField.current = f)}
            templateVars={templateInputVars}
          />
        )}
      </div>
      <div className="border-t px-4 py-2.5">
        <div className="mb-1.5 text-[11px] font-medium text-muted-foreground">
          {t('workflow.editor.templateVars')}
        </div>
        <div className="flex max-h-24 flex-wrap gap-1.5 overflow-y-auto">
          {templateVars.map((value) => (
            <button
              key={value}
              type="button"
              onClick={() => insertVar(`{{${value}}}`)}
              className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              {value}
            </button>
          ))}
        </div>
      </div>
      <div className="border-t px-4 py-2.5">
        <Button
          variant="destructive"
          size="sm"
          className="w-full"
          onClick={onDelete}
        >
          <IconTrash className="mr-1.5 h-4 w-4" />
          {t('workflow.editor.deleteNode')}
        </Button>
      </div>
    </div>
  )
}

function DrawerHeader({
  title,
  sub,
  accent,
}: {
  title: string
  sub: string
  accent?: string
}) {
  return (
    <div className="border-b px-4 py-3">
      <div className="flex items-center gap-2">
        {accent && <span className={`h-2.5 w-2.5 rounded-full ${accent}`} />}
        <div className="text-sm font-semibold">{title}</div>
      </div>
      <div className="mt-0.5 text-xs text-muted-foreground">{sub}</div>
    </div>
  )
}

function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs">{label}</Label>
      {children}
      {hint && (
        <p className="text-[11px] leading-4 text-muted-foreground">{hint}</p>
      )}
    </div>
  )
}

interface PropsFormShared {
  node: WorkflowNode
  patchProps: (key: string, value: unknown) => void
  onFocusField: (field: string) => void
  // Autocomplete inputs for TemplateInput fields: params, sibling nodes and
  // the owning node id (its own outputs stay out of the suggestions).
  templateVars: {
    params: ParamSpec[]
    nodes: WorkflowNode[]
    currentNodeId: string
  }
}

function readStringProp(node: WorkflowNode, key: string): string {
  const value = node.props?.[key]
  return typeof value === 'string' ? value : ''
}

function setStringProp(
  node: WorkflowNode,
  key: string,
  value: string,
  onChange: (n: WorkflowNode) => void
) {
  onChange({ ...node, props: { ...(node.props ?? {}), [key]: value } })
}

// AdvancedCollapsible folds a props form's low-frequency fields. The owning
// forms are keyed by node id, so each selection re-derives the initial open
// state from that node's stored values.
function AdvancedCollapsible({
  open,
  onOpenChange,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  children: React.ReactNode
}) {
  const { t } = useTranslation()
  return (
    <Collapsible open={open} onOpenChange={onOpenChange}>
      <CollapsibleTrigger className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent/50">
        <IconChevronRight
          className={cn(
            'h-4 w-4 shrink-0 text-muted-foreground transition-transform',
            open && 'rotate-90'
          )}
        />
        {t('workflow.props.advancedSection')}
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-4 pt-3">
        {children}
      </CollapsibleContent>
    </Collapsible>
  )
}

function BuildPropsForm({
  node,
  patchProps,
  onFocusField,
  templateVars,
}: PropsFormShared) {
  const { t } = useTranslation()
  const gitUrl = readNestedString(node, 'git', 'url')
  const sourceKind = gitUrl
    ? 'git'
    : readStringProp(node, 'httpUrl')
      ? 'http'
      : 'git'
  const args = readArgs(node)
  // The credential refs are optional: each Select degrades to the plain
  // input when its kind's credential list failed to load or is empty (query
  // still pending counts as empty, so the field is always usable).
  const { data: credentials, isSuccess: credentialsLoaded } = useCredentials()
  const credentialList = credentialsLoaded ? (credentials ?? []) : []
  const gitCredentialNames = credentialList
    .filter((c) => c.hasGit)
    .map((c) => c.name)
  const registryCredentialNames = credentialList
    .filter((c) => c.registryHosts.length > 0)
    .map((c) => c.name)
  const savedGitCredential = readStringProp(node, 'gitCredentialRef')
  const savedRegistryCredential = readStringProp(node, 'registryCredentialRef')
  // The advanced section starts collapsed unless one of its fields already
  // carries a value, so configured options never hide behind the fold.
  const [advancedOpen, setAdvancedOpen] = useState(
    readNestedString(node, 'git', 'subdir') !== '' ||
      readStringProp(node, 'httpUrl') !== '' ||
      readStringProp(node, 'dockerfileContent') !== '' ||
      args !== '' ||
      ((node.props?.platforms as string[] | undefined)?.length ?? 0) > 0
  )

  const setGit = (key: string, value: string) => {
    const git = { ...((node.props?.git as Record<string, unknown>) ?? {}) }
    if (value === '') delete git[key]
    else git[key] = value
    patchProps('git', Object.keys(git).length > 0 ? git : undefined)
  }

  return (
    <div className="space-y-4">
      <CredentialRefField
        propKey="gitCredentialRef"
        saved={savedGitCredential}
        names={gitCredentialNames}
        label={t('workflow.props.gitCredentialRef')}
        hint={t('workflow.props.gitCredentialRefHint')}
        noneLabel={t('workflow.props.credentialRefNone')}
        currentLabel={t('workflow.props.currentValue')}
        patchProps={patchProps}
        onFocusField={onFocusField}
      />
      <CredentialRefField
        propKey="registryCredentialRef"
        saved={savedRegistryCredential}
        names={registryCredentialNames}
        label={t('workflow.props.registryCredentialRef')}
        hint={t('workflow.props.registryCredentialRefHint')}
        noneLabel={t('workflow.props.credentialRefNone')}
        currentLabel={t('workflow.props.currentValue')}
        patchProps={patchProps}
        onFocusField={onFocusField}
      />
      <Field label={t('workflow.props.gitUrl')}>
        <TemplateInput
          {...templateVars}
          value={gitUrl}
          onValueChange={(v) => setGit('url', v)}
          onFocus={() => onFocusField('git.url')}
          placeholder="https://github.com/org/repo.git"
        />
      </Field>
      {sourceKind === 'git' && (
        <Field label={t('workflow.props.gitRef')}>
          <TemplateInput
            {...templateVars}
            value={readNestedString(node, 'git', 'ref')}
            onValueChange={(v) => setGit('ref', v)}
            onFocus={() => onFocusField('git.ref')}
            placeholder="main"
          />
        </Field>
      )}
      <Field label={t('workflow.props.dockerfile')}>
        <TemplateInput
          {...templateVars}
          value={readStringProp(node, 'dockerfile')}
          onValueChange={(v) => patchProps('dockerfile', v || undefined)}
          onFocus={() => onFocusField('dockerfile')}
          placeholder="Dockerfile"
        />
      </Field>
      <Field
        label={t('workflow.props.outputRef')}
        hint={t('workflow.props.outputRefHint')}
      >
        <TemplateInput
          {...templateVars}
          value={readStringProp(node, 'outputRef')}
          onValueChange={(v) => patchProps('outputRef', v || undefined)}
          onFocus={() => onFocusField('outputRef')}
        />
      </Field>
      <AdvancedCollapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        {sourceKind === 'git' && (
          <Field label={t('workflow.props.gitSubdir')}>
            <TemplateInput
              {...templateVars}
              value={readNestedString(node, 'git', 'subdir')}
              onValueChange={(v) => setGit('subdir', v)}
              onFocus={() => onFocusField('git.subdir')}
            />
          </Field>
        )}
        {!gitUrl && (
          <Field label={t('workflow.props.httpUrl')}>
            <TemplateInput
              {...templateVars}
              value={readStringProp(node, 'httpUrl')}
              onValueChange={(v) => patchProps('httpUrl', v || undefined)}
              onFocus={() => onFocusField('httpUrl')}
            />
          </Field>
        )}
        <Field label={t('workflow.props.dockerfileContent')}>
          <TemplateInput
            {...templateVars}
            multiline
            rows={4}
            className="font-mono text-xs"
            value={readStringProp(node, 'dockerfileContent')}
            onValueChange={(v) =>
              patchProps('dockerfileContent', v || undefined)
            }
            onFocus={() => onFocusField('dockerfileContent')}
          />
        </Field>
        <Field
          label={t('workflow.props.args')}
          hint={t('workflow.props.argsHint')}
        >
          <TemplateInput
            {...templateVars}
            multiline
            rows={3}
            className="font-mono text-xs"
            value={args}
            onValueChange={(v) => patchProps('args', parseArgs(v))}
            onFocus={() => onFocusField('args')}
          />
        </Field>
        <Field
          label={t('workflow.props.platforms')}
          hint={t('workflow.props.platformsHint')}
        >
          <Input
            value={
              (node.props?.platforms as string[] | undefined)?.join(',') ?? ''
            }
            onChange={(e) => patchProps('platforms', splitList(e.target.value))}
            onFocus={() => onFocusField('platforms')}
          />
        </Field>
      </AdvancedCollapsible>
    </div>
  )
}

// Deploy targets address an application by its (space, name) identity. The
// space is fixed to the workflow's own space (the editor locks the workflow
// to one space) — only the app is picked, from that space's app list.
interface AppOption {
  value: string
  label: string
}

function DeployPropsForm({
  node,
  patchProps,
  onSelectApp,
  onFocusField,
  templateVars,
}: PropsFormShared & {
  onSelectApp: (space: string, app: string) => void
}) {
  const { t } = useTranslation()
  const { clusters } = useCluster()
  const { currentSpace } = useSpaces()
  const { data: applications } = useApplications()
  // Registry credentials feed the pull-secret pick; the Select degrades to a
  // plain input when the list failed to load or is empty (same contract as
  // the build form's credential fields).
  const { data: credentials, isSuccess: credentialsLoaded } = useCredentials()
  const credentialList = credentialsLoaded ? (credentials ?? []) : []
  const registryCredentialNames = credentialList
    .filter((c) => c.registryHosts.length > 0)
    .map((c) => c.name)
  const savedSpace = readStringProp(node, 'space')
  const savedApp = readStringProp(node, 'app')
  const savedCluster = readStringProp(node, 'cluster')
  // A node that never saved a space defaults to the workflow's (locked)
  // space, so picking an app immediately records the intended (space, app)
  // pair; a saved space always wins so legacy nodes keep deploying where
  // their spec says.
  const selectedSpace = savedSpace || currentSpace || ''

  const appOptions: AppOption[] = (applications ?? [])
    .filter((app) => app.namespace === selectedSpace)
    .map((app) => ({ value: app.name, label: app.name }))
  // A saved app missing from the space's list still echoes so the spec never
  // silently loses its reference.
  const appEcho =
    savedApp !== '' &&
    selectedSpace !== '' &&
    !appOptions.some((option) => option.value === savedApp)
      ? { value: savedApp, label: savedApp }
      : undefined
  const options = appEcho ? [appEcho, ...appOptions] : appOptions

  // Clusters the selected app already has configs on sort first; the rest
  // stay selectable so a brand-new binding can be created.
  const clusterOptions = useMemo(() => {
    const usable = clusters.filter((cluster) => !cluster.error)
    if (savedSpace === '' || savedApp === '') return usable
    const app = applications?.find(
      (candidate) =>
        candidate.namespace === savedSpace && candidate.name === savedApp
    )
    if (!app) return usable
    const bound = new Set(app.configs.map((config) => config.cluster))
    return [
      ...usable.filter((cluster) => bound.has(cluster.name)),
      ...usable.filter((cluster) => !bound.has(cluster.name)),
    ]
  }, [clusters, applications, savedSpace, savedApp])
  const clusterEcho =
    savedCluster && !clusterOptions.some((c) => c.name === savedCluster)
      ? savedCluster
      : undefined
  const [advancedOpen, setAdvancedOpen] = useState(node.props?.dryRun === true)

  return (
    <div className="space-y-4">
      <Field
        label={t('workflow.props.space')}
        hint={t('workflow.props.spaceFixedHint')}
      >
        <Input value={selectedSpace} disabled />
      </Field>
      <Field label={t('workflow.props.app')}>
        <AppCombobox
          value={savedApp}
          label={savedApp}
          options={options}
          echoValue={appEcho}
          disabled={selectedSpace === ''}
          onSelect={(option) => onSelectApp(selectedSpace, option.value)}
          placeholder={
            selectedSpace === ''
              ? t('workflow.props.appPickSpaceHint')
              : t('workflow.props.appPlaceholder')
          }
          searchPlaceholder={t('workflow.props.appSearch')}
          emptyText={t('workflow.props.appEmpty')}
          currentLabel={t('workflow.props.currentValue')}
        />
      </Field>
      <Field label={t('workflow.props.cluster')}>
        <Select
          value={savedCluster}
          onValueChange={(v) => patchProps('cluster', v)}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder={t('workflow.props.clusterPlaceholder')} />
          </SelectTrigger>
          <SelectContent>
            {clusterOptions.map((cluster) => (
              <SelectItem key={cluster.name} value={cluster.name}>
                {cluster.name}
              </SelectItem>
            ))}
            {clusterEcho && (
              <SelectItem value={clusterEcho}>
                {clusterEcho}{' '}
                <span className="text-muted-foreground">
                  {t('workflow.props.currentValue')}
                </span>
              </SelectItem>
            )}
          </SelectContent>
        </Select>
      </Field>
      <Field
        label={t('workflow.props.image')}
        hint={t('workflow.props.imageHint')}
      >
        <TemplateInput
          {...templateVars}
          value={readStringProp(node, 'image')}
          onValueChange={(v) => patchProps('image', v || undefined)}
          onFocus={() => onFocusField('image')}
          placeholder="{{steps.build1.outputs.imageRef}}"
        />
      </Field>
      <CredentialRefField
        propKey="registryCredentialRef"
        saved={readStringProp(node, 'registryCredentialRef')}
        names={registryCredentialNames}
        label={t('workflow.props.registryCredentialRef')}
        hint={t('workflow.props.deployRegistryCredentialRefHint')}
        noneLabel={t('workflow.props.credentialRefNone')}
        currentLabel={t('workflow.props.currentValue')}
        patchProps={patchProps}
        onFocusField={onFocusField}
      />
      <AdvancedCollapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <label className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={node.props?.dryRun === true}
            onCheckedChange={(checked) =>
              patchProps('dryRun', checked === true)
            }
          />
          {t('workflow.props.dryRun')}
        </label>
      </AdvancedCollapsible>
    </div>
  )
}

// Searchable single select for the deploy target app, following the
// NamespaceSelector Popover+Command pattern. `echoValue` (a saved value not
// in the application list) is appended to `options` by the caller and marked
// as the current value.
function AppCombobox({
  value,
  label,
  options,
  echoValue,
  disabled,
  onSelect,
  placeholder,
  searchPlaceholder,
  emptyText,
  currentLabel,
}: {
  value: string
  label: string
  options: AppOption[]
  echoValue: AppOption | undefined
  disabled?: boolean
  onSelect: (option: AppOption) => void
  placeholder: string
  searchPlaceholder: string
  emptyText: string
  currentLabel: string
}) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className="w-full justify-between font-normal"
        >
          <span className={cn('truncate', !value && 'text-muted-foreground')}>
            {value ? label : placeholder}
          </span>
          <IconChevronDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[var(--radix-popover-trigger-width)] p-0"
      >
        <Command>
          <CommandInput placeholder={searchPlaceholder} className="h-9" />
          <CommandList>
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup>
              {options.map((option) => (
                <CommandItem
                  key={option.value}
                  value={option.value}
                  onSelect={() => {
                    onSelect(option)
                    setOpen(false)
                  }}
                >
                  <IconCheck
                    className={cn(
                      'mr-2 h-4 w-4 shrink-0',
                      option.value === value ? 'opacity-100' : 'opacity-0'
                    )}
                  />
                  <span className="truncate">{option.label}</span>
                  {echoValue !== undefined &&
                    option.value === echoValue.value && (
                      <span className="ml-auto shrink-0 pl-2 text-xs text-muted-foreground">
                        {currentLabel}
                      </span>
                    )}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

function ApprovalPropsForm({
  node,
  patchProps,
  onFocusField,
  templateVars,
}: PropsFormShared) {
  const { t } = useTranslation()
  // Assignees are user IDs ([]uint on the backend); the directory turns them
  // into a searchable username picker. It degrades to the digits-only input
  // when the directory is not usable — pending counts as unavailable, so the
  // field is always editable.
  const { data: directory } = useUserDirectory()
  const directoryUsers = directory?.users ?? []
  const pickerEnabled =
    directory !== undefined && !directory.forbidden && directoryUsers.length > 0
  const assignees = (node.props?.assignees as number[] | undefined) ?? []

  // A stored ID missing from the directory (deleted or disabled user) still
  // shows, as #<id>, so the saved spec never silently loses assignees.
  const assigneeLabel = (id: number) =>
    directoryUsers.find((user) => user.id === id)?.username ?? `#${id}`

  const toggleAssignee = (id: number) => {
    const next = assignees.includes(id)
      ? assignees.filter((existing) => existing !== id)
      : [...assignees, id]
    patchProps('assignees', next.length > 0 ? next : undefined)
  }

  return (
    <div className="space-y-4">
      <Field
        label={t('workflow.props.assignees')}
        hint={
          pickerEnabled
            ? t('workflow.props.assigneesHint')
            : t('workflow.props.assigneesNoDirectory')
        }
      >
        {pickerEnabled ? (
          <div className="space-y-1.5">
            <AssigneeCombobox
              users={directoryUsers}
              selected={assignees}
              summary={
                assignees.length > 0
                  ? t('workflow.props.assigneesPicked', {
                      count: assignees.length,
                    })
                  : t('workflow.props.assigneesPlaceholder')
              }
              searchPlaceholder={t('workflow.props.assigneesSearch')}
              emptyText={t('workflow.props.assigneesEmpty')}
              onToggle={toggleAssignee}
            />
            {assignees.length > 0 && (
              <div className="flex flex-wrap gap-1.5">
                {assignees.map((id) => (
                  <Badge key={id} variant="secondary">
                    {assigneeLabel(id)}
                    <button
                      type="button"
                      aria-label={t('workflow.props.assigneesRemove')}
                      onClick={() => toggleAssignee(id)}
                      className="transition-colors hover:text-destructive"
                    >
                      <IconX />
                    </button>
                  </Badge>
                ))}
              </div>
            )}
          </div>
        ) : (
          <Input
            value={assignees.join(',')}
            // Keep the input digits-only so a non-numeric entry can never
            // reach the spec — an unparsable assignee would silently drop the
            // node from the timeout patrol and the assignee check.
            onChange={(e) => {
              const digits = e.target.value.replace(/[^0-9,]/g, '')
              patchProps(
                'assignees',
                digits
                  .split(',')
                  .map((v) => v.trim())
                  .filter((v) => v !== '')
                  .map((v) => Number(v))
              )
            }}
            onFocus={() => onFocusField('assignees')}
            placeholder="3,7"
          />
        )}
      </Field>
      <Field label={t('workflow.props.message')}>
        <TemplateInput
          {...templateVars}
          multiline
          rows={2}
          value={readStringProp(node, 'message')}
          onValueChange={(v) => patchProps('message', v || undefined)}
          onFocus={() => onFocusField('message')}
        />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field
          label={t('workflow.editor.timeout')}
          hint={t('workflow.editor.timeoutHint')}
        >
          <Input
            value={readStringProp(node, 'timeout')}
            onChange={(e) => patchProps('timeout', e.target.value || undefined)}
            onFocus={() => onFocusField('timeout')}
            placeholder="24h"
          />
        </Field>
        <Field label={t('workflow.props.timeoutAction')}>
          <Select
            value={
              (readStringProp(node, 'timeoutAction') || 'reject') as string
            }
            onValueChange={(v) =>
              patchProps('timeoutAction', v === 'reject' ? undefined : v)
            }
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="reject">
                {t('workflow.props.timeoutActionReject')}
              </SelectItem>
              <SelectItem value="approve">
                {t('workflow.props.timeoutActionApprove')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>
    </div>
  )
}

// Multi-select popover for approval assignees, following the AppCombobox
// Popover+Command pattern. Selection toggles in place (the popover stays
// open); the node keeps storing numeric user IDs, usernames are display only.
function AssigneeCombobox({
  users,
  selected,
  summary,
  searchPlaceholder,
  emptyText,
  onToggle,
}: {
  users: UserDirectoryEntry[]
  selected: number[]
  summary: string
  searchPlaceholder: string
  emptyText: string
  onToggle: (id: number) => void
}) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className="w-full justify-between font-normal"
        >
          <span
            className={cn(
              'truncate',
              selected.length === 0 && 'text-muted-foreground'
            )}
          >
            {summary}
          </span>
          <IconChevronDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[var(--radix-popover-trigger-width)] p-0"
      >
        <Command>
          <CommandInput placeholder={searchPlaceholder} className="h-9" />
          <CommandList>
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup>
              {users.map((user) => (
                <CommandItem
                  key={user.id}
                  value={user.username}
                  onSelect={() => onToggle(user.id)}
                >
                  <IconCheck
                    className={cn(
                      'mr-2 h-4 w-4 shrink-0',
                      selected.includes(user.id) ? 'opacity-100' : 'opacity-0'
                    )}
                  />
                  <span className="truncate">{user.username}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}

function NotifyPropsForm({
  node,
  patchProps,
  onFocusField,
  templateVars,
}: PropsFormShared) {
  const { t } = useTranslation()
  return (
    <div className="space-y-4">
      <Field label={t('workflow.props.channel')}>
        <Input value="webhook" disabled />
      </Field>
      <Field label={t('workflow.props.url')}>
        <Input
          value={readStringProp(node, 'url')}
          onChange={(e) => patchProps('url', e.target.value)}
          onFocus={() => onFocusField('url')}
          placeholder="https://hooks.example.com/…"
        />
      </Field>
      <Field
        label={t('workflow.props.payloadTemplate')}
        hint={t('workflow.props.payloadTemplateHint')}
      >
        <TemplateInput
          {...templateVars}
          multiline
          rows={5}
          className="font-mono text-xs"
          value={readStringProp(node, 'payloadTemplate')}
          onValueChange={(v) => patchProps('payloadTemplate', v || undefined)}
          onFocus={() => onFocusField('payloadTemplate')}
        />
      </Field>
    </div>
  )
}

function readNestedString(
  node: WorkflowNode,
  group: string,
  key: string
): string {
  const nested = node.props?.[group]
  if (nested && typeof nested === 'object' && !Array.isArray(nested)) {
    const value = (nested as Record<string, unknown>)[key]
    if (typeof value === 'string') return value
  }
  return ''
}

function readArgs(node: WorkflowNode): string {
  const args = node.props?.args
  if (args && typeof args === 'object' && !Array.isArray(args)) {
    return Object.entries(args as Record<string, string>)
      .map(([k, v]) => `${k}=${v}`)
      .join('\n')
  }
  return ''
}

function parseArgs(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const idx = line.indexOf('=')
    if (idx <= 0) continue
    const key = line.slice(0, idx).trim()
    if (key === '') continue
    out[key] = line.slice(idx + 1)
  }
  return Object.keys(out).length > 0 ? out : undefined
}

function splitList(text: string): string[] | undefined {
  const items = text
    .split(',')
    .map((v) => v.trim())
    .filter((v) => v !== '')
  return items.length > 0 ? items : undefined
}
