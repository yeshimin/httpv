<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import TrafficCanvas, { type TrafficRecord } from './TrafficCanvas.vue'

type Match = { host: string; path_prefix: string; method: string }
type Gate = { enabled: boolean; timeout_ms: number; timeout_action: 'allow' | 'block'; max_pending: number; overflow_action: 'allow' | 'block' | 'reject'; failure_action: 'allow' | 'block' }
type CapturePolicy = { enabled: boolean; request_headers: boolean; response_headers: boolean; request_body: boolean; response_body: boolean; max_body_bytes: number; ttl_seconds: number; sample_rate: number; content_types: string[]; redact_headers: string[] }
type Subject = { id: string; name: string; enabled: boolean; display: boolean; match: Match; gate: Gate; capture: CapturePolicy }
type UpstreamTarget = { id: string; url: string; host_header: string; weight: number }
type Upstream = { id: string; name: string; url?: string; host_header?: string; targets: UpstreamTarget[]; max_failures: number; failure_timeout_ms: number }
type Route = { id: string; name: string; enabled: boolean; match: Match; upstream_id: string }
type Rule = { id: string; subject_id?: string; action: string; path_prefix: string; method: string; client_ip: string; created_at: number }
type EventMessage = {
  phase: string; request_id: string; subject_id: string; timestamp_ms: number; method?: string; path?: string; client_ip?: string;
  status?: number; duration_ms?: number; request_bytes?: number; response_bytes?: number; action?: string
}
type TrafficSummaryPath = { subject_id: string; method: string; path: string; count: number; blocked: number; pending: number }
type TrafficSummary = {
  type: 'traffic_summary'; window_start_ms: number; window_end_ms: number; total: number; started: number; responded: number;
  finished: number; pending: number; blocked: number; paths: TrafficSummaryPath[]
}
type NativePolicyStatus = { version: number; state: 'active' | 'pending' | 'unpublished' | 'disabled'; mode?: 'native' | 'lua_fallback' | 'disabled'; acks: { worker: string; version: number }[] }
type AuthStatus = { enabled: boolean; authenticated: boolean; role?: 'viewer' | 'operator' | 'admin'; csrf_token?: string }
type CapturedSide = { headers?: Record<string, unknown>; body?: string; truncated?: boolean; content_type?: string; body_unavailable?: string; status?: number }
type RequestDetails = { request_id: string; subject_id: string; captured_at: number; finished_at?: number; duration_ms?: number; request: CapturedSide; response: CapturedSide }
type ConfigRevisionSummary = { revision: number; created_at: number; source: string }

