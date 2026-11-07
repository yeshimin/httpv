export type TrafficRiskLevel = 'unknown' | 'normal' | 'trusted' | 'suspicious' | 'dangerous'

export type TrafficRecord = {
  requestId: string
  subjectId: string
  method: string
  path: string
  clientIp: string
  startedAt: number
  pendingAt?: number
  releasedAt?: number
  blockedAt?: number
  abortedAt?: number
  responseStartedAt?: number
  finishedAt?: number
  status?: number
  action?: string
  durationMs?: number
  requestBytes?: number
  responseBytes?: number
  riskLevel?: TrafficRiskLevel
  riskScore?: number
}

export type TrafficEventMessage = {
  phase: string
  request_id: string
  subject_id: string
  timestamp_ms: number
  method?: string
  path?: string
  client_ip?: string
  status?: number
  duration_ms?: number
  request_bytes?: number
  response_bytes?: number
  action?: string
  risk_level?: TrafficRiskLevel
  risk_score?: number
}

export function applyTrafficEvent(event: TrafficEventMessage, target: Map<string, TrafficRecord>, maximumRecords = 5_000) {
  const existing = target.get(event.request_id)
  const record: TrafficRecord = existing ?? {
    requestId: event.request_id,
    subjectId: event.subject_id,
    method: event.method ?? 'GET',
    path: event.path ?? '/',
    clientIp: event.client_ip ?? '',
    startedAt: event.timestamp_ms,
    riskLevel: event.risk_level,
    riskScore: event.risk_score
  }
  record.method = event.method ?? record.method
  record.path = event.path ?? record.path
  record.clientIp = event.client_ip ?? record.clientIp
  record.requestBytes = event.request_bytes ?? record.requestBytes
  record.riskLevel = event.risk_level ?? record.riskLevel
  record.riskScore = event.risk_score ?? record.riskScore
  if (event.phase === 'request_started') record.startedAt = event.timestamp_ms
  if (event.phase === 'request_pending') record.pendingAt = event.timestamp_ms
  if (event.phase === 'response_started') record.responseStartedAt = event.timestamp_ms
  if (event.phase === 'response_finished') {
    record.finishedAt = event.timestamp_ms
    record.status = event.status
    record.durationMs = event.duration_ms
    record.responseBytes = event.response_bytes
  }
  if (event.phase === 'request_blocked' || event.phase === 'request_timeout' || event.phase === 'request_decided' || event.phase === 'request_overflow') {
    record.action = event.action
    if (event.status) record.status = event.status
  }
  if ((event.phase === 'request_decided' || event.phase === 'request_timeout' || event.phase === 'request_overflow') && event.action === 'allow') {
    record.releasedAt = event.timestamp_ms
  }
  if (event.phase === 'request_aborted') {
    record.action = 'abort'
    record.abortedAt = event.timestamp_ms
    record.status = event.status ?? 499
    record.durationMs = event.duration_ms
  }
  if ((event.phase === 'request_decided' || event.phase === 'request_blocked' || event.phase === 'request_timeout' || event.phase === 'request_overflow') && (event.action === 'block' || event.action === 'reject')) {
    record.blockedAt = event.timestamp_ms
  }
  target.set(record.requestId, { ...record })
  if (target.size > maximumRecords) target.delete(target.keys().next().value as string)
}

export function terminalTimestamp(record: TrafficRecord) {
  return record.finishedAt ?? record.blockedAt ?? record.abortedAt
}

export function visualExpiry(record: TrafficRecord, renderDelayMs: number, trailMs: number, selected = false, selectedRetentionMs = 60_000) {
  const terminalAt = terminalTimestamp(record)
  if (terminalAt === undefined) return undefined
  const retention = selected ? selectedRetentionMs : Math.max(1_000, trailMs)
  return terminalAt + renderDelayMs + 420 + retention
}

export function reconcileAnimationAdmissions(records: TrafficRecord[], admissions: Set<string>, seen: Set<string>, limit: number) {
  const currentIDs = new Set(records.map((record) => record.requestId))
  for (const id of admissions) if (!currentIDs.has(id)) admissions.delete(id)
  for (const id of seen) if (!currentIDs.has(id)) seen.delete(id)

  const maximum = limit === 0 ? Number.POSITIVE_INFINITY : Math.max(1, Math.min(5_000, Math.floor(limit)))
  for (const record of records) {
    if (seen.has(record.requestId)) continue
    seen.add(record.requestId)
    if (admissions.size < maximum) admissions.add(record.requestId)
  }
  return [...admissions]
}
