// React Flow canvas shared by the editor (editable) and the run view (read
// only, status colored). The custom node card renders the four built-in
// types with type accent + status dot; the editor's default edge adds a
// discoverable delete button on selection.
import { memo, useCallback, useMemo } from 'react'
import {
  Background,
  BaseEdge,
  Controls,
  EdgeLabelRenderer,
  getBezierPath,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type EdgeChange,
  type EdgeProps,
  type EdgeTypes,
  type NodeProps,
  type NodeTypes,
  type OnConnect,
  type OnNodesChange,
} from '@xyflow/react'

import '@xyflow/react/dist/style.css'

import { Bell, Rocket, ShieldCheck, Wrench } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { useAppearance } from '@/components/appearance-provider'

import {
  WORKFLOW_NODE_CARD,
  WORKFLOW_NODE_MIME,
  type WorkflowFlowNode,
  type WorkflowNodeData,
} from '../flow-model'
import { nodeStatusColor, nodeTypeAccent } from '../lib'
import type { WorkflowNodeType } from '../types'

const typeIcons: Record<WorkflowNodeType, typeof Wrench> = {
  'build-image': Wrench,
  'deploy-app': Rocket,
  approval: ShieldCheck,
  notify: Bell,
}

// v12 derives edge endpoints from the measured Handle elements, so handles
// must stay mounted in the read-only view — not rendering them (or hiding
// via display:none) drops every connected edge. It also anchors each edge
// at the handle's outer edge on the connection side, and edge strokes use
// butt caps (no backward extension), so every residual handle pixel shows
// as a gap between the line and the node border: the hidden handle must be
// a true zero box — no size classes (they are !important), no min-size
// (default CSS carries 5px), no border (1px borders still render on a
// zero-size box and shift the anchor off the border). Inline styles instead
// of Tailwind classes: they survive production builds unconditionally.
const hiddenHandleStyle: React.CSSProperties = {
  minWidth: 0,
  minHeight: 0,
  width: 0,
  height: 0,
  border: 'none',
  opacity: 0,
  pointerEvents: 'none',
}

// MiniMap paints SVG fills, so it needs concrete colors instead of the
// Tailwind bg classes nodeTypeAccent / nodeStatusColor return; this maps
// those classes to their default-palette hex values.
const miniMapColorByClass: Record<string, string> = {
  'bg-zinc-900': '#18181b',
  'bg-zinc-300': '#d4d4d8',
  'bg-zinc-400': '#a1a1aa',
  'bg-zinc-500': '#71717a',
  'bg-emerald-600': '#059669',
  'bg-amber-500': '#f59e0b',
  'bg-sky-600': '#0284c7',
  'bg-green-500': '#22c55e',
  'bg-blue-500': '#3b82f6',
  'bg-red-500': '#ef4444',
  'bg-orange-500': '#f97316',
}

function miniMapColor(cssClass: string, fallback: string): string {
  return miniMapColorByClass[cssClass] ?? fallback
}

// Editor nodes use the node-type accent; run-view nodes (data.status set)
// use the status color.
function miniMapNodeColor(node: { data: Record<string, unknown> }): string {
  const data = node.data as WorkflowNodeData
  return data.status
    ? miniMapColor(nodeStatusColor(data.status, data.nodeType), '#d4d4d8')
    : miniMapColor(nodeTypeAccent[data.nodeType] ?? '', '#18181b')
}

const WorkflowNodeCard = memo(
  ({ data, selected }: NodeProps<WorkflowFlowNode>) => {
    const { t } = useTranslation()
    const Icon = typeIcons[data.nodeType] ?? Wrench
    return (
      <div
        className={`relative w-44 rounded-lg border bg-card px-3 py-2.5 shadow-sm transition-shadow hover:shadow-md ${
          selected ? 'border-primary ring-2 ring-primary/30' : 'border-border'
        }`}
      >
        <Handle
          type="target"
          position={Position.Left}
          // The hollow-dot sizing classes carry !important, which would
          // override the hidden style's inline width/height — hidden handles
          // go classless instead.
          className={
            data.hideHandles
              ? undefined
              : '!h-2.5 !w-2.5 !border-2 !border-primary/60 !bg-background'
          }
          style={data.hideHandles ? hiddenHandleStyle : undefined}
        />
        <div className="flex items-center gap-2">
          <span
            className={`flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-white ${nodeTypeAccent[data.nodeType] ?? 'bg-zinc-900'}`}
          >
            <Icon className="h-3.5 w-3.5" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">{data.label}</div>
            <div className="truncate text-xs text-muted-foreground">
              {t(`workflow.nodeTypes.${data.nodeType}`)}
            </div>
          </div>
          {data.status && (
            <span
              className={`inline-block h-2.5 w-2.5 shrink-0 rounded-full ${nodeStatusColor(data.status, data.nodeType)} ${
                data.waitingApproval ? 'animate-pulse' : ''
              }`}
            />
          )}
        </div>
        <Handle
          type="source"
          position={Position.Right}
          className={
            data.hideHandles
              ? undefined
              : '!h-2.5 !w-2.5 !border-2 !border-primary/60 !bg-background'
          }
          style={data.hideHandles ? hiddenHandleStyle : undefined}
        />
      </div>
    )
  }
)

WorkflowNodeCard.displayName = 'WorkflowNodeCard'