type Locale = 'zh' | 'en'
const messages: Record<Locale, Record<string, string>> = {
  zh: {
    commandSurface: '流量指挥台', connecting: '连接中', live: '实时连接', closed: '连接断开',
    tracked: '{count} 条已跟踪 · {pending} 条待决', gateOn: '人工闸门：开启', gateOff: '人工闸门：关闭',
    sendCurrent: '发送当前请求', guidedDemo: '发送引导演示', request: '请求', response: '响应',
    waiting: '等待人工决定', blocked: '已阻断', tapParticle: '点击流量粒子查看详情', liveState: '实时状态',
    gateActive: '人工闸门已开启。', gateInstruction: '下一个匹配请求将以黄色粒子暂停，直到你选择放行或阻断。',
    ready: '就绪——运行引导演示以生成一条可见请求。', waitingDecision: '正在等待你的决定——点击黄色粒子，然后选择放行或阻断。',
    flowing: '请求正在流向上游：{method} {path}', returning: '上游已响应——响应正在返回客户端。',
    completed: '已完成：{status}，耗时 {duration}ms。拖尾会短暂保留。', clientAborted: '客户端已断开，请求已经终止。', trafficControls: '流量控制',
    managedPath: '受监管路径', gateTimeout: '闸门超时', timeoutAction: '超时动作', block: '阻断', allow: '放行', maxPending: '最大待决量', overflowAction: '超限动作', failureAction: '异常动作', reject503: '拒绝 503', failOpen: '放行（Fail-open）', failClosed: '阻断（Fail-closed）',
    regulateTraffic: '监管匹配流量', showEvents: '展示事件', render: '绘制 {value}ms', trail: '拖尾 {value}ms',
    selectedRequest: '选中请求', client: '客户端', status: '状态', duration: '耗时', responseSize: '响应大小',
    inFlight: '处理中', unavailable: '不可用', allowPending: '放行待决请求', blockPending: '阻断待决请求',
    blockFuture: '阻断后续匹配流量', selectHint: '点击流量粒子即可查看元数据并执行控制操作。',
    activeRules: '动态规则', noRules: '暂无动态阻断规则。', remove: '移除', language: '语言',
    pendingRemaining: '待人工决定，剩余 {seconds} 秒', pendingInfinite: '待人工决定，等待人工操作', batchRemaining: '待决倒计时：最早 {min} 秒，最晚 {max} 秒', decisionExpired: '该请求已不再等待人工决定。',
    allowedForwarding: '已放行——正在转发上游并等待响应。', dragSelect: '在画布空白处拖拽可框选多个请求', selectedCount: '已选 {count} 条请求',
    selectedPending: '其中 {count} 条仍待人工决定', batchHistory: '这些请求已经结束；只能为后续同类流量创建规则。', allowSelected: '放行选中待决请求', blockSelected: '阻断选中待决请求', blockSelectedFuture: '阻断选中路径的后续流量',
    burst: '并发 20 条', emergencyBlock: '紧急阻断全部待决', confirmEmergency: '确认阻断当前全部待决请求？此操作会立即终止这些请求。', rendering: '绘制 {shown}/{total}',
    overloadMode: '高流量聚合', overloadSummary: '已聚合 {total} 个普通事件 · {rate}/秒', overloadHint: '待决和阻断请求仍逐条展示、可直接操作。', blockFlow: '阻断此流量',
    policyActive: '策略 C v{version} 已生效', policyFallback: '策略 v{version} 已同步 · Lua 兼容路径', policyPending: '策略 C v{version} 同步中', policyDisabled: '策略 C 未启用',
    activeFlows: '活跃', rate: '速率', p95: 'P95', overview: '概览', headers: 'Header', body: 'Body',
    requestId: '请求 ID', subject: '监管对象', requestSize: '请求大小', started: '开始时间', finished: '完成时间',
    requestHeaders: '请求 Header', responseHeaders: '响应 Header', requestBody: '请求 Body', responseBody: '响应 Body',
    captureOff: '内容采集尚未开启。后续将按规则限长、脱敏并默认关闭。', managedSubject: '监管对象', topology: '{routes} 条路由 · {upstreams} 个上游',
    signIn: '登录 HTTPV', accessToken: '访问 Token', signInAction: '登录', invalidToken: 'Token 无效或登录失败', signOut: '退出', roleViewer: '只读', roleOperator: '操作员', roleAdmin: '管理员',
    captureSettings: '详情采集', captureEnabled: '开启采集', captureRequestHeaders: '请求 Header', captureResponseHeaders: '响应 Header', captureRequestBody: '请求 Body', captureResponseBody: '响应 Body', captureLimit: 'Body 上限', captureTTL: '保留秒数', captureSample: '采样率', captureSensitive: '采集内容可能包含敏感数据；强制脱敏鉴权与 Cookie Header。', captureLoading: '正在读取详情…', captureUnavailable: '该请求未采集详情、已过期或当前角色无权读取。', captureTruncated: '内容已截断',
    gatewayConfig: '网关配置', configValidate: '校验', configApply: '应用配置', configValid: '配置有效', configApplied: '配置已应用', configInvalid: '配置无效', configHistory: '版本历史', configRollback: '回滚', configClose: '关闭', confirmRollback: '确认回滚到版本 {revision}？回滚会生成一个新版本。'
  },
  en: {
    commandSurface: 'Traffic command surface', connecting: 'connecting', live: 'live', closed: 'closed',
    tracked: '{count} tracked · {pending} waiting', gateOn: 'Manual gate: ON', gateOff: 'Manual gate: OFF',
    sendCurrent: 'Send current request', guidedDemo: 'Send guided demo', request: 'request', response: 'response',
    waiting: 'waiting for manual decision', blocked: 'blocked', tapParticle: 'tap a particle for details', liveState: 'Live state',
    gateActive: 'Manual gate is active.', gateInstruction: 'The next matching request will pause as a yellow particle until you select Allow or Block.',
    ready: 'Ready — run the guided demo to create one visible request.', waitingDecision: 'Waiting for your decision — tap the yellow particle, then Allow or Block.',
    flowing: 'Request flowing to upstream: {method} {path}', returning: 'Upstream has responded — response is returning to the client.',
    completed: 'Completed: {status} in {duration}ms. Its trail remains visible briefly.', clientAborted: 'The client disconnected and the request has ended.', trafficControls: 'Traffic controls',
    managedPath: 'Managed path', gateTimeout: 'Gate timeout', timeoutAction: 'On timeout', block: 'Block', allow: 'Allow', maxPending: 'Max pending', overflowAction: 'On overflow', failureAction: 'On failure', reject503: 'Reject 503', failOpen: 'Allow (fail-open)', failClosed: 'Block (fail-closed)',
    regulateTraffic: 'Regulate matching traffic', showEvents: 'Show events', render: 'Render {value}ms', trail: 'Trail {value}ms',
    selectedRequest: 'Selected request', client: 'Client', status: 'Status', duration: 'Duration', responseSize: 'Response size',
    inFlight: 'in flight', unavailable: 'not available', allowPending: 'Allow pending', blockPending: 'Block pending',
    blockFuture: 'Block future matches', selectHint: 'Tap a request particle to inspect its metadata and apply a control action.',
    activeRules: 'Active dynamic rules', noRules: 'No dynamic block rules.', remove: 'Remove', language: 'Language',
    pendingRemaining: 'Waiting for a decision — {seconds}s remaining', pendingInfinite: 'Waiting for a manual decision', batchRemaining: 'Pending countdown: {min}s earliest, {max}s latest', decisionExpired: 'This request is no longer waiting for a decision.',
    allowedForwarding: 'Allowed — forwarding to the upstream and awaiting its response.', dragSelect: 'Drag across empty canvas space to box-select requests', selectedCount: '{count} requests selected',
    selectedPending: '{count} still waiting for a decision', batchHistory: 'These requests have completed; only future-match rules can be created.', allowSelected: 'Allow selected pending', blockSelected: 'Block selected pending', blockSelectedFuture: 'Block future traffic for selected paths',
    burst: 'Burst ×20', emergencyBlock: 'Emergency block all waiting', confirmEmergency: 'Block every currently waiting request? This immediately terminates those requests.', rendering: 'rendering {shown}/{total}',
    overloadMode: 'High-traffic aggregation', overloadSummary: '{total} ordinary events aggregated · {rate}/s', overloadHint: 'Waiting and blocked requests remain individually visible and actionable.', blockFlow: 'Block this flow',
    policyActive: 'Policy C v{version} active', policyFallback: 'Policy v{version} synced · Lua compatibility path', policyPending: 'Policy C v{version} syncing', policyDisabled: 'Policy C disabled',
    activeFlows: 'active', rate: 'rate', p95: 'P95', overview: 'Overview', headers: 'Headers', body: 'Body',
    requestId: 'Request ID', subject: 'Subject', requestSize: 'Request size', started: 'Started', finished: 'Finished',
    requestHeaders: 'Request headers', responseHeaders: 'Response headers', requestBody: 'Request body', responseBody: 'Response body',
    captureOff: 'Content capture is not enabled. It will be opt-in, size-limited, and redacted.', managedSubject: 'Managed subject', topology: '{routes} routes · {upstreams} upstreams',
    signIn: 'Sign in to HTTPV', accessToken: 'Access token', signInAction: 'Sign in', invalidToken: 'Invalid token or sign-in failed', signOut: 'Sign out', roleViewer: 'viewer', roleOperator: 'operator', roleAdmin: 'admin',
    captureSettings: 'Detail capture', captureEnabled: 'Enable capture', captureRequestHeaders: 'Request headers', captureResponseHeaders: 'Response headers', captureRequestBody: 'Request body', captureResponseBody: 'Response body', captureLimit: 'Body limit', captureTTL: 'Retention seconds', captureSample: 'Sample rate', captureSensitive: 'Captured content may be sensitive; authentication and cookie headers are always redacted.', captureLoading: 'Loading details…', captureUnavailable: 'Details were not captured, have expired, or are not available to this role.', captureTruncated: 'Content truncated',
    gatewayConfig: 'Gateway configuration', configValidate: 'Validate', configApply: 'Apply configuration', configValid: 'Configuration is valid', configApplied: 'Configuration applied', configInvalid: 'Invalid configuration', configHistory: 'Revision history', configRollback: 'Rollback', configClose: 'Close', confirmRollback: 'Rollback to revision {revision}? Rollback creates a new revision.'
  }
}

