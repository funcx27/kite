// Editor property drawer: per node type, one form following spec.go's props
// structs (BuildProps / DeployProps / ApprovalProps / NotifyProps) plus the
// shared node fields (id / name / onFailure / timeout). Lists that exist on
// the host (clusters, applications, credentials) render as selects and fall
// back to free text when the list cannot serve a pick.
import { useMemo, useRef, useState } from 'react'
import { useApplications, useSpaceQuotas } from '@/portal/application/api'
import { DockerfileEditorDialog } from '@/portal/components/editors/dockerfile-editor-dialog'
import { useSpaces } from '@/portal/space-context'
import {
  IconAdjustmentsHorizontal,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconMaximize,
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

import { useCredentials, useDefaultRegistry, useUserDirectory } from '../api'
import type { UserDirectoryEntry } from '../api'
import { isValidNodeNodeId, nodeTypeAccent } from '../lib'
import type { DAGSpec, ParamSpec, WorkflowNode } from '../types'
import { TemplateInput } from './template-input'

// Session-only deploy-target lock staged by the editor's application entry
// (?fromApp): every deploy-app form shows the locked app/cluster values
// grayed out instead of offering picks. Never part of the spec.
export interface DeployTargetLock {
  app: string
  cluster?: string
}

export interface PropertyDrawerProps {
  node: WorkflowNode | null
  params: ParamSpec[]
  // All current editor nodes: feeds steps.<id>.outputs.<key> template chips.
  nodes: WorkflowNode[]
  // Current editor edges: steps.* suggestions skip the node's DAG descendants.
  edges?: DAGSpec['edges']
  lockedTarget?: DeployTargetLock
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
}: {
  propKey: string
  saved: string
  names: string[]
  label: string
  hint: string
  noneLabel: string
  currentLabel: string
  patchProps: (key: string, value: unknown) => void
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
        />
      )}
    </Field>
  )
}

export function PropertyDrawer({
  node,
  params,
  nodes,
  edges,
  lockedTarget,
  onChange,
  onDelete,
}: PropertyDrawerProps) {
  const { t } = useTranslation()
  // '__id__' while the node-id input holds focus — gates its charset hint.
  const focusedField = useRef<string | null>(null)

  // git.* refs exist only on a build-image node whose props carry a git
  // source object (values may be template strings; only presence matters).
  const gitScope = node?.type === 'build-image' && !!node?.props?.git

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

  // Node-level fields (onFailure/timeout) live inside each type form's
  // advanced section, so the forms need the same node-snapshot patch path.
  const patchNode = (updates: Partial<WorkflowNode>) =>
    onChange({ ...node, ...updates })

  // Selecting a deploy target writes the space+app pair in one change: two
  // separate patchProps calls would race on the same stale node snapshot and
  // drop the first field.
  const patchAppTarget = (space: string, app: string) =>
    onChange({
      ...node,
      props: { ...(node.props ?? {}), space, app },
    })

  const templateInputVars = {
    params,
    nodes,
    currentNodeId: node.id,
    gitScope,
    edges,
  }

  return (
    <div className="flex h-full flex-col">
      <DrawerHeader
        title={t('workflow.editor.selectNode')}
        sub={t(`workflow.nodeTypes.${node.type}`)}
        accent={nodeTypeAccent[node.type]}
      />
      <div className="flex-1 space-y-4 overflow-y-auto px-4 py-3">
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
          />
        </Field>
        <Separator />

        {node.type === 'build-image' && (
          <BuildPropsForm
            key={node.id}
            node={node}
            patchProps={patchProps}
            patchNode={patchNode}
            onSelectApp={patchAppTarget}
            templateVars={templateInputVars}
          />
        )}
        {node.type === 'deploy-app' && (
          <DeployPropsForm
            key={node.id}
            node={node}
            patchProps={patchProps}
            patchNode={patchNode}
            onSelectApp={patchAppTarget}
            lockedTarget={lockedTarget}
            templateVars={templateInputVars}
          />
        )}
        {node.type === 'approval' && (
          <ApprovalPropsForm
            key={node.id}
            node={node}
            patchProps={patchProps}
            patchNode={patchNode}
            templateVars={templateInputVars}
          />
        )}
        {node.type === 'notify' && (
          <NotifyPropsForm
            key={node.id}
            node={node}
            patchProps={patchProps}
            patchNode={patchNode}
            templateVars={templateInputVars}
          />
        )}
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
  labelExtra,
  children,
}: {
  label: string
  hint?: string
  /** Right-aligned slot on the label row (e.g. the Dockerfile zoom button). */
  labelExtra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <Label className="text-xs">{label}</Label>
        {labelExtra}
      </div>
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
  /** Patches node-level fields (onFailure/timeout) on the current snapshot. */
  patchNode: (updates: Partial<WorkflowNode>) => void
  // Autocomplete inputs for TemplateInput fields: params, sibling nodes and
  // the owning node id (its own outputs stay out of the suggestions). gitScope
  // offers the git.* refs (build-image nodes with a git source only).
  templateVars: {
    params: ParamSpec[]
    nodes: WorkflowNode[]
    currentNodeId: string
    gitScope?: boolean
    edges?: DAGSpec['edges']
  }
}

function readStringProp(node: WorkflowNode, key: string): string {
  const value = node.props?.[key]
  return typeof value === 'string' ? value : ''
}

// UI mirror of the backend's bare-name rule: a first path segment without a
// dot (domain), a colon (port) or the literal localhost carries no registry
// part, so the build engine prefixes the default registry host at run time.
function isBareImageName(ref: string): boolean {
  const first = ref.split('/')[0]
  return !first.includes('.') && !first.includes(':') && first !== 'localhost'
}

// AdvancedCollapsible folds a props form's low-frequency fields behind a
// full-width block: header and expanded content share one bordered box, so
// the fold reads as a single "advanced" group. Label text sits left, the
// chevron sits at the far right and turns down when open. The owning forms
// are keyed by node id, so each selection re-derives the initial open state
// from that node's stored values.
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
    <Collapsible
      open={open}
      onOpenChange={onOpenChange}
      className="rounded-md border bg-muted"
    >
      <CollapsibleTrigger className="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-xs font-medium text-muted-foreground transition-colors hover:bg-muted/70">
        <span className="flex items-center gap-1.5">
          <IconAdjustmentsHorizontal className="h-3.5 w-3.5" />
          {t('workflow.props.advancedSection')}
        </span>
        <IconChevronRight
          className={cn(
            'h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform',
            open && 'rotate-90'
          )}
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-4 rounded-b-md border-t bg-background px-3 pb-3 pt-3">
        {children}
      </CollapsibleContent>
    </Collapsible>
  )
}

