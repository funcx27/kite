// Types mirroring pkg/buildfleetadapter/adapter.go (SSE events + status JSON).

export type BuildVertexStatus =
  'pending' | 'running' | 'complete' | 'cached' | 'failed'

export interface BuildRuntimeEvent {
  runtimeId: string
  provider: string
  addr: string
  attempt: number
}

export interface BuildVertexEvent {
  digest: string
  name: string
  status: BuildVertexStatus
}

export interface BuildLogEvent {
  vertex: string
  data: string
}

// Terminal success event; ends the stream.
export interface BuildResultEvent {
  digest: string
  imageRef: string
  attempts: number
  ref: string
  commit: string
  runtimeId: string
  provider: string
}

// Terminal failure event; ends the stream.
export interface BuildErrorEvent {
  error: string
}

export interface BuildGitSource {
  url: string
  ref?: string
  subdir?: string
  mode?: string
}

export interface BuildHTTPSource {
  url: string
  mode?: string
}

// POST /buildfleet/builds body.
export interface BuildRequest {
  git?: BuildGitSource
  http?: BuildHTTPSource
  contextDigest?: string
  dockerfile?: string
  dockerfileContent?: string
  args?: Record<string, string>
  platforms?: string[]
  outputRef?: string
  provider?: string
}

export interface BuildRuntimeStatus {
  id: string
  state: string
  addr: string
  provider: string
  cpu: number
  memoryBytes: number
  idleSeconds: number
}

export interface BuildTaskStatus {
  runtimeId: string
  source: string
  outputRef: string
  startedUnix: number
  attempt: number
}

// GET /buildfleet/status response.
export interface BuildStatus {
  runtimes: BuildRuntimeStatus[]
  tasks: BuildTaskStatus[]
}

export interface CacheProjectStat {
  provider: string
  project: string
  tags: number
  layers: number
  exclusiveBytes: number
  sharedBytes: number
}

// GET /buildfleet/cachestat response.
export interface CacheStat {
  totalBytes: number
  projects: CacheProjectStat[]
}