const subject = ref<Subject>({
  id: 'default', name: 'Default traffic', enabled: true, display: true,
  match: { host: '', path_prefix: '/', method: '' },
  gate: { enabled: false, timeout_ms: 30000, timeout_action: 'block', max_pending: 2000, overflow_action: 'block', failure_action: 'allow' },
  capture: { enabled: false, request_headers: true, response_headers: true, request_body: false, response_body: false, max_body_bytes: 16384, ttl_seconds: 60, sample_rate: 1, content_types: ['application/json', 'application/x-www-form-urlencoded', 'application/xml', 'text/'], redact_headers: ['authorization', 'cookie', 'set-cookie', 'proxy-authorization', 'x-api-key'] }
})
const subjects = ref<Subject[]>([])
const upstreams = ref<Upstream[]>([])
const routes = ref<Route[]>([])
const rules = ref<Rule[]>([])
const traffic = ref(new Map<string, TrafficRecord>())
const selectedID = ref<string>()
const selectedIds = ref<string[]>([])
const renderDelayMs = ref(0)
const trailMs = ref(3000)
const detailTab = ref<'overview' | 'headers' | 'body'>('overview')
const connectionState = ref<'connecting' | 'live' | 'closed'>('connecting')
const locale = ref<Locale>(localStorage.getItem('httpv-locale') === 'en' ? 'en' : 'zh')
const nowMs = ref(Date.now())
const burstBusy = ref(false)
const overloadSummary = ref<TrafficSummary>()
const policyStatus = ref<NativePolicyStatus>({ version: 0, state: 'unpublished', acks: [] })
const authStatus = ref<AuthStatus>({ enabled: false, authenticated: false })
const authReady = ref(false)
const csrfToken = ref('')
const loginToken = ref('')
const loginError = ref('')
const requestDetails = ref<RequestDetails>()
const detailsLoading = ref(false)
const detailsError = ref('')
const configEditorOpen = ref(false)
const configEditorText = ref('')
const configEditorMessage = ref('')
const configEditorError = ref(false)
const configRevision = ref(0)
const configRevisions = ref<ConfigRevisionSummary[]>([])
const timeoutEventGraceMs = 2_000
const selectedDetailRetentionMs = 60_000
let socket: WebSocket | undefined
let clockTimer: number | undefined
let policyTimer: number | undefined
let eventFrame: number | undefined
const eventQueue: EventMessage[] = []
const canOperate = computed(() => authStatus.value.role === 'operator' || authStatus.value.role === 'admin')
const canAdmin = computed(() => authStatus.value.role === 'admin')

async function apiFetch(input: RequestInfo | URL, init: RequestInit = {}) {
  const headers = new Headers(init.headers)
  const method = (init.method ?? 'GET').toUpperCase()
  if (csrfToken.value && ['POST', 'PUT', 'PATCH', 'DELETE'].includes(method)) headers.set('X-HTTPV-CSRF', csrfToken.value)
  return fetch(input, { ...init, headers, credentials: 'same-origin' })
}

async function readError(response: Response) {
  try {
    const payload = await response.json() as { error?: string }
    return payload.error ?? `${response.status} ${response.statusText}`
  } catch {
    return `${response.status} ${response.statusText}`
  }
}

function t(key: string, values: Record<string, string | number> = {}) {
  return (messages[locale.value][key] ?? key).replace(/\{(\w+)\}/g, (_, name: string) => String(values[name] ?? `{${name}}`))
}

function setLocale(next: Locale) {
  locale.value = next
  localStorage.setItem('httpv-locale', next)
}

function onLocaleChange(event: Event) {
  setLocale((event.target as HTMLSelectElement).value as Locale)
}

function onSubjectChange(event: Event) {
  const next = subjects.value.find((candidate) => candidate.id === (event.target as HTMLSelectElement).value)
  if (next) subject.value = structuredClone(next)
}

const records = computed(() => Array.from(traffic.value.values()).sort((a, b) => a.startedAt - b.startedAt))
const selected = computed(() => selectedID.value ? traffic.value.get(selectedID.value) : undefined)
const selectedRecords = computed(() => selectedIds.value.map((id) => traffic.value.get(id)).filter((record): record is TrafficRecord => Boolean(record)))
const pendingCount = computed(() => records.value.filter(isPendingActive).length)
const activeFlowCount = computed(() => records.value.filter((record) => !record.finishedAt && !record.abortedAt && !record.blockedAt).length)
const currentRps = computed(() => records.value.filter((record) => record.startedAt >= nowMs.value - 1_000).length)
const p95Duration = computed(() => {
  const durations = records.value.map((record) => record.durationMs).filter((duration): duration is number => duration !== undefined).sort((a, b) => a - b)
  if (durations.length === 0) return '—'
  return durations[Math.min(durations.length - 1, Math.ceil(durations.length * 0.95) - 1)]
})
function isPendingActive(record: TrafficRecord) {
  if (!record.pendingAt || record.releasedAt || record.blockedAt || record.abortedAt || record.finishedAt) return false
  const timeout = subject.value.gate.timeout_ms
  return timeout === 0 || nowMs.value < record.pendingAt + timeout
}
const selectedPendingActive = computed(() => {
  const record = selected.value
  return record ? isPendingActive(record) : false
})
const selectedPendingRecords = computed(() => selectedRecords.value.filter(isPendingActive))
const batchPendingRemaining = computed(() => {
  const pending = selectedPendingRecords.value
  if (pending.length === 0) return ''
  const timeout = subject.value.gate.timeout_ms
  if (timeout === 0) return t('pendingInfinite')
  const seconds = pending.map((record) => Math.max(0, Math.ceil((record.pendingAt! + timeout - nowMs.value) / 1000)))
  return t('batchRemaining', { min: Math.min(...seconds), max: Math.max(...seconds) })
})
const animatedIds = computed(() => {
  const selectedSet = new Set(selectedIds.value)
  const prioritized = [
    ...records.value.filter((record) => selectedSet.has(record.requestId)),
    ...records.value.filter((record) => record.pendingAt && !record.releasedAt && !record.blockedAt && !record.abortedAt && !record.finishedAt),
    ...records.value.slice(-360).reverse()
  ]
  const unique = new Set<string>()
  for (const record of prioritized) if (unique.size < 360) unique.add(record.requestId)
  return [...unique]
})
const selectedPendingLabel = computed(() => {
  const record = selected.value
  if (!record?.pendingAt || record.releasedAt || record.blockedAt || record.abortedAt || record.finishedAt) return ''
  const timeout = subject.value.gate.timeout_ms
  if (timeout === 0) return t('pendingInfinite')
  const seconds = Math.max(0, Math.ceil((record.pendingAt + timeout - nowMs.value) / 1000))
  return t('pendingRemaining', { seconds })
})
const latestRecord = computed(() => records.value.at(-1))
const overloadActive = computed(() => Boolean(overloadSummary.value && nowMs.value - overloadSummary.value.window_end_ms < 1_500))
const policyLabel = computed(() => {
  if (policyStatus.value.state === 'active' && policyStatus.value.mode === 'lua_fallback') return t('policyFallback', { version: policyStatus.value.version })
  if (policyStatus.value.state === 'active') return t('policyActive', { version: policyStatus.value.version })
  if (policyStatus.value.state === 'pending') return t('policyPending', { version: policyStatus.value.version })
  return t('policyDisabled')
})
const flowStatus = computed(() => {
  const record = latestRecord.value
  if (!record) return t('ready')
  if (record.pendingAt && !record.releasedAt && !record.blockedAt && !record.abortedAt && !record.finishedAt) return t('waitingDecision')
  if (record.abortedAt) return t('clientAborted')
  if (record.releasedAt && !record.responseStartedAt) return t('allowedForwarding')
  if (!record.responseStartedAt) return t('flowing', { method: record.method, path: record.path })
  if (!record.finishedAt) return t('returning')
  return t('completed', { status: record.status ?? '—', duration: record.durationMs ?? '—' })
})

