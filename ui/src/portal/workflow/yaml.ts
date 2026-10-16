// Workflow YAML envelope: the document format of the editor's YAML mode and
// of export/import files. It mirrors the POST/PUT /workflows request body
// with a Kubernetes-style wrapper — { apiVersion, kind, metadata,
// spaces, params, triggers, spec }.
import type { TFunction } from 'i18next'
import * as yaml from 'js-yaml'

import { findCycle, isValidNodeNodeId, WORKFLOW_NODE_TYPES } from './lib'
import type {
  DAGSpec,
  ParamSpec,
  TriggerConfig,
  WorkflowEdge,
  WorkflowNode,
  WorkflowNodeType,
} from './types'

export interface WorkflowYamlEnvelope {
  apiVersion: 'kite.sh/v1'
  kind: 'Workflow'
  metadata: {
    name: string
    description: string
  }
  spaces: string[]
  params: ParamSpec[]
  triggers: TriggerConfig
  spec: DAGSpec
}

// Draft fields the editor carries unwrapped; dump wraps them in the envelope.
export interface WorkflowYamlDraft {
  name: string
  description: string
  spaces: string[]
  params: ParamSpec[]
  triggers: TriggerConfig
  spec: DAGSpec
}

// lineWidth 0 keeps long strings (payload templates, inline Dockerfiles) on
// one line instead of folding them.
const dumpOptions: yaml.DumpOptions = { indent: 2, lineWidth: 0, noRefs: true }

export function draftToWorkflowYaml(draft: WorkflowYamlDraft): string {
  return yaml.dump(
    {
      apiVersion: 'kite.sh/v1',
      kind: 'Workflow',
      metadata: { name: draft.name, description: draft.description },
      spaces: draft.spaces,
      params: draft.params,
      triggers: draft.triggers,
      spec: draft.spec,
    },
    dumpOptions
  )
}

// Skeleton for the editor's ?yaml=1 create entry (list page import).
export function emptyWorkflowYaml(): string {
  return draftToWorkflowYaml({
    name: '',
    description: '',
    spaces: [],
    params: [],
    triggers: {},
    spec: { nodes: [], edges: [] },
  })
}