// The shared failure-strategy/timeout fields render inside each type form's
// advanced section (one fold per drawer), so the forms receive patchNode for
// these node-level writes.
function SharedAdvancedFields({
  node,
  patchNode,
}: {
  node: WorkflowNode
  patchNode: (updates: Partial<WorkflowNode>) => void
}) {
  const { t } = useTranslation()
  // Notify nodes run with forced continue semantics (engine
  // effectiveOnFailure): a policy select would be dead config, so it
  // becomes an explanatory note (design §5.4 UI 灰提示).
  if (node.type === 'notify') {
    return (
      <div className="space-y-3">
        <p className="text-[11px] leading-4 text-muted-foreground">
          {t('workflow.editor.notifyFailureNote')}
        </p>
        <Field
          label={t('workflow.editor.timeout')}
          hint={t('workflow.editor.timeoutHint')}
        >
          <Input
            value={node.timeout ?? ''}
            onChange={(e) =>
              patchNode({ timeout: e.target.value || undefined })
            }
            placeholder="24h"
          />
        </Field>
      </div>
    )
  }
  return (
    <div className="grid grid-cols-2 gap-3">
      <Field label={t('workflow.editor.onFailure')}>
        <Select
          value={node.onFailure ?? 'stop'}
          onValueChange={(v) =>
            patchNode({
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
          onChange={(e) => patchNode({ timeout: e.target.value || undefined })}
          placeholder="24h"
        />
      </Field>
    </div>
  )
}

function BuildPropsForm({
  node,
  patchProps,
  patchNode,
  onSelectApp,
  templateVars,
}: PropsFormShared & {
  onSelectApp: (space: string, app: string) => void
}) {
  const { t } = useTranslation()
  const { currentSpace } = useSpaces()
  const { data: applications } = useApplications()
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
  // Bare-name completion preview: mirrors the backend rule (first segment
  // with no dot, no colon and not localhost = no registry part), display
  // only — the form value keeps what the user typed.
  const { data: defaultRegistry } = useDefaultRegistry()
  const outputRef = readStringProp(node, 'outputRef')
  const resolvedOutputRef =
    defaultRegistry?.valid && outputRef && isBareImageName(outputRef)
      ? `${defaultRegistry.host}/${outputRef}`
      : undefined
  // The advanced section starts collapsed unless one of its fields already
  // carries a value, so configured options never hide behind the fold.
  const [advancedOpen, setAdvancedOpen] = useState(
    node.onFailure !== undefined ||
      node.timeout !== undefined ||
      readNestedString(node, 'git', 'subdir') !== '' ||
      readStringProp(node, 'httpUrl') !== '' ||
      args !== '' ||
      ((node.props?.platforms as string[] | undefined)?.length ?? 0) > 0
  )
  // The form is keyed by node.id, so this open flag resets per node.
  const [dockerfileEditorOpen, setDockerfileEditorOpen] = useState(false)
  const dockerfile = readStringProp(node, 'dockerfile')
  // Display-only mirror of the backend's rule: any whitespace inside the
  // trimmed value marks inline Dockerfile content (the backend decides).
  const dockerfileInline = /\s/.test(dockerfile.trim())

  // Auto-naming target, following the deploy form's app/space pattern: the
  // space is fixed to the workflow's own space and only the app is picked.
  // Picking an app writes the (space, app) pair in one change; the pair
  // names the auto-generated outputRef when the ref field stays empty.
  const savedSpace = readStringProp(node, 'space')
  const savedApp = readStringProp(node, 'app')
  const selectedSpace = savedSpace || currentSpace || ''
  const appOptions: AppOption[] = (applications ?? [])
    .filter((app) => app.namespace === selectedSpace)
    .map((app) => ({ value: app.name, label: app.name }))
  const appEcho =
    savedApp !== '' &&
    selectedSpace !== '' &&
    !appOptions.some((option) => option.value === savedApp)
      ? { value: savedApp, label: savedApp }
      : undefined

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
      />
      <Field label={t('workflow.props.gitUrl')}>
        <TemplateInput
          {...templateVars}
          value={gitUrl}
          onValueChange={(v) => setGit('url', v)}
          placeholder="https://github.com/org/repo.git"
        />
      </Field>
      {sourceKind === 'git' && (
        <Field label={t('workflow.props.gitRef')}>
          <TemplateInput
            {...templateVars}
            value={readNestedString(node, 'git', 'ref')}
            onValueChange={(v) => setGit('ref', v)}
            placeholder="main"
          />
        </Field>
      )}
      {sourceKind === 'git' && (
        <Field
          label={t('workflow.props.gitMode')}
          hint={t('workflow.props.gitModeHint')}
        >
          <Select
            value={readNestedString(node, 'git', 'mode') || 'server'}
            onValueChange={(v) => setGit('mode', v === 'server' ? '' : v)}
          >
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="server">
                {t('workflow.props.gitModeServer')}
              </SelectItem>
              <SelectItem value="runtime">
                {t('workflow.props.gitModeRuntime')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
      )}
      <Field
        label={t('workflow.props.dockerfile')}
        hint={t('workflow.props.dockerfileHint')}
        labelExtra={
          <span className="flex items-center gap-1">
            {dockerfileInline && (
              <Badge variant="secondary">
                {t('workflow.props.dockerfileInline')}
              </Badge>
            )}
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-6 w-6 text-muted-foreground"
              aria-label={t('workflow.props.dockerfileEnlarge')}
              title={t('workflow.props.dockerfileEnlarge')}
              onClick={() => setDockerfileEditorOpen(true)}
            >
              <IconMaximize className="h-3.5 w-3.5" />
            </Button>
          </span>
        }
      >
        <TemplateInput
          {...templateVars}
          value={dockerfile}
          onValueChange={(v) => patchProps('dockerfile', v || undefined)}
          placeholder="Dockerfile"
        />
      </Field>
      <Field
        label={t('workflow.props.space')}
        hint={t('workflow.props.buildSpaceFixedHint')}
      >
        <Input value={selectedSpace} disabled />
      </Field>
      <Field label={t('workflow.props.app')}>
        <AppCombobox
          value={savedApp}
          label={savedApp}
          options={appEcho ? [appEcho, ...appOptions] : appOptions}
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
      <Field
        label={t('workflow.props.outputRef')}
        hint={t('workflow.props.outputRefHint')}
      >
        <TemplateInput
          {...templateVars}
          value={outputRef}
          onValueChange={(v) => patchProps('outputRef', v || undefined)}
          placeholder={
            defaultRegistry?.valid
              ? t('workflow.props.outputRefAutoPlaceholder')
              : undefined
          }
        />
        {resolvedOutputRef && (
          <p className="break-all font-mono text-[11px] leading-4 text-muted-foreground">
            {t('workflow.props.outputRefResolved', {
              ref: resolvedOutputRef,
            })}
          </p>
        )}
        {defaultRegistry?.valid && !outputRef && (
          <p className="break-all font-mono text-[11px] leading-4 text-muted-foreground">
            {t('workflow.props.outputRefAutoHint', {
              ref: `${defaultRegistry.host}/${selectedSpace || '…'}/${savedApp || '…'}:<commit>[-<fingerprint>]`,
            })}
          </p>
        )}
      </Field>
      <AdvancedCollapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <SharedAdvancedFields node={node} patchNode={patchNode} />
        {sourceKind === 'git' && (
          <Field label={t('workflow.props.gitSubdir')}>
            <TemplateInput
              {...templateVars}
              value={readNestedString(node, 'git', 'subdir')}
              onValueChange={(v) => setGit('subdir', v)}
            />
          </Field>
        )}
        {!gitUrl && (
          <Field label={t('workflow.props.httpUrl')}>
            <TemplateInput
              {...templateVars}
              value={readStringProp(node, 'httpUrl')}
              onValueChange={(v) => patchProps('httpUrl', v || undefined)}
            />
          </Field>
        )}
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
          />
        </Field>
        <Field
          label={t('workflow.props.platforms')}
          hint={t('workflow.props.platformsHint')}
        >
          <PlatformsSelect
            values={(node.props?.platforms as string[] | undefined) ?? []}
            onChange={(next) =>
              patchProps('platforms', next.length > 0 ? next : undefined)
            }
          />
        </Field>
      </AdvancedCollapsible>
      <DockerfileEditorDialog
        open={dockerfileEditorOpen}
        onOpenChange={setDockerfileEditorOpen}
        value={dockerfile}
        onCommit={(draft) => patchProps('dockerfile', draft || undefined)}
        title={t('workflow.props.dockerfile')}
        hint={t('workflow.dockerfileEditor.templateHint')}
        lineCountLabel={(count) =>
          t('workflow.dockerfileEditor.lines', { count })
        }
      />
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
  patchNode,
  onSelectApp,
  lockedTarget,
  templateVars,
}: PropsFormShared & {
  onSelectApp: (space: string, app: string) => void
  lockedTarget?: DeployTargetLock
}) {
  const { t } = useTranslation()
  const { clusters } = useCluster()
  const { currentSpace, spaceClusters } = useSpaces()
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
  // Session lock: the disabled controls only display the locked values —
  // they never write back, so the lock stays out of the spec.
  const lockedApp = lockedTarget?.app
  const lockedCluster = lockedTarget?.cluster
  const displayApp = lockedApp ?? savedApp
  const displayCluster = lockedCluster ?? savedCluster
  // A node that never saved a space defaults to the workflow's (locked)
  // space, so picking an app immediately records the intended (space, app)
  // pair; a saved space always wins so legacy nodes keep deploying where
  // their spec says.
  const selectedSpace = savedSpace || currentSpace || ''
  // A space is deployable only where it was adopted — a configured managed
  // quota. The labeled namespace alone can pre-exist on non-adopted clusters
  // (quota row `unconfigured`), so namespace presence is not a sufficient
  // scope.
  const { data: spaceQuotaRows, isError: spaceQuotasFailed } = useSpaceQuotas(
    selectedSpace === '' ? undefined : selectedSpace
  )

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
  // stay selectable so a brand-new binding can be created. A space-bound
  // node offers only the space's adopted clusters; an unset space (platform
  // flow / all spaces) keeps the full list.
  const clusterOptions = useMemo(() => {
    const usable = clusters.filter((cluster) => !cluster.error)
    // Adopted = quota configured AND the labeled namespace present: the
    // namespace can pre-exist without a quota (quota row `unconfigured`),
    // and the managed quota lingers after a dissociate (the marker is only
    // removed from the namespace) — the intersection drops both.
    const nsSet = new Set(spaceClusters[selectedSpace] ?? [])
    const adoptedSet =
      selectedSpace === '' || spaceQuotasFailed
        ? null
        : new Set(
            (spaceQuotaRows?.quotas ?? [])
              .filter(
                (row) =>
                  !row.unconfigured &&
                  !row.unauthorized &&
                  !row.error &&
                  nsSet.has(row.cluster)
              )
              .map((row) => row.cluster)
          )
    const scoped = adoptedSet
      ? usable.filter((cluster) => adoptedSet.has(cluster.name))
      : usable
    if (savedSpace === '' || savedApp === '') return scoped
    const app = applications?.find(
      (candidate) =>
        candidate.namespace === savedSpace && candidate.name === savedApp
    )
    if (!app) return scoped
    const bound = new Set(app.configs.map((config) => config.cluster))
    return [
      ...scoped.filter((cluster) => bound.has(cluster.name)),
      ...scoped.filter((cluster) => !bound.has(cluster.name)),
    ]
  }, [
    clusters,
    applications,
    savedSpace,
    savedApp,
    selectedSpace,
    spaceClusters,
    spaceQuotaRows,
    spaceQuotasFailed,
  ])
  const clusterEcho =
    displayCluster && !clusterOptions.some((c) => c.name === displayCluster)
      ? displayCluster
      : undefined
  const [advancedOpen, setAdvancedOpen] = useState(
    node.onFailure !== undefined ||
      node.timeout !== undefined ||
      node.props?.dryRun === true
  )

  return (
    <div className="space-y-4">
      <Field
        label={t('workflow.props.space')}
        hint={t('workflow.props.spaceFixedHint')}
      >
        <Input value={selectedSpace} disabled />
      </Field>
      <Field
        label={t('workflow.props.app')}
        hint={
          lockedApp !== undefined ? t('workflow.props.lockedHint') : undefined
        }
      >
        <AppCombobox
          value={displayApp}
          label={displayApp}
          options={options}
          echoValue={appEcho}
          disabled={lockedApp !== undefined || selectedSpace === ''}
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
          value={displayCluster}
          onValueChange={(v) => patchProps('cluster', v)}
          disabled={lockedCluster !== undefined}
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
      />
      <AdvancedCollapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <SharedAdvancedFields node={node} patchNode={patchNode} />
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
  patchNode,
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

  // Approval owns two timeout pairs: the shared node-level onFailure/timeout
  // and its own approval timeout/timeoutAction props — both fold into one
  // advanced section so the drawer keeps a single fold.
  const [advancedOpen, setAdvancedOpen] = useState(
    node.onFailure !== undefined ||
      node.timeout !== undefined ||
      readStringProp(node, 'timeout') !== '' ||
      readStringProp(node, 'timeoutAction') !== ''
  )

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
        />
      </Field>
      <AdvancedCollapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <SharedAdvancedFields node={node} patchNode={patchNode} />
        <div className="grid grid-cols-2 gap-3">
          <Field
            label={t('workflow.props.timeout')}
            hint={t('workflow.props.timeoutHint')}
          >
            <Input
              value={readStringProp(node, 'timeout')}
              onChange={(e) =>
                patchProps('timeout', e.target.value || undefined)
              }
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
      </AdvancedCollapsible>
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
  patchNode,
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
        />
      </Field>
      {/* No advanced fold here: notify keeps only timeout (plus the
          forced-continue note), so the fields render inline. */}
      <SharedAdvancedFields node={node} patchNode={patchNode} />
    </div>
  )
}

// Platforms are fixed OCI os/arch values handed straight to buildkit
// (joined with commas); a multi-select popover keeps typos out of the spec.
// The preset covers the arch pairs this fleet actually builds (Java/C apps);
// stored values outside the list stay listed and removable so legacy specs
// never lose their entries.
const PLATFORM_OPTIONS = ['linux/amd64', 'linux/arm64']

function PlatformsSelect({
  values,
  onChange,
}: {
  values: string[]
  onChange: (next: string[]) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const options = [...new Set([...PLATFORM_OPTIONS, ...values])]
  const toggle = (platform: string) =>
    onChange(
      values.includes(platform)
        ? values.filter((v) => v !== platform)
        : [...values, platform].sort()
    )
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
              values.length === 0 && 'text-muted-foreground'
            )}
          >
            {values.length > 0
              ? values.join(', ')
              : t('workflow.props.platformsPlaceholder')}
          </span>
          <IconChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-56 p-1" align="start">
        {options.map((platform) => (
          <button
            key={platform}
            type="button"
            role="option"
            aria-selected={values.includes(platform)}
            onClick={() => toggle(platform)}
            className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-accent"
          >
            <Checkbox checked={values.includes(platform)} />
            <span className="font-mono text-xs">{platform}</span>
          </button>
        ))}
      </PopoverContent>
    </Popover>
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
