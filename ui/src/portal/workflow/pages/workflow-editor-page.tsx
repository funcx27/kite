// Workflow editor (design §11 视图②): React Flow canvas with a left node
// palette (four built-in types, drag onto canvas), a right property drawer
// per spec.go props fields, params/triggers dialogs, client-side cycle check
// and save via POST/PUT (a save on an existing workflow appends a revision).
// /workflows/new renders the create variant; /workflows/:id the editor — the
// workflow identity is the numeric ID, names are reusable labels.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useApplications } from '@/portal/application/api'
import { downloadTextFile } from '@/portal/components/download'
import { useSpaces } from '@/portal/space-context'
import {
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type EdgeChange,
  type Edge as FlowEdge,
  type NodeChange,
} from '@xyflow/react'
import { FileCode, History, Network } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

import { usePageTitle } from '@/hooks/use-page-title'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import {
  useDefaultRegistry,
  useSaveWorkflowMutation,
  useWorkflow,
  WorkflowApiError,
} from '../api'
import { ParamsDialog, TriggersDialog } from '../components/definition-dialogs'
import { FlowCanvas } from '../components/flow'
import { PropertyDrawer } from '../components/property-drawer'
import { WebhookTokenDialog } from '../components/run-dialogs'
import { WorkflowYamlPane } from '../components/workflow-yaml-pane'
import {
  flowToSpecNode,
  WORKFLOW_NODE_CARD,
  WORKFLOW_NODE_MIME,
  type WorkflowFlowNode,
} from '../flow-model'
import {
  autoLayout,
  findCycle,
  isValidNodeNodeId,
  nodeLayout,
  nodeTypeAccent,
  WORKFLOW_NODE_TYPES,
} from '../lib'
import type {
  DAGSpec,
  ParamSpec,
  TriggerConfig,
  Workflow,
  WorkflowNode,
  WorkflowNodeType,
} from '../types'
import {
  draftToWorkflowYaml,
  emptyWorkflowYaml,
  parseWorkflowYaml,
  readEnvelopeName,
  workflowYamlFilename,
  type WorkflowYamlEnvelope,
} from '../yaml'

// The auto-naming rule expressed as an editable template: prefilled on new
// build nodes (and the application-entry pipeline) so the default image name
// is visible and adjustable; the ctx globals resolve at run time.
const DEFAULT_BUILD_OUTPUT =
  '{{ctx.registry}}/{{ctx.space}}/{{ctx.app}}:{{git.shortCommit}}-{{build.fingerprint}}'

function nextNodeId(type: WorkflowNodeType, taken: Set<string>): string {
  const prefix = type.replace(/-/g, '')
  for (let i = 1; i < 10000; i++) {
    const candidate = `${prefix}${i}`
    if (!taken.has(candidate)) return candidate
  }
  return `${prefix}-${Date.now()}`
}