function applyEventToMap(event: EventMessage, target: Map<string, TrafficRecord>) {
  const existing = target.get(event.request_id)
  const record: TrafficRecord = existing ?? {
    requestId: event.request_id,
    subjectId: event.subject_id,
    method: event.method ?? 'GET',
    path: event.path ?? '/',
    clientIp: event.client_ip ?? '',
    startedAt: event.timestamp_ms
  }
  record.method = event.method ?? record.method
  record.path = event.path ?? record.path
  record.clientIp = event.client_ip ?? record.clientIp
  record.requestBytes = event.request_bytes ?? record.requestBytes
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
  if (target.size > 5_000) target.delete(target.keys().next().value as string)
}

function flushEvents() {
  eventFrame = undefined
  const next = new Map(traffic.value)
  for (const event of eventQueue.splice(0)) applyEventToMap(event, next)
  traffic.value = next
}

function enqueueEvent(event: EventMessage) {
  eventQueue.push(event)
  if (eventFrame === undefined) eventFrame = window.requestAnimationFrame(flushEvents)
}

function recordVisualEnd(record: TrafficRecord) {
  const terminalAt = record.finishedAt
    ?? record.blockedAt
    ?? record.abortedAt
  if (terminalAt === undefined) return undefined
  const retention = selectedID.value === record.requestId ? selectedDetailRetentionMs : Math.max(1_000, trailMs.value)
  return terminalAt + renderDelayMs.value + 420 + retention
}

function pruneExpiredRecords() {
  const next = new Map(traffic.value)
  let changed = false
  for (const [id, record] of next) {
    const unresolved = Boolean(record.pendingAt && !record.releasedAt && !record.blockedAt && !record.abortedAt && !record.finishedAt)
    const unresolvedExpiry = unresolved && subject.value.gate.timeout_ms > 0
      ? record.pendingAt! + subject.value.gate.timeout_ms + timeoutEventGraceMs
      : undefined
    const terminalVisualEnd = recordVisualEnd(record)
    if ((unresolvedExpiry !== undefined && nowMs.value > unresolvedExpiry) || (terminalVisualEnd !== undefined && nowMs.value > terminalVisualEnd)) {
      next.delete(id)
      changed = true
    }
  }
  if (!changed) return
  traffic.value = next
  selectedIds.value = selectedIds.value.filter((id) => next.has(id))
  if (selectedID.value && !next.has(selectedID.value)) selectedID.value = undefined
}

function applySummary(summary: TrafficSummary) {
  overloadSummary.value = summary
}

function summaryRate(summary: TrafficSummary) {
  const duration = Math.max(1, summary.window_end_ms - summary.window_start_ms)
  return Math.round(summary.total * 1_000 / duration)
}

async function bootstrap() {
  const response = await apiFetch('/api/bootstrap')
  if (!response.ok) throw new Error('Unable to load control state')
  const data = await response.json()
  configRevision.value = data.config_revision ?? configRevision.value
  subjects.value = data.config.subjects
  upstreams.value = data.config.upstreams
  routes.value = data.config.routes
  const current = subjects.value.find((candidate) => candidate.id === subject.value.id) ?? subjects.value[0]
  if (current) subject.value = structuredClone(current)
  rules.value = data.config.rules
  const next = new Map<string, TrafficRecord>()
  for (const event of data.events as EventMessage[]) applyEventToMap(event, next)
  const cutoff = Date.now() - 15_000
  for (const [id, record] of next) {
    const keepPending = isPendingActive(record)
    const visualEnd = recordVisualEnd(record)
    if (!keepPending && ((visualEnd !== undefined && Date.now() > visualEnd) || (visualEnd === undefined && record.startedAt < cutoff))) next.delete(id)
  }
  traffic.value = next
}

async function refreshPolicyStatus() {
  try {
    const response = await apiFetch('/api/policy-status')
    if (response.ok) policyStatus.value = await response.json() as NativePolicyStatus
  } catch {
    // Policy status is advisory; a transient control-plane read must not affect traffic control.
  }
}

async function refreshConfigRevisions() {
  const response = await apiFetch('/api/config/revisions')
  if (!response.ok) return
  const payload = await response.json() as { current: number; revisions: ConfigRevisionSummary[] }
  configRevision.value = payload.current
  configRevisions.value = payload.revisions
}

async function openConfigEditor() {
  configEditorMessage.value = ''
  configEditorError.value = false
  const response = await apiFetch('/api/config')
  if (!response.ok) return
  configEditorText.value = JSON.stringify(await response.json(), null, 2)
  await refreshConfigRevisions()
  configEditorOpen.value = true
}

