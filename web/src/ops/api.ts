import type {
  OpsQueue, OpsFailedTask, OpsCollection, OpsExportChunk, OpsHealth,
} from './types'

const BASE = ''

async function get<T>(url: string): Promise<T> {
  const r = await fetch(`${BASE}${url}`)
  if (!r.ok) throw new Error(`${url}: ${r.status}`)
  return r.json()
}

export const fetchOpsQueues = () => get<OpsQueue[]>('/api/ops/queues')
export const fetchOpsFailed = (limit = 50) => get<OpsFailedTask[]>(`/api/ops/failed?limit=${limit}`)
export const fetchOpsCollection = () => get<OpsCollection>('/api/ops/collection')
export const fetchOpsExports = () => get<OpsExportChunk[]>('/api/ops/exports')
export const fetchOpsHealth = () => get<OpsHealth>('/api/health')