// Route/query ids address workflows numerically; anything that is not a
// positive integer is invalid (mirrors the backend's findWorkflow 400).
function parseIdParam(value: string | null): number | undefined {
  if (value === null || value === '') return undefined
  const parsed = Number(value)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

// Flow nodes/edges from a spec: legacy props.layout positions win, missing
// ones fall back to the deterministic auto-layout.
function specToFlow(spec: DAGSpec): {
  nodes: WorkflowFlowNode[]
  edges: FlowEdge[]
} {
  const positions = new Map<string, { x: number; y: number }>()
  for (const n of spec.nodes) {
    const layout = nodeLayout(n.props)
    if (layout) positions.set(n.id, layout)
  }
  if (positions.size < spec.nodes.length) {
    for (const [id, p] of autoLayout(spec)) {
      if (!positions.has(id)) positions.set(id, p)
    }
  }
  return {
    nodes: spec.nodes.map((n) => ({
      id: n.id,
      type: WORKFLOW_NODE_CARD,
      position: positions.get(n.id) ?? { x: 40, y: 40 },
      data: {
        nodeType: n.type,
        label: n.name,
        onFailure: n.onFailure,
        timeout: n.timeout,
        props: n.props,
      },
    })),
    edges: spec.edges.map((e) => ({
      id: `${e.from}->${e.to}`,
      source: e.from,
      target: e.to,
    })),
  }
}

// Spec cleanup shared by the canvas and YAML paths: strip the legacy
// props.layout field — the engine's decodeProps rejects unknown props
// fields, so any extra props key would fail the node at run time — and drop
// now-empty props maps.
function sanitizeSpec(spec: DAGSpec): DAGSpec {
  const nodes = spec.nodes.map((n) => {
    const { layout: _legacy, ...props } = n.props ?? {}
    void _legacy
    return { ...n, props: Object.keys(props).length > 0 ? props : undefined }
  })
  return { nodes, edges: spec.edges }
}

// The engine substitutes {{...}} templates only, so a $ref outside of those
// braces would stay literal text at run time. Scan the strings of the spec
// about to be saved ({{...}} content excluded) and report the suspects; the
// save itself is not blocked.
function findDollarTemplates(spec: DAGSpec): string[] {
  const found: string[] = []
  const walk = (value: unknown) => {
    if (typeof value === 'string') {
      const withoutTemplates = value.replace(/\{\{[^{}]*\}\}/g, '')
      for (const match of withoutTemplates.matchAll(/\$\s*[A-Za-z_][\w.]*/g)) {
        found.push(match[0])
      }
    } else if (Array.isArray(value)) {
      value.forEach(walk)
    } else if (value && typeof value === 'object') {
      Object.values(value).forEach(walk)
    }
  }
  walk(spec)
  return found
}

export function WorkflowEditorPage() {
  const { id: routeId } = useParams()
  const isCreate = routeId === undefined
  const routeWorkflowId = isCreate ? undefined : parseIdParam(routeId ?? null)
  const navigate = useNavigate()
  const { t } = useTranslation()
  usePageTitle(
    isCreate ? t('workflow.editor.createTitle') : t('workflow.editor.editTitle')
  )

  const {
    data: workflow,
    isLoading,
    error,
  } = useWorkflow(isCreate ? undefined : routeWorkflowId)
  // ?copy=<id> (create route only): the source workflow to prefill a draft
  // from. ?fromApp=<name>&fromSpace=<space>&cluster=<cluster> is the
  // application-detail entry: copy wins when both are present.
  const [searchParams] = useSearchParams()
  const copySourceId = isCreate
    ? parseIdParam(searchParams.get('copy'))
    : undefined
  const fromApp = isCreate ? searchParams.get('fromApp') : null
  const fromSpace = isCreate ? searchParams.get('fromSpace') : null
  const fromCluster = isCreate ? searchParams.get('cluster') : null
  // ?returnTo=<app route> (application entry, create and edit): the toolbar
  // gains a back control and a successful save bounces back to the app.
  // '/'-rooted only — a //host value would be a protocol-relative redirect.
  const returnToRaw = searchParams.get('returnTo')
  const returnTo =
    returnToRaw?.startsWith('/') && !returnToRaw.startsWith('//')
      ? returnToRaw
      : null
  // Runs-list route for this workflow; returnTo rides along so the runs
  // pages (same parsing and same-origin guard) keep their back control.
  const runsPath = (id: number) =>
    returnTo
      ? `/workflows/${id}/runs?returnTo=${encodeURIComponent(returnTo)}`
      : `/workflows/${id}/runs`
  const { data: copyWorkflow } = useWorkflow(copySourceId)
  const { data: applications } = useApplications()
  const { data: defaultRegistry, isError: defaultRegistryFailed } =
    useDefaultRegistry()
  const { spaces, currentSpace, setCurrentSpace } = useSpaces()
  const saveMutation = useSaveWorkflowMutation()

  // The default registry's push/pull credential, prefilled into new build and
  // deploy nodes (and the application-entry pipeline) so the created specs
  // name it explicitly instead of relying on the runtime fallback.
  const defaultRegistryCredential =
    defaultRegistry?.valid && defaultRegistry.credential !== ''
      ? defaultRegistry.credential
      : undefined

  const [draftName, setDraftName] = useState('')
  const [draftDescription, setDraftDescription] = useState('')
  const [boundSpaces, setBoundSpaces] = useState<string[]>([])
  // The workflow's space follows the header switcher, which is locked to the
  // draft's binding while editing (space-picker disables itself on editor
  // routes): edit/copy sync it below, so saving can never silently rebind
  // the workflow to whatever space the list happened to be under. A blank
  // create keeps the entry context — creating under "all spaces" is the
  // platform-workflow entry point. The backend still accepts multiple.
  const effectiveSpaces = currentSpace ? [currentSpace] : boundSpaces
  const [params, setParams] = useState<ParamSpec[]>([])
  const [triggers, setTriggers] = useState<TriggerConfig>({})
  const [flowNodes, setFlowNodes] = useState<WorkflowFlowNode[]>([])
  const [flowEdges, setFlowEdges] = useState<FlowEdge[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [expectedRev, setExpectedRev] = useState(0)
  const [hydrated, setHydrated] = useState(false)
  const [paramsOpen, setParamsOpen] = useState(false)
  const [triggersOpen, setTriggersOpen] = useState(false)
  const [tokenOpen, setTokenOpen] = useState(false)
  // ?yaml=1 (create route only, the list page's import entry): start
  // directly in YAML mode on the empty skeleton.
  const yamlStart = isCreate && searchParams.get('yaml') === '1'
  const [mode, setMode] = useState<'canvas' | 'yaml'>(
    yamlStart ? 'yaml' : 'canvas'
  )
  const [yamlText, setYamlText] = useState(() =>
    yamlStart ? emptyWorkflowYaml() : ''
  )

  // ':id' is one route object, so parameter-to-parameter navigation (e.g.
  // history from /workflows/1 to /workflows/2) reuses this component; drop
  // the whole draft whenever the target workflow changes. edit/copy/fromApp
  // prefill below wins. Comparing the previous routeId (not a one-shot
  // boolean) keeps the initial mount inert under React StrictMode's dev
  // double effect run, so ?yaml=1 keeps its starting YAML mode and skeleton.
  const prevRouteId = useRef<string | undefined>(routeId)
  useEffect(() => {
    if (prevRouteId.current === routeId) return
    prevRouteId.current = routeId
    setHydrated(false)
    setSelectedId(null)
    setDraftName('')
    setDraftDescription('')
    setBoundSpaces([])
    setParams([])
    setTriggers({})
    setFlowNodes([])
    setFlowEdges([])
    setExpectedRev(0)
    setMode('canvas')
    setYamlText('')
  }, [routeId])

  // Load the definition once; edits live in local flow state afterwards.
  useEffect(() => {
    if (isCreate || !workflow || hydrated) return
    const { nodes, edges } = specToFlow(
      workflow.spec ?? { nodes: [], edges: [] }
    )
    setFlowNodes(nodes)
    setFlowEdges(edges)
    setDraftName(workflow.name)
    setDraftDescription(workflow.description)
    setBoundSpaces(workflow.spaces ?? [])
    setParams(workflow.params ?? [])
    setTriggers(workflow.triggers ?? {})
    setExpectedRev(workflow.currentRev)
    setHydrated(true)
  }, [isCreate, workflow, hydrated])

  // Lock the header switcher's space to the draft's binding: save binds the
  // workflow to the active space, so entering from the all-spaces view (or a
  // different space) would otherwise rebind it on the next save. Edit uses
  // the loaded workflow's first space (null = platform), copy the source's,
  // fromApp the application's space; a blank create keeps the entry context.
  // A binding to a space that no longer exists is left alone — the provider
  // would keep resetting it to "all spaces" and this effect would keep
  // writing it back.
  useEffect(() => {
    if (isCreate && copySourceId === undefined && !fromApp) return
    const source = isCreate ? copyWorkflow : workflow
    if (!source) return
    const target =
      isCreate && fromApp && copySourceId === undefined
        ? fromSpace
        : (source.spaces?.[0] ?? null)
    if (target !== null && spaces.length > 0 && !spaces.includes(target)) {
      return
    }
    if (currentSpace !== target) setCurrentSpace(target)
  }, [
    isCreate,
    copySourceId,
    copyWorkflow,
    workflow,
    fromApp,
    fromSpace,
    currentSpace,
    setCurrentSpace,
    spaces,
  ])

  // Prefill the create draft from ?copy=<id>, once per source. The result
  // stays an unsaved draft (expectedRev 0) until the user renames and saves.
  const copiedFrom = useRef<number | null>(null)
  useEffect(() => {
    if (!isCreate || copySourceId === undefined || !copyWorkflow) return
    if (copiedFrom.current === copySourceId) return
    copiedFrom.current = copySourceId
    const { nodes, edges } = specToFlow(
      copyWorkflow.spec ?? { nodes: [], edges: [] }
    )
    setFlowNodes(nodes)
    setFlowEdges(edges)
    setDraftName(`${copyWorkflow.name}-copy`)
    setDraftDescription(copyWorkflow.description)
    setBoundSpaces(copyWorkflow.spaces ?? [])
    setParams(copyWorkflow.params ?? [])
    setTriggers(copyWorkflow.triggers ?? {})
  }, [isCreate, copySourceId, copyWorkflow])

  // Prefill from ?fromApp=<name>&fromSpace=<space>&cluster=<cluster> (copy
  // takes precedence): once the application list resolves the composite
  // target, stage the default build → deploy → notify pipeline locked onto
  // that app, with the params the node templates reference prefilled.
  const prefilledFrom = useRef<string | null>(null)
  useEffect(() => {
    if (!isCreate || !fromApp || !fromSpace || copySourceId !== undefined)
      return
    const target = `${fromSpace}/${fromApp}`
    if (prefilledFrom.current === target) return
    if (applications === undefined) return
    // The default-registry credential rides the staged props; wait for the
    // query to settle (error counts as settled-empty) so it is never missed.
    if (defaultRegistry === undefined && !defaultRegistryFailed) return
    const app = applications.find(
      (candidate) =>
        candidate.namespace === fromSpace && candidate.name === fromApp
    )
    if (!app) return
    prefilledFrom.current = target
    const credentialProps: Record<string, unknown> = defaultRegistryCredential
      ? { registryCredentialRef: defaultRegistryCredential }
      : {}
    const deployProps: Record<string, unknown> = {
      space: fromSpace,
      app: fromApp,
      // Empty image deploys the app's current config (deployRunner), so wire
      // the build node's output through to deploy the built artifact.
      image: '{{steps.build1.outputs.imageRef}}',
      ...credentialProps,
    }
    if (fromCluster) deployProps.cluster = fromCluster
    // Positions come from specToFlow's auto-layout fallback (no legacy
    // layout props on template nodes).
    const { nodes, edges } = specToFlow({
      nodes: [
        {
          id: 'build1',
          type: 'build-image',
          name: t('workflow.nodeTypes.build-image'),
          props: {
            git: { url: '{{params.gitRepo}}', ref: '{{params.gitBranch}}' },
            dockerfile: '{{params.dockerfile}}',
            outputRef: DEFAULT_BUILD_OUTPUT,
            ...credentialProps,
          },
        },
        {
          id: 'deploy1',
          type: 'deploy-app',
          name: t('workflow.nodeTypes.deploy-app'),
          props: deployProps,
        },
        {
          id: 'notify1',
          type: 'notify',
          name: t('workflow.nodeTypes.notify'),
          props: { url: '' },
        },
      ],
      edges: [
        { from: 'build1', to: 'deploy1' },
        { from: 'deploy1', to: 'notify1' },
      ],
    })
    setFlowNodes(nodes)
    setFlowEdges(edges)
    setParams([
      { name: 'gitRepo', type: 'string', required: false },
      { name: 'gitBranch', type: 'string', required: false, default: 'main' },
      {
        name: 'dockerfile',
        type: 'string',
        required: false,
        default: 'path:Dockerfile',
      },
    ])
    // Cluster in the default name keeps same-app pipelines across clusters
    // distinguishable in the list.
    setDraftName(
      fromCluster ? `${fromApp}-${fromCluster}-workflow` : `${fromApp}-workflow`
    )
    setBoundSpaces([fromSpace])
  }, [
    isCreate,
    fromApp,
    fromSpace,
    fromCluster,
    copySourceId,
    applications,
    defaultRegistry,
    defaultRegistryFailed,
    defaultRegistryCredential,
    t,
  ])

  // Session-only deploy-target lock for the application entry: the drawer
  // grays out the app/cluster picks on every deploy-app node; it is never
  // written into the spec.
  const lockedTarget =
    isCreate && fromApp && fromSpace && copySourceId === undefined
      ? { app: fromApp, cluster: fromCluster ?? undefined }
      : undefined

  // Why the create-page save button is disabled; validateDraft() reports
  // the same blocker so "validation passed" always means "savable". In YAML
  // mode the name lives in the document, so applying it validates the name
  // instead.
  const saveBlocker: 'name' | null = useMemo(() => {
    if (!isCreate) return null
    if (mode === 'yaml') return null
    if (draftName.trim() === '') return 'name'
    return null
  }, [isCreate, draftName, mode])

  const selectedNode = useMemo(
    () => flowNodes.find((n) => n.id === selectedId) ?? null,
    [flowNodes, selectedId]
  )

  const specFromFlow = useCallback((): DAGSpec => {
    // Canvas positions are deliberately NOT persisted; sanitizeSpec strips
    // any legacy layout field (see its comment).
    return sanitizeSpec({
      nodes: flowNodes.map(flowToSpecNode),
      edges: flowEdges.map((e) => ({ from: e.source, to: e.target })),
    })
  }, [flowNodes, flowEdges])

  // Mode-agnostic draft validation over the (already sanitized) spec, so
  // the canvas draft and an applied YAML document share one rule set.
  const validateDraft = useCallback(
    (name: string, spec: DAGSpec, spaces: string[]): string | null => {
      const trimmedName = name.trim()
      if (trimmedName === '') {
        return t('workflow.editor.nameRequired')
      }
      // 1–63 chars, no leading/trailing whitespace — same rule as the server;
      // any unicode (Chinese included) is allowed since the name is a
      // DB-only display label.
      if (trimmedName.length > 63 || trimmedName !== name) {
        return t('workflow.editor.nameInvalid')
      }
      if (spec.nodes.length === 0) {
        return t('workflow.editor.emptyGraph')
      }
      const ids = new Set<string>()
      for (const n of spec.nodes) {
        if (!isValidNodeNodeId(n.id)) {
          return t('workflow.editor.nodeIdInvalid', { id: n.id })
        }
        if (ids.has(n.id)) {
          return t('workflow.editor.nodeIdDuplicate', { id: n.id })
        }
        ids.add(n.id)
        if (n.name.trim() === '') {
          return t('workflow.editor.nodeNameEmpty', { id: n.id })
        }
      }
      // Platform (unbound) workflows do not support deploy-app nodes yet.
      if (
        spaces.length === 0 &&
        spec.nodes.some((n) => n.type === 'deploy-app')
      ) {
        return t('workflow.editor.deployNodeNeedsSpace')
      }
      const cycle = findCycle(spec)
      if (cycle) {
        return t('workflow.editor.cycleError', { path: cycle.join(' -> ') })
      }
      return null
    },
    [t]
  )

  // Why the toolbar export and the YAML download are disabled right now:
  // the save path's own validation, so a gated export always matches a
  // gated save. An empty draft fails naturally (empty name / no nodes) —
  // no special case. In YAML mode the parse must succeed first and its
  // error is the reason.
  const exportBlocker: string | null = useMemo(() => {
    if (mode === 'yaml') {
      const parsed = parseWorkflowYaml(yamlText, t)
      return parsed.ok
        ? validateDraft(
            parsed.envelope.metadata.name,
            sanitizeSpec(parsed.envelope.spec),
            currentSpace ? [currentSpace] : parsed.envelope.spaces
          )
        : parsed.error
    }
    return validateDraft(
      draftName,
      specFromFlow(),
      currentSpace ? [currentSpace] : boundSpaces
    )
  }, [
    mode,
    yamlText,
    t,
    validateDraft,
    currentSpace,
    boundSpaces,
    draftName,
    specFromFlow,
  ])

  // The current draft serialized as an envelope document (entering YAML
  // mode and canvas-mode export).
  const draftEnvelopeYaml = () =>
    draftToWorkflowYaml({
      name: draftName,
      description: draftDescription,
      spaces: effectiveSpaces,
      params,
      triggers,
      spec: specFromFlow(),
    })

  // Parse the YAML text and apply it onto the draft states — the same
  // hydration path as loading a workflow (specToFlow for the graph,
  // metadata/params/triggers/spaces for the rest). Returns the envelope, or
  // null after toasting the first problem; callers then stay in YAML mode.
  const applyYaml = (): WorkflowYamlEnvelope | null => {
    const parsed = parseWorkflowYaml(yamlText, t)
    if (!parsed.ok) {
      toast.error(`${t('workflow.editor.yamlApplyFailed')}: ${parsed.error}`)
      return null
    }
    const { envelope } = parsed
    const { nodes, edges } = specToFlow(envelope.spec)
    setFlowNodes(nodes)
    setFlowEdges(edges)
    setDraftName(envelope.metadata.name)
    setDraftDescription(envelope.metadata.description)
    setBoundSpaces(envelope.spaces)
    setParams(envelope.params)
    setTriggers(envelope.triggers)
    setSelectedId(null)
    return envelope
  }

  const switchMode = (target: 'canvas' | 'yaml') => {
    if (target === mode) return
    if (target === 'yaml') {
      setYamlText(draftEnvelopeYaml())
      setMode('yaml')
      return
    }
    if (applyYaml()) setMode('canvas')
  }

  // Download filename shared by the toolbar export and the pane's download
  // button; in YAML mode the document's metadata.name wins over the (stale)
  // header draft name. Resolved at click time only — parsing the whole
  // document per render just for a name would be waste. A failed parse
  // falls back to the draft name and then 'workflow'.
  const exportFilename = () =>
    workflowYamlFilename(
      mode === 'yaml' ? (readEnvelopeName(yamlText) ?? draftName) : draftName
    )

  const handleExport = () => {
    downloadTextFile(
      exportFilename(),
      mode === 'yaml' ? yamlText : draftEnvelopeYaml(),
      'text/yaml'
    )
  }

  const handleValidate = () => {
    if (exportBlocker) {
      toast.error(`${t('workflow.editor.validationFailed')}: ${exportBlocker}`)
      return
    }
    toast.success(t('workflow.editor.validationOk'))
  }

  const handleSave = () => {
    // In YAML mode the document is the draft: apply it first (a parse or
    // validation failure blocks the save), then save from the applied
    // envelope instead of the still-stale flow state.
    let name: string
    let description: string
    let spec: DAGSpec
    let saveParams: ParamSpec[]
    let saveTriggers: TriggerConfig
    let spaces: string[]
    if (mode === 'yaml') {
      const envelope = applyYaml()
      if (!envelope) return
      name = envelope.metadata.name
      description = envelope.metadata.description
      spec = sanitizeSpec(envelope.spec)
      saveParams = envelope.params
      saveTriggers = envelope.triggers
      spaces = currentSpace ? [currentSpace] : envelope.spaces
    } else {
      if (isCreate && draftName.trim() === '') return
      name = draftName
      description = draftDescription
      spec = specFromFlow()
      saveParams = params
      saveTriggers = triggers
      spaces = effectiveSpaces
    }
    const problem = validateDraft(name, spec, spaces)
    if (problem) {
      toast.error(`${t('workflow.editor.validationFailed')}: ${problem}`)
      return
    }
    const dollarRefs = findDollarTemplates(spec)
    if (dollarRefs.length > 0) {
      toast.warning(
        t('workflow.editor.dollarTemplateWarning', {
          found: dollarRefs[0],
          example: '{{params.name}}',
        })
      )
    }
    const onSuccess = (saved: Workflow) => {
      toast.success(t('workflow.editor.saved', { rev: saved.currentRev }))
      // Advisory save-time checks (e.g. an environmental git probe failure)
      // ride the saved definition back as warnings.
      saved.warnings?.forEach((warning) => toast.warning(warning))
      if (returnTo) {
        // Application entry: go straight back to the app; create replaces
        // the transient /workflows/new history entry, like the default path.
        navigate(returnTo, { replace: isCreate })
      } else if (isCreate) {
        navigate(`/workflows/${saved.id}`, { replace: true })
      } else {
        setExpectedRev(saved.currentRev)
      }
    }
    const onError = (err: Error) => {
      // 400 carries the joined server validation problems; 409 is the
      // expectedRev optimistic-lock mismatch (design §10).
      if (err instanceof WorkflowApiError && err.status === 409) {
        toast.error(t('workflow.editor.conflict'))
        return
      }
      toast.error(err.message)
    }
    if (isCreate) {
      saveMutation.mutate(
        {
          req: {
            name: name.trim(),
            description,
            spec,
            params: saveParams,
            triggers: saveTriggers,
            spaces,
          },
        },
        { onSuccess, onError }
      )
      return
    }
    saveMutation.mutate(
      {
        id: routeWorkflowId,
        req: {
          // Sent only when the name actually changed; absent keeps the
          // stored name untouched.
          ...(workflow && name.trim() !== workflow.name
            ? { name: name.trim() }
            : {}),
          description,
          spec,
          params: saveParams,
          triggers: saveTriggers,
          expectedRev,
          spaces,
        },
      },
      { onSuccess, onError }
    )
  }

  const onNodesChange = useCallback(
    (changes: NodeChange<WorkflowFlowNode>[]) =>
      setFlowNodes((prev) => applyNodeChanges(changes, prev)),
    []
  )

  const onEdgesChange = useCallback(
    (changes: EdgeChange<FlowEdge>[]) =>
      setFlowEdges((prev) => applyEdgeChanges(changes, prev)),
    []
  )

  const onConnect = useCallback((connection: Connection) => {
    if (!connection.source || !connection.target) return
    setFlowEdges((prev) => {
      // One dependency per node pair; duplicates carry no extra semantics.
      if (
        prev.some(
          (e) =>
            e.source === connection.source && e.target === connection.target
        )
      ) {
        return prev
      }
      return addEdge(
        { ...connection, id: `${connection.source}->${connection.target}` },
        prev
      )
    })
  }, [])

  const onDropNode = useCallback(
    (nodeType: WorkflowNodeType, position: { x: number; y: number }) => {
      setFlowNodes((prev) => [
        ...prev,
        {
          id: nextNodeId(nodeType, new Set(prev.map((n) => n.id))),
          type: WORKFLOW_NODE_CARD,
          position,
          data: {
            nodeType,
            label: t(`workflow.nodeTypes.${nodeType}`),
            // Build nodes start from the auto-naming rule as an editable
            // template (the ctx globals resolve at run time); build and
            // deploy nodes carry the default registry's credential — the
            // created specs name it instead of the runtime fallback.
            ...(nodeType === 'build-image' || nodeType === 'deploy-app'
              ? {
                  props: {
                    ...(defaultRegistryCredential
                      ? {
                          registryCredentialRef: defaultRegistryCredential,
                        }
                      : {}),
                    ...(nodeType === 'build-image'
                      ? { outputRef: DEFAULT_BUILD_OUTPUT }
                      : {}),
                  },
                }
              : {}),
          },
        },
      ])
    },
    [t, defaultRegistryCredential]
  )

  // Renaming a node id rewires its edges so template paths (steps.<id>.*)
  // stay valid.
  const updateSelectedNode = (updated: WorkflowNode) => {
    const previousId = selectedId
    setFlowNodes((prev) =>
      prev.map((n) =>
        n.id === previousId
          ? {
              ...n,
              id: updated.id,
              data: {
                ...n.data,
                label: updated.name,
                onFailure: updated.onFailure,
                timeout: updated.timeout,
                props: updated.props,
              },
            }
          : n
      )
    )
    if (previousId && updated.id !== previousId) {
      setFlowEdges((prev) =>
        prev.map((e) => ({
          id: `${e.source === previousId ? updated.id : e.source}->${e.target === previousId ? updated.id : e.target}`,
          source: e.source === previousId ? updated.id : e.source,
          target: e.target === previousId ? updated.id : e.target,
        }))
      )
      setSelectedId(updated.id)
    }
  }

  const deleteNode = (id: string) => {
    setFlowNodes((prev) => prev.filter((n) => n.id !== id))
    setFlowEdges((prev) =>
      prev.filter((e) => e.source !== id && e.target !== id)
    )
    if (selectedId === id) setSelectedId(null)
  }

  if (!isCreate && routeWorkflowId === undefined) {
    return (
      <div className="py-10 text-center text-sm text-destructive">
        Invalid workflow id
      </div>
    )
  }
  if (!isCreate && error) {
    return (
      <div className="py-10 text-center text-sm text-destructive">
        {(error as Error).message}
      </div>
    )
  }
  if (!isCreate && (isLoading || !hydrated)) {
    return (
      <div className="py-10 text-center text-sm text-muted-foreground">
        {t('common.messages.loading')}
      </div>
    )
  }

  const selectedSpecNode: WorkflowNode | null = selectedNode
    ? flowToSpecNode(selectedNode)
    : null

  return (
    <div className="flex h-[calc(100dvh-11rem)] flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {returnTo && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate(returnTo)}
          >
            ← {t('workflow.run.backToApp')}
          </Button>
        )}
        {mode === 'yaml' ? (
          // YAML mode: name/description (and params/triggers, whose toolbar
          // buttons hide below) are edited inside the document itself.
          <div className="flex min-w-0 flex-1 items-center gap-3">
            {!isCreate && routeWorkflowId !== undefined && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => navigate(runsPath(routeWorkflowId))}
              >
                <History className="h-4 w-4" />
                {t('workflow.editor.viewRuns')}
              </Button>
            )}
            {!isCreate && (
              <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
                rev {expectedRev}
              </span>
            )}
            <span className="truncate text-xs text-muted-foreground">
              {t('workflow.editor.yamlHint')}
            </span>
          </div>
        ) : isCreate ? (
          <div className="flex flex-1 flex-wrap items-center gap-2">
            <Input
              value={draftName}
              onChange={(e) => setDraftName(e.target.value)}
              placeholder={`${t('workflow.editor.name')} *`}
              aria-required
              className="w-48 font-mono"
            />
            <Input
              value={draftDescription}
              onChange={(e) => setDraftDescription(e.target.value)}
              placeholder={t('workflow.editor.description')}
              className="w-56"
            />
          </div>
        ) : (
          <div className="flex min-w-0 flex-1 flex-wrap items-center gap-3">
            <Input
              value={draftName}
              onChange={(e) => setDraftName(e.target.value)}
              aria-required
              placeholder={`${t('workflow.editor.name')} *`}
              className="w-48 font-mono text-sm font-semibold"
            />
            {routeWorkflowId !== undefined && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => navigate(runsPath(routeWorkflowId))}
              >
                <History className="h-4 w-4" />
                {t('workflow.editor.viewRuns')}
              </Button>
            )}
            <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
              rev {expectedRev}
            </span>
            <span className="hidden truncate text-xs text-muted-foreground md:inline">
              {flowNodes.length} {t('workflow.editor.nodes')}
            </span>
            <Input
              value={draftDescription}
              onChange={(e) => setDraftDescription(e.target.value)}
              placeholder={t('workflow.editor.description')}
              className="hidden w-56 lg:inline-flex"
            />
          </div>
        )}
        <div className="flex items-center gap-2">
          {mode === 'canvas' && (
            <>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setParamsOpen(true)}
              >
                {t('workflow.editor.params')}
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setTriggersOpen(true)}
              >
                {t('workflow.editor.triggers')}
              </Button>
            </>
          )}
          {/* Single switch-to-the-other-view button, styled like the rest of
              the toolbar; switchMode stays put when the YAML apply fails. */}
          <Button
            variant="outline"
            size="sm"
            onClick={() => switchMode(mode === 'canvas' ? 'yaml' : 'canvas')}
          >
            {mode === 'canvas' ? (
              <>
                <FileCode className="size-3.5" />
                {t('workflow.editor.modeYaml')}
              </>
            ) : (
              <>
                <Network className="size-3.5" />
                {t('workflow.editor.modeCanvas')}
              </>
            )}
          </Button>
          <Button variant="outline" size="sm" onClick={handleValidate}>
            ✓ {t('workflow.editor.validate')}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={exportBlocker !== null}
            title={exportBlocker ?? undefined}
            onClick={handleExport}
          >
            {t('workflow.editor.export')}
          </Button>
          <Button
            size="sm"
            disabled={saveMutation.isPending || saveBlocker !== null}
            title={
              saveBlocker === 'name'
                ? t('workflow.editor.nameRequired')
                : undefined
            }
            onClick={handleSave}
          >
            {saveMutation.isPending
              ? t('workflow.editor.saving')
              : t('workflow.editor.save')}
          </Button>
        </div>
      </div>

      <div className="flex min-h-0 flex-1 overflow-hidden rounded-lg border">
        {mode === 'yaml' ? (
          <WorkflowYamlPane
            value={yamlText}
            onChange={setYamlText}
            getFilename={exportFilename}
            blocker={exportBlocker}
          />
        ) : (
          <>
            <aside className="flex w-52 shrink-0 flex-col gap-2 border-r bg-muted/30 p-3">
              <div className="px-1 text-xs font-medium text-muted-foreground">
                {t('workflow.editor.nodes')} · {t('workflow.editor.nodesHint')}
              </div>
              {/* Platform (unbound) workflows cannot deploy apps yet: the
                  deploy-app node stays hidden until a space is bound. */}
              {WORKFLOW_NODE_TYPES.filter(
                (type) => effectiveSpaces.length > 0 || type !== 'deploy-app'
              ).map((type) => (
                <div
                  key={type}
                  draggable
                  onDragStart={(e) => {
                    e.dataTransfer.setData(WORKFLOW_NODE_MIME, type)
                    e.dataTransfer.effectAllowed = 'move'
                  }}
                  className="flex cursor-grab items-center gap-2.5 rounded-lg border bg-card p-2 text-sm shadow-sm transition-colors hover:bg-accent active:cursor-grabbing"
                >
                  <span
                    className={`h-6 w-6 shrink-0 rounded-md ${nodeTypeAccent[type]}`}
                  />
                  {t(`workflow.nodeTypes.${type}`)}
                </div>
              ))}
              <div className="mt-2 rounded-lg bg-card p-2.5 text-[11px] leading-4 text-muted-foreground">
                {effectiveSpaces.length === 0
                  ? t('workflow.editor.palettePlatformHint')
                  : t('workflow.editor.paletteHint')}
              </div>
            </aside>

            <div className="min-w-0 flex-1">
              <FlowCanvas
                nodes={flowNodes}
                edges={flowEdges}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onConnect={onConnect}
                onNodesDelete={(nodes) => {
                  for (const n of nodes) deleteNode(n.id)
                }}
                onSelectNode={setSelectedId}
                onDropNode={onDropNode}
              />
            </div>

            {/* Drawer only exists while a node is selected: the canvas takes
                the freed width back, and onPaneClick already clears the
                selection. */}
            {selectedNode && (
              <aside className="w-80 shrink-0 border-l bg-card">
                <PropertyDrawer
                  node={selectedSpecNode}
                  params={params}
                  nodes={flowNodes.map(flowToSpecNode)}
                  edges={flowEdges.map((e) => ({
                    from: e.source,
                    to: e.target,
                  }))}
                  lockedTarget={lockedTarget}
                  workflowName={draftName}
                  onChange={updateSelectedNode}
                  onDelete={() => selectedNode && deleteNode(selectedNode.id)}
                />
              </aside>
            )}
          </>
        )}
      </div>

      <ParamsDialog
        open={paramsOpen}
        onOpenChange={setParamsOpen}
        params={params}
        onSave={setParams}
      />
      <TriggersDialog
        open={triggersOpen}
        onOpenChange={setTriggersOpen}
        triggers={triggers}
        onSave={setTriggers}
        onManageToken={
          isCreate
            ? undefined
            : () => {
                setTriggersOpen(false)
                setTokenOpen(true)
              }
        }
      />
      <WebhookTokenDialog
        id={routeWorkflowId ?? 0}
        open={tokenOpen}
        onOpenChange={setTokenOpen}
      />
    </div>
  )
}
