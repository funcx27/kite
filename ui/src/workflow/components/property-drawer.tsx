// Editor property drawer: per node type, one form following spec.go's props
// structs (BuildProps / DeployProps / ApprovalProps / NotifyProps) plus the
// shared node fields (id / name / onFailure / timeout). credentialRef is a
// plain text input for M4 (design §11).
import { useMemo, useRef } from 'react'
import { IconTrash } from '@tabler/icons-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'

import { isValidNodeNodeId, nodeTypeAccent } from '../lib'
import type { ParamSpec, WorkflowNode, WorkflowNodeType } from '../types'

export interface PropertyDrawerProps {
  node: WorkflowNode | null
  params: ParamSpec[]
  // All current editor nodes: feeds steps.<id>.outputs.<key> template chips.
  nodes: WorkflowNode[]
  onChange: (node: WorkflowNode) => void
  onDelete: () => void
}

// Known output keys per node type, feeding the steps.* template chips
// (mirrors the runners' outputs in engine/runner.go).
const outputKeysByType: Record<WorkflowNodeType, string[]> = {
  'build-image': ['imageRef', 'digest', 'tag'],
  'deploy-app': ['revision', 'preview'],
  approval: [],
  notify: ['delivered'],
}

export function PropertyDrawer({
  node,
  params,
  nodes,
  onChange,
  onDelete,
}: PropertyDrawerProps) {
  const { t } = useTranslation()
  // The template-var chips insert into the field the user focused last.
  const focusedField = useRef<string | null>(null)

  const templateVars = useMemo(() => {
    const vars = params.map((p) => `params.${p.name}`)
    vars.push('run.number', 'run.status', 'run.url')
    if (node) {
      for (const other of nodes) {
        if (other.id === node.id) continue
        for (const key of outputKeysByType[other.type] ?? []) {
          vars.push(`steps.${other.id}.outputs.${key}`)
        }
      }
    }
    return vars
  }, [params, node, nodes])

  const insertVar = (token: string) => {
    if (!node || !focusedField.current) return
    const field = focusedField.current
    if (field === '__name__') {
      onChange({ ...node, name: node.name + token })
      return
    }
    if (field.includes('.')) {
      const [group, key] = field.split('.')
      const nested = {
        ...((node.props?.[group] as Record<string, unknown>) ?? {}),
      }
      nested[key] = String(nested[key] ?? '') + token
      onChange({ ...node, props: { ...(node.props ?? {}), [group]: nested } })
      return
    }
    setStringProp(node, field, readStringProp(node, field) + token, onChange)
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
            node={node}
            patchProps={patchProps}
            onFocusField={(f) => (focusedField.current = f)}
          />
        )}
        {node.type === 'deploy-app' && (
          <DeployPropsForm
            node={node}
            patchProps={patchProps}
            onFocusField={(f) => (focusedField.current = f)}
          />
        )}
        {node.type === 'approval' && (
          <ApprovalPropsForm
            node={node}
            patchProps={patchProps}
            onFocusField={(f) => (focusedField.current = f)}
          />
        )}
        {node.type === 'notify' && (
          <NotifyPropsForm
            node={node}
            patchProps={patchProps}
            onFocusField={(f) => (focusedField.current = f)}
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

function BuildPropsForm({ node, patchProps, onFocusField }: PropsFormShared) {
  const { t } = useTranslation()
  const gitUrl = readNestedString(node, 'git', 'url')
  const sourceKind = gitUrl
    ? 'git'
    : readStringProp(node, 'httpUrl')
      ? 'http'
      : 'git'
  const args = readArgs(node)

  const setGit = (key: string, value: string) => {
    const git = { ...((node.props?.git as Record<string, unknown>) ?? {}) }
    if (value === '') delete git[key]
    else git[key] = value
    patchProps('git', Object.keys(git).length > 0 ? git : undefined)
  }

  return (
    <div className="space-y-4">
      <Field
        label={t('workflow.props.credentialRef')}
        hint={t('workflow.props.credentialRefHint')}
      >
        <Input
          value={readStringProp(node, 'credentialRef')}
          onChange={(e) =>
            patchProps('credentialRef', e.target.value || undefined)
          }
          onFocus={() => onFocusField('credentialRef')}
        />
      </Field>
      <Field label={t('workflow.props.gitUrl')}>
        <Input
          value={gitUrl}
          onChange={(e) => setGit('url', e.target.value)}
          onFocus={() => onFocusField('git.url')}
          placeholder="https://github.com/org/repo.git"
        />
      </Field>
      {sourceKind === 'git' && (
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('workflow.props.gitRef')}>
            <Input
              value={readNestedString(node, 'git', 'ref')}
              onChange={(e) => setGit('ref', e.target.value)}
              onFocus={() => onFocusField('git.ref')}
              placeholder="main"
            />
          </Field>
          <Field label={t('workflow.props.gitSubdir')}>
            <Input
              value={readNestedString(node, 'git', 'subdir')}
              onChange={(e) => setGit('subdir', e.target.value)}
              onFocus={() => onFocusField('git.subdir')}
            />
          </Field>
        </div>
      )}
      {!gitUrl && (
        <Field label={t('workflow.props.httpUrl')}>
          <Input
            value={readStringProp(node, 'httpUrl')}
            onChange={(e) => patchProps('httpUrl', e.target.value || undefined)}
            onFocus={() => onFocusField('httpUrl')}
          />
        </Field>
      )}
      <Field label={t('workflow.props.dockerfile')}>
        <Input
          value={readStringProp(node, 'dockerfile')}
          onChange={(e) =>
            patchProps('dockerfile', e.target.value || undefined)
          }
          onFocus={() => onFocusField('dockerfile')}
          placeholder="Dockerfile"
        />
      </Field>
      <Field label={t('workflow.props.dockerfileContent')}>
        <Textarea
          value={readStringProp(node, 'dockerfileContent')}
          onChange={(e) =>
            patchProps('dockerfileContent', e.target.value || undefined)
          }
          onFocus={() => onFocusField('dockerfileContent')}
          rows={4}
          className="font-mono text-xs"
        />
      </Field>
      <Field
        label={t('workflow.props.args')}
        hint={t('workflow.props.argsHint')}
      >
        <Textarea
          value={args}
          onChange={(e) => patchProps('args', parseArgs(e.target.value))}
          onFocus={() => onFocusField('args')}
          rows={3}
          className="font-mono text-xs"
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
      <Field
        label={t('workflow.props.outputRef')}
        hint={t('workflow.props.outputRefHint')}
      >
        <Input
          value={readStringProp(node, 'outputRef')}
          onChange={(e) => patchProps('outputRef', e.target.value || undefined)}
          onFocus={() => onFocusField('outputRef')}
        />
      </Field>
    </div>
  )
}

function DeployPropsForm({ node, patchProps, onFocusField }: PropsFormShared) {
  const { t } = useTranslation()
  return (
    <div className="space-y-4">
      <Field label={t('workflow.props.app')}>
        <Input
          value={readStringProp(node, 'app')}
          onChange={(e) => patchProps('app', e.target.value)}
          onFocus={() => onFocusField('app')}
        />
      </Field>
      <Field label={t('workflow.props.cluster')}>
        <Input
          value={readStringProp(node, 'cluster')}
          onChange={(e) => patchProps('cluster', e.target.value)}
          onFocus={() => onFocusField('cluster')}
        />
      </Field>
      <Field
        label={t('workflow.props.image')}
        hint={t('workflow.props.imageHint')}
      >
        <Input
          value={readStringProp(node, 'image')}
          onChange={(e) => patchProps('image', e.target.value || undefined)}
          onFocus={() => onFocusField('image')}
          placeholder="{{steps.build1.outputs.imageRef}}"
        />
      </Field>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={node.props?.dryRun === true}
          onCheckedChange={(checked) => patchProps('dryRun', checked === true)}
        />
        {t('workflow.props.dryRun')}
      </label>
    </div>
  )
}

function ApprovalPropsForm({
  node,
  patchProps,
  onFocusField,
}: PropsFormShared) {
  const { t } = useTranslation()
  const assignees =
    (node.props?.assignees as Array<number | string> | undefined) ?? []
  return (
    <div className="space-y-4">
      <Field
        label={t('workflow.props.assignees')}
        hint={t('workflow.props.assigneesHint')}
      >
        <Input
          value={assignees.join(',')}
          // Assignees are user IDs ([]uint on the backend): keep the input
          // digits-only so a non-numeric entry can never reach the spec — an
          // unparsable assignee would silently drop the node from the timeout
          // patrol and the assignee check.
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
      </Field>
      <Field label={t('workflow.props.message')}>
        <Textarea
          value={readStringProp(node, 'message')}
          onChange={(e) => patchProps('message', e.target.value || undefined)}
          onFocus={() => onFocusField('message')}
          rows={2}
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

function NotifyPropsForm({ node, patchProps, onFocusField }: PropsFormShared) {
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
        <Textarea
          value={readStringProp(node, 'payloadTemplate')}
          onChange={(e) =>
            patchProps('payloadTemplate', e.target.value || undefined)
          }
          onFocus={() => onFocusField('payloadTemplate')}
          rows={5}
          className="font-mono text-xs"
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