function parsedEditorConfig() {
  try {
    return JSON.parse(configEditorText.value)
  } catch {
    configEditorMessage.value = t('configInvalid')
    configEditorError.value = true
    return undefined
  }
}

async function validateEditorConfig() {
  const config = parsedEditorConfig()
  if (!config) return
  const response = await apiFetch('/api/config/validate', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(config)
  })
  configEditorError.value = !response.ok
  configEditorMessage.value = response.ok ? t('configValid') : await readError(response)
}

async function applyEditorConfig() {
  const config = parsedEditorConfig()
  if (!config) return
  const response = await apiFetch('/api/config', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(config)
  })
  configEditorError.value = !response.ok
  if (!response.ok) {
    configEditorMessage.value = await readError(response)
    return
  }
  configEditorText.value = JSON.stringify(await response.json(), null, 2)
  configEditorMessage.value = t('configApplied')
  await bootstrap()
  await refreshConfigRevisions()
}

async function rollbackEditorConfig(revision: number) {
  if (!window.confirm(t('confirmRollback', { revision }))) return
  const response = await apiFetch(`/api/config/rollback/${revision}`, {
    method: 'POST', headers: { 'X-HTTPV-Confirm': `rollback:${revision}` }
  })
  configEditorError.value = !response.ok
  if (!response.ok) {
    configEditorMessage.value = await readError(response)
    return
  }
  const payload = await response.json() as { revision: number; config: unknown }
  configEditorText.value = JSON.stringify(payload.config, null, 2)
  configEditorMessage.value = t('configApplied')
  await bootstrap()
  await refreshConfigRevisions()
}

function connect() {
  connectionState.value = 'connecting'
  const protocol = location.protocol === 'https:' ? 'wss' : 'ws'
  socket = new WebSocket(`${protocol}://${location.host}/api/ws`)
  socket.onopen = () => { connectionState.value = 'live' }
  socket.onmessage = (message) => {
    const payload = JSON.parse(message.data) as EventMessage | TrafficSummary
    if ('type' in payload && payload.type === 'traffic_summary') applySummary(payload)
    else enqueueEvent(payload as EventMessage)
  }
  socket.onclose = () => {
    connectionState.value = 'closed'
    window.setTimeout(connect, 1000)
  }
}

async function saveSubject() {
  const response = await apiFetch(`/api/subjects/${encodeURIComponent(subject.value.id)}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(subject.value)
  })
  if (!response.ok) { alert('OpenResty has not accepted the configuration yet.'); return }
  const applied = await response.json() as Subject
  subject.value = applied
  subjects.value = subjects.value.map((candidate) => candidate.id === applied.id ? applied : candidate)
}

async function toggleGate() {
  subject.value.gate.enabled = !subject.value.gate.enabled
  await saveSubject()
}

async function decide(action: 'allow' | 'block') {
  if (!selectedPendingActive.value || !selected.value) {
    alert(t('decisionExpired'))
    return
  }
  const response = await apiFetch(`/api/pending/${selected.value.requestId}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action })
  })
  if (!response.ok) alert(t('decisionExpired'))
}

function selectOne(record: TrafficRecord) {
  detailTab.value = 'overview'
  requestDetails.value = undefined
  detailsError.value = ''
  selectedID.value = record.requestId
  selectedIds.value = [record.requestId]
}

function selectMany(records: TrafficRecord[]) {
  detailTab.value = 'overview'
  requestDetails.value = undefined
  detailsError.value = ''
  const ids = [...new Set(records.map((record) => record.requestId))]
  selectedIds.value = ids
  selectedID.value = ids.length === 1 ? ids[0] : undefined
}

async function loadRequestDetails() {
  if (!selected.value || requestDetails.value?.request_id === selected.value.requestId || detailsLoading.value) return
  if (!canAdmin.value) {
    detailsError.value = t('captureUnavailable')
    return
  }
  const requestID = selected.value.requestId
  detailsLoading.value = true
  detailsError.value = ''
  try {
    const response = await apiFetch(`/api/requests/${encodeURIComponent(requestID)}/details`)
    if (!response.ok) {
      detailsError.value = t('captureUnavailable')
      return
    }
    const payload = await response.json() as RequestDetails
    if (selected.value?.requestId === requestID) requestDetails.value = payload
  } catch {
    detailsError.value = t('captureUnavailable')
  } finally {
    detailsLoading.value = false
  }
}

function setDetailTab(tab: 'overview' | 'headers' | 'body') {
  detailTab.value = tab
  if (tab !== 'overview') void loadRequestDetails()
}

function formatCapturedHeaders(headers?: Record<string, unknown>) {
  if (!headers) return t('captureUnavailable')
  return JSON.stringify(headers, null, 2)
}

function formatCapturedBody(side?: CapturedSide) {
  if (!side?.body) return side?.body_unavailable ? `${t('captureUnavailable')} (${side.body_unavailable})` : t('captureUnavailable')
  return side.truncated ? `${side.body}\n\n[${t('captureTruncated')}]` : side.body
}

async function decideSelected(action: 'allow' | 'block') {
  const pending = selectedPendingRecords.value
  if (pending.length === 0) {
    alert(t('decisionExpired'))
    return
  }
  const response = await apiFetch('/api/pending/decide-batch', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ request_ids: pending.map((record) => record.requestId), action })
  })
  if (!response.ok) alert(t('decisionExpired'))
}

async function blockAllPending() {
  if (!window.confirm(t('confirmEmergency'))) return
  const response = await apiFetch('/api/pending/block-all', { method: 'POST', headers: { 'X-HTTPV-Confirm': 'block-all-pending' } })
  if (!response.ok) alert(t('decisionExpired'))
}

async function blockSelectedPaths() {
  const unique = new Map(selectedRecords.value.map((record) => [`${record.method}:${record.path}`, record]))
  const created = await Promise.all([...unique.values()].map(async (record) => {
    const response = await apiFetch('/api/rules', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ subject_id: record.subjectId, action: 'block', path_prefix: record.path, method: record.method, client_ip: '' })
    })
    return response.ok ? response.json() as Promise<Rule> : undefined
  }))
  rules.value.push(...created.filter((rule): rule is Rule => Boolean(rule)))
}

