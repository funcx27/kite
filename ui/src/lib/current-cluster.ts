const CURRENT_CLUSTER_STORAGE_KEY = 'current-cluster'
const CURRENT_CLUSTER_HEADER_KEY = 'x-cluster-name'

export function getCurrentCluster() {
  return (
    sessionStorage.getItem(CURRENT_CLUSTER_STORAGE_KEY) ||
    localStorage.getItem(CURRENT_CLUSTER_STORAGE_KEY)
  )
}

export function setCurrentCluster(clusterName: string) {
  sessionStorage.setItem(CURRENT_CLUSTER_STORAGE_KEY, clusterName)
  localStorage.setItem(CURRENT_CLUSTER_STORAGE_KEY, clusterName)
}

export function clearCurrentCluster() {
  sessionStorage.removeItem(CURRENT_CLUSTER_STORAGE_KEY)
  localStorage.removeItem(CURRENT_CLUSTER_STORAGE_KEY)
}

export function appendCurrentClusterParam(
  params: URLSearchParams,
  currentCluster = getCurrentCluster()
) {
  if (currentCluster) {
    params.append(CURRENT_CLUSTER_HEADER_KEY, currentCluster)
  }
}

export function appendCurrentClusterHeader(headers: Record<string, string>) {
  // A caller-provided header wins: it addresses a specific cluster (e.g. an
  // application page whose cluster differs from the global switcher).
  if (headers[CURRENT_CLUSTER_HEADER_KEY]) {
    return
  }
  const currentCluster = getCurrentCluster()
  if (currentCluster) {
    headers[CURRENT_CLUSTER_HEADER_KEY] = encodeURIComponent(currentCluster)
  }
}

// Header targeting one specific cluster, overriding the global switcher for
// a single request (the backend prefers the header over the query param).
export function clusterHeader(cluster: string): Record<string, string> {
  return { [CURRENT_CLUSTER_HEADER_KEY]: encodeURIComponent(cluster) }
}

export function withCurrentClusterPath(
  path: string,
  currentCluster: string | null = getCurrentCluster()
) {
  const normalizedPath = path.startsWith('/') ? path : `/${path}`
  if (!currentCluster) {
    return normalizedPath
  }
  return `/_clusters/${encodeURIComponent(currentCluster)}${normalizedPath}`
}

export function getClusterScopedStorageKey(key: string) {
  const currentCluster = getCurrentCluster()
  return `${currentCluster || ''}${key}`
}
