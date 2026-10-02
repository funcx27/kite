// WorkflowNode ⇄ React Flow node model shared by the editor and the run view
// (kept out of flow.tsx so that file only exports components for fast
// refresh).
import type { Node } from '@xyflow/react'

import type { NodeRunStatus, WorkflowNode, WorkflowNodeType } from './types'

export const WORKFLOW_NODE_CARD = 'workflowNode'

export const WORKFLOW_NODE_MIME = 'application/kite-workflow-node'

export interface WorkflowNodeData extends Record<string, unknown> {
  nodeType: WorkflowNodeType
  label: string
  // Editor-only fields mirroring spec.Node; the run view leaves them unset.
  onFailure?: 'stop' | 'continue'
  timeout?: string
  props?: Record<string, unknown>
  // Run-view overlay: undefined in the editor.
  status?: NodeRunStatus
  waitingApproval?: boolean
}

export type WorkflowFlowNode = Node<WorkflowNodeData, typeof WORKFLOW_NODE_CARD>

// Flow node → spec node. Canvas positions are deliberately not persisted:
// the engine's decodeProps rejects unknown props fields, so layout always
// comes from the deterministic auto-layout.
export function flowToSpecNode(node: WorkflowFlowNode): WorkflowNode {
  return {
    id: node.id,
    type: node.data.nodeType,
    name: node.data.label,
    onFailure: node.data.onFailure,
    timeout: node.data.timeout,
    props: node.data.props,
  }
}