async function blockPath() {
  if (!selected.value) return
  const response = await apiFetch('/api/rules', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ subject_id: selected.value.subjectId, action: 'block', path_prefix: selected.value.path, method: selected.value.method, client_ip: '' })
  })
  if (!response.ok) { alert('Unable to create the block rule.'); return }
  rules.value.push(await response.json())
}

async function blockAggregatePath(path: TrafficSummaryPath) {
  if (!path.path || path.path === 'other') return
  const response = await apiFetch('/api/rules', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ subject_id: path.subject_id, action: 'block', path_prefix: path.path, method: path.method, client_ip: '' })
  })
  if (!response.ok) { alert('Unable to create the block rule.'); return }
  rules.value.push(await response.json())
}

async function removeRule(id: string) {
  const response = await apiFetch(`/api/rules/${id}`, { method: 'DELETE' })
  if (response.ok) rules.value = rules.value.filter((rule) => rule.id !== id)
}

async function sendTestRequest() {
  try {
    await apiFetch('/api/demo/burst', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ count: 1 })
    })
  } catch {
    // The visual control flow is the subject of this prototype; an unavailable test generator is non-fatal.
  }
}

async function sendBurst() {
  if (burstBusy.value) return
  burstBusy.value = true
  try {
    await apiFetch('/api/demo/burst', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ count: 20 })
    })
  } finally {
    window.setTimeout(() => { burstBusy.value = false }, 200)
  }
}

async function runGuidedDemo() {
  renderDelayMs.value = 0
  trailMs.value = 4000
  selectedID.value = undefined
  await sendTestRequest()
}

function formatTimestamp(value?: number) {
  if (value === undefined) return t('unavailable')
  return new Date(value).toLocaleTimeString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { hour12: false, fractionalSecondDigits: 3 })
}

function roleLabel() {
  if (authStatus.value.role === 'admin') return t('roleAdmin')
  if (authStatus.value.role === 'operator') return t('roleOperator')
  return t('roleViewer')
}

async function startConsole() {
  if (clockTimer !== undefined) return
  clockTimer = window.setInterval(() => {
    nowMs.value = Date.now()
    pruneExpiredRecords()
  }, 250)
  policyTimer = window.setInterval(refreshPolicyStatus, 1_000)
  try { await bootstrap() } catch (error) { console.error(error) }
  await refreshPolicyStatus()
  connect()
}

async function signIn() {
  loginError.value = ''
  const response = await fetch('/api/auth/login', {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token: loginToken.value })
  })
  if (!response.ok) {
    loginError.value = t('invalidToken')
    return
  }
  location.reload()
}

async function signOut() {
  await apiFetch('/api/auth/logout', { method: 'POST' })
  location.reload()
}

onMounted(async () => {
  try {
    const response = await fetch('/api/auth/status', { credentials: 'same-origin' })
    if (!response.ok) throw new Error('auth status unavailable')
    authStatus.value = await response.json() as AuthStatus
    csrfToken.value = authStatus.value.csrf_token ?? ''
  } catch {
    authStatus.value = { enabled: true, authenticated: false }
  }
  authReady.value = true
  if (authStatus.value.authenticated) await startConsole()
})
onBeforeUnmount(() => {
  socket?.close()
  if (clockTimer) window.clearInterval(clockTimer)
  if (policyTimer) window.clearInterval(policyTimer)
  if (eventFrame !== undefined) window.cancelAnimationFrame(eventFrame)
})
</script>

