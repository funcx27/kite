// React Flow canvas shared by the editor (editable) and the run view (read
// only, status colored). The custom node card renders the four built-in
// types with type accent + status dot.
import { memo, useCallback } from 'react'
import {
  Background,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Edge,
  type EdgeChange,
  type NodeProps,
  type NodeTypes,
  type OnConnect,
  type OnNodesChange,
} from '@xyflow/react'

import '@xyflow/react/dist/style.css'

import { Bell, Rocket, ShieldCheck, Wrench } from 'lucide-react'
import { useTranslation } from 'react-i18next'

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
        className={`w-44 rounded-lg border bg-card px-3 py-2.5 shadow-sm transition-shadow hover:shadow-md ${
          selected ? 'border-primary ring-2 ring-primary/30' : 'border-border'
        }`}
      >
        <Handle
          type="target"
          position={Position.Left}
          className="!h-2.5 !w-2.5"
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
          className="!h-2.5 !w-2.5"
        />
      </div>
    )
  }
)

WorkflowNodeCard.displayName = 'WorkflowNodeCard'

const nodeTypes: NodeTypes = {
  [WORKFLOW_NODE_CARD]: WorkflowNodeCard,
}

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
      nodes={nodes}
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
      deleteKeyCode={readOnly ? null : ['Backspace', 'Delete']}
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