const nodeTypes: NodeTypes = {
  [WORKFLOW_NODE_CARD]: WorkflowNodeCard,
}

// Editor's default edge: when selected, a small delete button appears at the
// midpoint — edge deletion must be discoverable, not keyboard-only. It
// deletes via useReactFlow().setEdges, which reaches the controlled parent
// state through the onEdgesChange handler.
function WorkflowEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  style,
  selected,
}: EdgeProps) {
  const { t } = useTranslation()
  const { setEdges } = useReactFlow()
  const [path, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })
  return (
    <>
      <BaseEdge path={path} markerEnd={markerEnd} style={style} />
      {selected && (
        <EdgeLabelRenderer>
          <button
            type="button"
            className="nodrag nopan absolute flex h-5 w-5 items-center justify-center rounded-full border bg-background text-xs leading-none text-muted-foreground shadow-sm transition-colors hover:bg-accent hover:text-foreground"
            style={{
              // The EdgeLabelRenderer overlay is pointer-events:none so
              // labels don't block the canvas; interactive children must
              // opt back in.
              pointerEvents: 'all',
              transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            }}
            aria-label={t('workflow.editor.deleteEdge')}
            title={t('workflow.editor.deleteEdge')}
            onClick={() =>
              setEdges((edges) => edges.filter((edge) => edge.id !== id))
            }
          >
            ×
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

// Only the editor canvas uses the delete-button edge; the read-only run view
// keeps the built-in default edge.
const editorEdgeTypes: EdgeTypes = { default: WorkflowEdge }

interface FlowCanvasProps {
  nodes: WorkflowFlowNode[]
  edges: Edge[]
  readOnly?: boolean
  onNodesChange?: OnNodesChange<WorkflowFlowNode>
  onEdgesChange?: (changes: EdgeChange<Edge>[]) => void
  onConnect?: OnConnect
  onNodesDelete?: (nodes: WorkflowFlowNode[]) => void
  onEdgesDelete?: (edges: Edge[]) => void
  onSelectNode?: (nodeId: string | null) => void
  onDropNode?: (
    nodeType: WorkflowNodeType,
    position: { x: number; y: number }
  ) => void
}

function CanvasInner({
  nodes,
  edges,
  readOnly = false,
  onNodesChange,
  onEdgesChange,
  onConnect,
  onNodesDelete,
  onEdgesDelete,
  onSelectNode,
  onDropNode,
}: FlowCanvasProps) {
  const { screenToFlowPosition } = useReactFlow()
  const { actualTheme } = useAppearance()

  // Read-only canvas: mark nodes so the card hides its connection handles
  // (invisible + non-interactive, still mounted as edge anchors).
  const viewNodes = useMemo(
    () =>
      readOnly
        ? nodes.map((n) => ({ ...n, data: { ...n.data, hideHandles: true } }))
        : nodes,
    [nodes, readOnly]
  )

  const onDragOver = useCallback(
    (event: React.DragEvent) => {
      if (!onDropNode) return
      event.preventDefault()
      event.dataTransfer.dropEffect = 'move'
    },
    [onDropNode]
  )

  const onDrop = useCallback(
    (event: React.DragEvent) => {
      if (!onDropNode) return
      event.preventDefault()
      const nodeType = event.dataTransfer.getData(WORKFLOW_NODE_MIME)
      if (!(Object.keys(typeIcons) as string[]).includes(nodeType)) {
        return
      }
      onDropNode(
        nodeType as WorkflowNodeType,
        screenToFlowPosition({ x: event.clientX, y: event.clientY })
      )
    },
    [onDropNode, screenToFlowPosition]
  )

  return (
    <ReactFlow
      nodes={viewNodes}
      edges={edges}
      nodeTypes={nodeTypes}
      onNodesChange={onNodesChange}
      onEdgesChange={onEdgesChange}
      onConnect={onConnect}
      onNodesDelete={onNodesDelete}
      onEdgesDelete={onEdgesDelete}
      onNodeClick={(_, node) => onSelectNode?.(node.id)}
      onPaneClick={() => onSelectNode?.(null)}
      onDragOver={onDragOver}
      onDrop={onDrop}
      nodesDraggable={!readOnly}
      nodesConnectable={!readOnly}
      elementsSelectable
      edgeTypes={readOnly ? undefined : editorEdgeTypes}
      // A self-connection is a length-1 cycle: reject it at drag time rather
      // than failing the save-time cycle check afterwards.
      isValidConnection={
        readOnly
          ? undefined
          : (connection) => connection.source !== connection.target
      }
      deleteKeyCode={readOnly ? null : ['Backspace', 'Delete']}
      colorMode={actualTheme === 'dark' ? 'dark' : 'light'}
      fitView
      proOptions={{ hideAttribution: true }}
      className="bg-muted/30"
    >
      <Background gap={16} />
      <Controls showInteractive={false} />
      <MiniMap
        pannable
        zoomable
        nodeColor={miniMapNodeColor}
        nodeStrokeColor="#a1a1aa"
        nodeBorderRadius={6}
      />
    </ReactFlow>
  )
}

// ReactFlow hooks (screenToFlowPosition) require a provider ancestor, so the
// exported component wraps the canvas itself.
export function FlowCanvas(props: FlowCanvasProps) {
  return (
    <ReactFlowProvider>
      <CanvasInner {...props} />
    </ReactFlowProvider>
  )
}