<template>
  <div v-if="!authReady" class="auth-shell"></div>
  <section v-else-if="authStatus.enabled && !authStatus.authenticated" class="auth-shell">
    <form class="login-panel" @submit.prevent="signIn">
      <p class="eyebrow">HTTPV / CONTROL PLANE</p>
      <h1>{{ t('signIn') }}</h1>
      <label><span>{{ t('accessToken') }}</span><input v-model="loginToken" type="password" autocomplete="current-password" autofocus /></label>
      <p v-if="loginError" class="login-error">{{ loginError }}</p>
      <button class="test-button" type="submit">{{ t('signInAction') }}</button>
    </form>
  </section>
  <main v-else class="app-shell">
    <header class="topbar">
      <p class="eyebrow">HTTPV / LOCAL PROTOTYPE</p>
      <div class="topbar-right">
        <div class="policy-state" :class="policyStatus.state">{{ policyLabel }}</div>
        <span v-if="authStatus.enabled" class="role-state">{{ roleLabel() }}</span>
        <button v-if="canAdmin" class="sign-out" @click="openConfigEditor">{{ t('gatewayConfig') }}</button>
        <label class="language-picker"><span>{{ t('language') }}</span><select :value="locale" @change="onLocaleChange"><option value="zh">中文</option><option value="en">English</option></select></label>
        <div class="status" :class="connectionState"><span></span>{{ t(connectionState) }}</div>
        <button v-if="authStatus.enabled" class="sign-out" @click="signOut">{{ t('signOut') }}</button>
      </div>
    </header>

    <div class="command-layout">
      <section class="canvas-panel" :class="{ 'has-overload': overloadActive && overloadSummary }">
        <div class="canvas-header">
          <div class="subject-summary"><strong>{{ subject.name }}</strong><span>{{ t('tracked', { count: records.length, pending: pendingCount }) }} · {{ t('rendering', { shown: animatedIds.length, total: records.length }) }}</span></div>
          <div class="live-metrics">
            <span><b>{{ currentRps }}</b>{{ t('rate') }}/s</span>
            <span><b>{{ activeFlowCount }}</b>{{ t('activeFlows') }}</span>
            <span><b>{{ p95Duration }}</b>{{ t('p95') }}<i v-if="p95Duration !== '—'">ms</i></span>
          </div>
          <div class="header-actions">
            <button class="gate-toggle" :class="{ active: subject.gate.enabled }" :disabled="!canAdmin" @click="toggleGate">{{ subject.gate.enabled ? t('gateOn') : t('gateOff') }}</button>
            <button v-if="pendingCount > 0" class="emergency-button" :disabled="!canOperate" @click="blockAllPending">{{ t('emergencyBlock') }}</button>
            <button class="outline compact" :disabled="!canOperate" @click="sendTestRequest">{{ t('sendCurrent') }}</button>
            <button class="outline compact" :disabled="burstBusy || !canOperate" @click="sendBurst">{{ t('burst') }}</button>
            <button class="test-button" :disabled="!canOperate" @click="runGuidedDemo">{{ t('guidedDemo') }}</button>
          </div>
        </div>
        <div v-if="overloadActive && overloadSummary" class="overload-strip">
          <div class="overload-copy"><b>{{ t('overloadMode') }}</b><span>{{ t('overloadSummary', { total: overloadSummary.total, rate: summaryRate(overloadSummary) }) }}</span><small>{{ t('overloadHint') }}</small></div>
          <div class="overload-paths"><button v-for="path in overloadSummary.paths" :key="`${path.subject_id}:${path.method}:${path.path}`" :disabled="!canOperate || !path.path || path.path === 'other'" @click="blockAggregatePath(path)"><span>{{ path.method || '*' }} {{ path.path || '*' }}</span><b>{{ path.count }}</b><em>{{ t('blockFlow') }}</em></button></div>
        </div>
        <TrafficCanvas :records="records" :animated-ids="animatedIds" :render-delay-ms="renderDelayMs" :trail-ms="trailMs" :selected-ids="selectedIds" @select="selectOne" @select-many="selectMany" />
        <div class="legend"><span class="legend-blue"></span>{{ t('request') }} <span class="legend-green"></span>{{ t('response') }} <span class="legend-yellow"></span>{{ t('waiting') }} <span class="legend-red"></span>{{ t('blocked') }} · {{ t('tapParticle') }} · {{ t('dragSelect') }}</div>
        <div class="flow-status" :class="{ waiting: pendingCount > 0 }"><b>{{ t('liveState') }}</b>{{ flowStatus }}</div>
        <div v-if="subject.gate.enabled" class="gate-banner"><b>{{ t('gateActive') }}</b>{{ t('gateInstruction') }}</div>
      </section>

      <aside class="command-rail">
        <article class="panel operating-panel">
          <div class="panel-title controls-title">
            <div><h2>{{ t('trafficControls') }}</h2><small>{{ t('topology', { routes: routes.length, upstreams: upstreams.length }) }}</small></div>
            <label class="subject-picker"><span>{{ t('managedSubject') }}</span><select :value="subject.id" @change="onSubjectChange"><option v-for="candidate in subjects" :key="candidate.id" :value="candidate.id">{{ candidate.name }}</option></select></label>
          </div>
          <div class="field-row">
            <label><span>{{ t('managedPath') }}</span><input v-model="subject.match.path_prefix" :disabled="!canAdmin" @change="saveSubject" /></label>
            <label><span>{{ t('gateTimeout') }}</span><input v-model.number="subject.gate.timeout_ms" :disabled="!canAdmin" min="0" step="1000" type="number" @change="saveSubject" /></label>
            <label><span>{{ t('timeoutAction') }}</span><select v-model="subject.gate.timeout_action" :disabled="!canAdmin" @change="saveSubject"><option value="block">{{ t('block') }}</option><option value="allow">{{ t('allow') }}</option></select></label>
          </div>
          <div class="field-row protection-row">
            <label><span>{{ t('maxPending') }}</span><input v-model.number="subject.gate.max_pending" :disabled="!canAdmin" min="0" step="100" type="number" @change="saveSubject" /></label>
            <label><span>{{ t('overflowAction') }}</span><select v-model="subject.gate.overflow_action" :disabled="!canAdmin" @change="saveSubject"><option value="allow">{{ t('allow') }}</option><option value="block">{{ t('block') }}</option><option value="reject">{{ t('reject503') }}</option></select></label>
            <label><span>{{ t('failureAction') }}</span><select v-model="subject.gate.failure_action" :disabled="!canAdmin" @change="saveSubject"><option value="allow">{{ t('failOpen') }}</option><option value="block">{{ t('failClosed') }}</option></select></label>
          </div>
          <div class="inline-switches">
            <label class="switch"><input v-model="subject.enabled" :disabled="!canAdmin" type="checkbox" @change="saveSubject" /><span>{{ t('regulateTraffic') }}</span></label>
            <label class="switch"><input v-model="subject.display" :disabled="!canAdmin" type="checkbox" @change="saveSubject" /><span>{{ t('showEvents') }}</span></label>
          </div>
          <details class="capture-config">
            <summary>{{ t('captureSettings') }}</summary>
            <div class="capture-switches">
              <label class="switch"><input v-model="subject.capture.enabled" :disabled="!canAdmin" type="checkbox" @change="saveSubject" /><span>{{ t('captureEnabled') }}</span></label>
              <label class="switch"><input v-model="subject.capture.request_headers" :disabled="!canAdmin" type="checkbox" @change="saveSubject" /><span>{{ t('captureRequestHeaders') }}</span></label>
              <label class="switch"><input v-model="subject.capture.response_headers" :disabled="!canAdmin" type="checkbox" @change="saveSubject" /><span>{{ t('captureResponseHeaders') }}</span></label>
              <label class="switch"><input v-model="subject.capture.request_body" :disabled="!canAdmin" type="checkbox" @change="saveSubject" /><span>{{ t('captureRequestBody') }}</span></label>
              <label class="switch"><input v-model="subject.capture.response_body" :disabled="!canAdmin" type="checkbox" @change="saveSubject" /><span>{{ t('captureResponseBody') }}</span></label>
            </div>
            <div class="capture-policy-fields">
              <label><span>{{ t('captureLimit') }}</span><input v-model.number="subject.capture.max_body_bytes" :disabled="!canAdmin" min="1" max="1048576" step="1024" type="number" @change="saveSubject" /></label>
              <label><span>{{ t('captureTTL') }}</span><input v-model.number="subject.capture.ttl_seconds" :disabled="!canAdmin" min="1" max="3600" step="10" type="number" @change="saveSubject" /></label>
              <label><span>{{ t('captureSample') }}</span><input v-model.number="subject.capture.sample_rate" :disabled="!canAdmin" min="0.01" max="1" step="0.05" type="number" @change="saveSubject" /></label>
            </div>
            <p class="capture-warning">{{ t('captureSensitive') }}</p>
          </details>
          <div class="slider-row">
            <label><span>{{ t('render', { value: renderDelayMs }) }}</span><input v-model.number="renderDelayMs" min="0" max="10000" step="25" type="range" /></label>
            <label><span>{{ t('trail', { value: trailMs }) }}</span><input v-model.number="trailMs" min="100" max="10000" step="100" type="range" /></label>
          </div>
        </article>

        <article class="panel detail-panel">
        <div class="panel-title"><h2>{{ t('selectedRequest') }}</h2><span v-if="selected?.status" class="status-code" :class="{ error: selected.status >= 400 }">{{ selected.status }}</span></div>
        <template v-if="selected">
          <div class="request-title"><b>{{ selected.method }}</b><code>{{ selected.path }}</code></div>
          <nav class="detail-tabs">
            <button :class="{ active: detailTab === 'overview' }" @click="setDetailTab('overview')">{{ t('overview') }}</button>
            <button :class="{ active: detailTab === 'headers' }" @click="setDetailTab('headers')">{{ t('headers') }}</button>
            <button :class="{ active: detailTab === 'body' }" @click="setDetailTab('body')">{{ t('body') }}</button>
          </nav>
          <dl v-if="detailTab === 'overview'" class="metadata-grid">
            <div><dt>{{ t('requestId') }}</dt><dd class="mono">{{ selected.requestId }}</dd></div>
            <div><dt>{{ t('subject') }}</dt><dd>{{ selected.subjectId }}</dd></div>
            <div><dt>{{ t('client') }}</dt><dd>{{ selected.clientIp || t('unavailable') }}</dd></div>
            <div><dt>{{ t('requestSize') }}</dt><dd>{{ selected.requestBytes === undefined ? t('unavailable') : `${selected.requestBytes} B` }}</dd></div>
            <div><dt>{{ t('started') }}</dt><dd>{{ formatTimestamp(selected.startedAt) }}</dd></div>
            <div><dt>{{ t('finished') }}</dt><dd>{{ formatTimestamp(selected.finishedAt ?? selected.abortedAt ?? selected.blockedAt) }}</dd></div>
            <div><dt>{{ t('duration') }}</dt><dd>{{ selected.durationMs === undefined ? t('inFlight') : `${selected.durationMs} ms` }}</dd></div>
            <div><dt>{{ t('responseSize') }}</dt><dd>{{ selected.responseBytes === undefined ? t('inFlight') : `${selected.responseBytes} B` }}</dd></div>
          </dl>
          <p v-else-if="detailsLoading" class="hint">{{ t('captureLoading') }}</p>
          <p v-else-if="detailsError" class="hint warning">{{ detailsError }}</p>
          <div v-else-if="detailTab === 'headers'" class="capture-grid"><section><b>{{ t('requestHeaders') }}</b><pre>{{ formatCapturedHeaders(requestDetails?.request.headers) }}</pre></section><section><b>{{ t('responseHeaders') }}</b><pre>{{ formatCapturedHeaders(requestDetails?.response.headers) }}</pre></section></div>
          <div v-else class="capture-grid"><section><b>{{ t('requestBody') }}</b><pre>{{ formatCapturedBody(requestDetails?.request) }}</pre></section><section><b>{{ t('responseBody') }}</b><pre>{{ formatCapturedBody(requestDetails?.response) }}</pre></section></div>
          <div class="actions">
            <p v-if="selected.pendingAt && !selected.releasedAt && !selected.blockedAt && !selected.abortedAt && !selected.finishedAt" class="pending-countdown" :class="{ expired: !selectedPendingActive }">{{ selectedPendingActive ? selectedPendingLabel : t('decisionExpired') }}</p>
            <button :disabled="!canOperate || !selectedPendingActive" class="allow" @click="decide('allow')">{{ t('allowPending') }}</button>
            <button :disabled="!canOperate || !selectedPendingActive" class="block" @click="decide('block')">{{ t('blockPending') }}</button>
            <button class="outline" :disabled="!canOperate" @click="blockPath">{{ t('blockFuture') }}</button>
          </div>
        </template>
        <template v-else-if="selectedRecords.length > 0">
          <p class="batch-summary">{{ t('selectedCount', { count: selectedRecords.length }) }}</p>
          <p class="hint">{{ t('selectedPending', { count: selectedPendingRecords.length }) }}</p>
          <p v-if="selectedPendingRecords.length > 0" class="pending-countdown">{{ batchPendingRemaining }}</p>
          <p v-if="selectedPendingRecords.length === 0" class="hint">{{ t('batchHistory') }}</p>
          <div class="actions">
            <button :disabled="!canOperate || selectedPendingRecords.length === 0" class="allow" @click="decideSelected('allow')">{{ t('allowSelected') }}</button>
            <button :disabled="!canOperate || selectedPendingRecords.length === 0" class="block" @click="decideSelected('block')">{{ t('blockSelected') }}</button>
            <button class="outline" :disabled="!canOperate" @click="blockSelectedPaths">{{ t('blockSelectedFuture') }}</button>
          </div>
        </template>
        <p v-else class="hint">{{ t('selectHint') }}</p>
      </article>

        <article class="panel rules-panel">
          <h2>{{ t('activeRules') }}</h2>
          <p v-if="rules.length === 0" class="hint">{{ t('noRules') }}</p>
        <ul v-else>
          <li v-for="rule in rules" :key="rule.id"><span><b>{{ t('block') }}</b> {{ rule.subject_id ? `${rule.subject_id} ·` : '' }} {{ rule.method || '*' }} {{ rule.path_prefix || rule.client_ip }}</span><button class="remove" :disabled="!canOperate" @click="removeRule(rule.id)">{{ t('remove') }}</button></li>
        </ul>
        </article>
      </aside>
    </div>

    <div v-if="configEditorOpen" class="config-overlay" @click.self="configEditorOpen = false">
      <section class="config-editor">
        <header><div><h2>{{ t('gatewayConfig') }}</h2><small>revision {{ configRevision }}</small></div><button class="sign-out" @click="configEditorOpen = false">{{ t('configClose') }}</button></header>
        <textarea v-model="configEditorText" spellcheck="false" aria-label="HTTPV gateway JSON configuration"></textarea>
        <div class="config-actions"><button class="outline" @click="validateEditorConfig">{{ t('configValidate') }}</button><button class="test-button" @click="applyEditorConfig">{{ t('configApply') }}</button></div>
        <p v-if="configEditorMessage" class="config-message" :class="{ error: configEditorError }">{{ configEditorMessage }}</p>
        <div class="revision-list"><h3>{{ t('configHistory') }}</h3><div v-for="revision in configRevisions" :key="revision.revision"><span><b>v{{ revision.revision }}</b>{{ formatTimestamp(revision.created_at) }} · {{ revision.source }}</span><button class="outline" :disabled="revision.revision === configRevision" @click="rollbackEditorConfig(revision.revision)">{{ t('configRollback') }}</button></div></div>
      </section>
    </div>
  </main>
</template>