// <name>.workflow.yaml with filesystem-hostile characters stripped. \p{Cc}
// (not \x00-\x1f) avoids eslint no-control-regex while still covering the
// C0 range (plus DEL/C1 controls, equally hostile to filenames).
export function workflowYamlFilename(name: string): string {
  const safe = name.trim().replace(/[\\/:*?"<>|\p{Cc}]+/gu, '_')
  return `${safe || 'workflow'}.workflow.yaml`
}

// Best-effort metadata.name read for download filenames; any parse or shape
// problem yields null and the caller falls back to the draft name.
export function readEnvelopeName(text: string): string | null {
  try {
    const doc: unknown = yaml.load(text)
    if (!isPlainObject(doc) || !isPlainObject(doc.metadata)) return null
    const name = doc.metadata.name
    return typeof name === 'string' && name.trim() !== '' ? name : null
  } catch {
    return null
  }
}

export type WorkflowYamlParseResult =
  { ok: true; envelope: WorkflowYamlEnvelope } | { ok: false; error: string }

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

// Parse + structural validation of a workflow document. Covers what is
// YAML-specific: syntax, envelope/spec shape, known node types, edge
// endpoints resolving to nodes, template-safe node ids and acyclicity.
// Rules the editor's validate() already enforces on every draft (empty
// name/label, deploy-app needing a space) are deliberately not repeated.
// apiVersion/kind may be omitted in hand-written documents; wrong values
// are rejected.
export function parseWorkflowYaml(
  text: string,
  t: TFunction
): WorkflowYamlParseResult {
  let doc: unknown
  try {
    doc = yaml.load(text)
  } catch (err) {
    const message = err instanceof Error ? err.message.split('\n')[0] : ''
    return { ok: false, error: message || t('workflow.editor.yaml.syntax') }
  }
  if (!isPlainObject(doc)) {
    return { ok: false, error: t('workflow.editor.yaml.notMapping') }
  }
  if (
    (doc.apiVersion !== undefined && doc.apiVersion !== 'kite.sh/v1') ||
    (doc.kind !== undefined && doc.kind !== 'Workflow')
  ) {
    return { ok: false, error: t('workflow.editor.yaml.envelope') }
  }

  const metadata = doc.metadata ?? {}
  if (!isPlainObject(metadata)) {
    return { ok: false, error: t('workflow.editor.yaml.metadata') }
  }
  const name = metadata.name ?? ''
  const description = metadata.description ?? ''
  if (typeof name !== 'string' || typeof description !== 'string') {
    return { ok: false, error: t('workflow.editor.yaml.metadata') }
  }

  const spaces = doc.spaces ?? []
  if (!Array.isArray(spaces) || spaces.some((s) => typeof s !== 'string')) {
    return { ok: false, error: t('workflow.editor.yaml.spaces') }
  }
  const params = doc.params ?? []
  if (!Array.isArray(params) || params.some((p) => !isPlainObject(p))) {
    return { ok: false, error: t('workflow.editor.yaml.params') }
  }
  const triggers = doc.triggers ?? {}
  if (!isPlainObject(triggers)) {
    return { ok: false, error: t('workflow.editor.yaml.triggers') }
  }

  const spec = doc.spec
  if (!isPlainObject(spec)) {
    return { ok: false, error: t('workflow.editor.yaml.spec') }
  }
  const nodesRaw = spec.nodes ?? []
  const edgesRaw = spec.edges ?? []
  if (!Array.isArray(nodesRaw) || !Array.isArray(edgesRaw)) {
    return { ok: false, error: t('workflow.editor.yaml.spec') }
  }

  const nodes: WorkflowNode[] = []
  const ids = new Set<string>()
  for (const [index, entry] of nodesRaw.entries()) {
    if (!isPlainObject(entry)) {
      return {
        ok: false,
        error: t('workflow.editor.yaml.nodeInvalid', { index }),
      }
    }
    const id = entry.id
    if (typeof id !== 'string' || id === '') {
      return {
        ok: false,
        error: t('workflow.editor.yaml.nodeIdRequired', { index }),
      }
    }
    const type = entry.type
    if (
      typeof type !== 'string' ||
      !WORKFLOW_NODE_TYPES.includes(type as WorkflowNodeType)
    ) {
      return {
        ok: false,
        error: t('workflow.editor.yaml.nodeTypeUnknown', {
          id,
          type: String(type),
          allowed: WORKFLOW_NODE_TYPES.join(', '),
        }),
      }
    }
    const label = entry.name ?? ''
    const onFailure = entry.onFailure
    const timeout = entry.timeout
    const props = entry.props
    const invalidField = (field: string): WorkflowYamlParseResult => ({
      ok: false,
      error: t('workflow.editor.yaml.nodeFieldInvalid', { id, field }),
    })
    if (typeof label !== 'string') return invalidField('name')
    if (
      onFailure !== undefined &&
      onFailure !== 'stop' &&
      onFailure !== 'continue'
    ) {
      return invalidField('onFailure')
    }
    if (timeout !== undefined && typeof timeout !== 'string') {
      return invalidField('timeout')
    }
    if (props !== undefined && !isPlainObject(props)) {
      return invalidField('props')
    }
    if (!isValidNodeNodeId(id)) {
      return { ok: false, error: t('workflow.editor.nodeIdInvalid', { id }) }
    }
    if (ids.has(id)) {
      return { ok: false, error: t('workflow.editor.nodeIdDuplicate', { id }) }
    }
    ids.add(id)
    nodes.push({
      id,
      type: type as WorkflowNodeType,
      name: label,
      ...(onFailure !== undefined
        ? { onFailure: onFailure as 'stop' | 'continue' }
        : {}),
      ...(timeout !== undefined ? { timeout: timeout as string } : {}),
      ...(props !== undefined
        ? { props: props as Record<string, unknown> }
        : {}),
    })
  }

  const edges: WorkflowEdge[] = []
  for (const [index, entry] of edgesRaw.entries()) {
    const from = isPlainObject(entry) ? entry.from : undefined
    const to = isPlainObject(entry) ? entry.to : undefined
    if (
      typeof from !== 'string' ||
      typeof to !== 'string' ||
      !ids.has(from) ||
      !ids.has(to)
    ) {
      return {
        ok: false,
        error: t('workflow.editor.yaml.edgeInvalid', { index }),
      }
    }
    edges.push({ from, to })
  }

  const specOut: DAGSpec = { nodes, edges }
  const cycle = findCycle(specOut)
  if (cycle) {
    return {
      ok: false,
      error: t('workflow.editor.cycleError', { path: cycle.join(' -> ') }),
    }
  }

  return {
    ok: true,
    envelope: {
      apiVersion: 'kite.sh/v1',
      kind: 'Workflow',
      metadata: { name, description },
      spaces: spaces as string[],
      params: params as ParamSpec[],
      triggers: triggers as TriggerConfig,
      spec: specOut,
    },
  }
}
