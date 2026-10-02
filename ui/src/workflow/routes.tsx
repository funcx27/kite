// Workflow module routes (design §11): list, create/edit editor, per-workflow
// run list and run view. Mounted by the host ui/src/routes.tsx ahead of the
// :resource wildcards.
import type { RouteObject } from 'react-router-dom'

import { WorkflowEditorPage } from './pages/workflow-editor-page'
import { WorkflowListPage } from './pages/workflow-list-page'
import { WorkflowRunPage } from './pages/workflow-run-page'
import { WorkflowRunsPage } from './pages/workflow-runs-page'

import './i18n'

export const workflowRoutes: RouteObject[] = [
  {
    path: 'workflows',
    element: <WorkflowListPage />,
  },
  {
    // Static segment must win over 'workflows/:name'; react-router ranks it
    // higher, but keep the declaration order explicit as well.
    path: 'workflows/new',
    element: <WorkflowEditorPage />,
  },
  {
    path: 'workflows/:name',
    element: <WorkflowEditorPage />,
  },
  {
    path: 'workflows/:name/runs',
    element: <WorkflowRunsPage />,
  },
  {
    path: 'workflows/:name/runs/:runId',
    element: <WorkflowRunPage />,
  },
]
