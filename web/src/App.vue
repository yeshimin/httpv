<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import TrafficCanvas, { type TrafficRecord } from './TrafficCanvas.vue'

type Match = { host: string; path_prefix: string; method: string }
type Gate = { enabled: boolean; timeout_ms: number; timeout_action: 'allow' | 'block' }
type Subject = { id: string; name: string; enabled: boolean; display: boolean; match: Match; gate: Gate }
type Rule = { id: string; action: string; path_prefix: string; method: string; client_ip: string; created_at: number }
type EventMessage = {
  phase: string; request_id: string; subject_id: string; timestamp_ms: number; method?: string; path?: string; client_ip?: string;
  status?: number; duration_ms?: number; response_bytes?: number; action?: string
}
type TrafficSummaryPath = { subject_id: string; method: string; path: string; count: number; blocked: number; pending: number }
type TrafficSummary = {
  type: 'traffic_summary'; window_start_ms: number; window_end_ms: number; total: number; started: number; responded: number;
  finished: number; pending: number; blocked: number; paths: TrafficSummaryPath[]
}
type NativePolicyStatus = { version: number; state: 'active' | 'pending' | 'unpublished' | 'disabled'; acks: { worker: string; version: number }[] }

type Locale = 'zh' | 'en'
const messages: Record<Locale, Record<string, string>> = {
  zh: {
    commandSurface: '流量指挥台', connecting: '连接中', live: '实时连接', closed: '连接断开',
    tracked: '{count} 条已跟踪 · {pending} 条待决', gateOn: '人工闸门：开启', gateOff: '人工闸门：关闭',
    sendCurrent: '发送当前请求', guidedDemo: '运行 2 秒引导演示', request: '请求', response: '响应',
    waiting: '等待人工决定', blocked: '已阻断', tapParticle: '点击流量粒子查看详情', liveState: '实时状态',
    gateActive: '人工闸门已开启。', gateInstruction: '下一个匹配请求将以黄色粒子暂停，直到你选择放行或阻断。',
    ready: '就绪——运行引导演示以生成一条可见请求。', waitingDecision: '正在等待你的决定——点击黄色粒子，然后选择放行或阻断。',
    flowing: '请求正在流向上游：{method} {path}', returning: '上游已响应——响应正在返回客户端。',
    completed: '已完成：{status}，耗时 {duration}ms。拖尾会短暂保留。', clientAborted: '客户端已断开，请求已经终止。', trafficControls: '流量控制',
    managedPath: '受监管路径', gateTimeout: '闸门超时', timeoutAction: '超时动作', block: '阻断', allow: '放行',
    regulateTraffic: '监管匹配流量', showEvents: '展示事件', render: '绘制 {value}ms', trail: '拖尾 {value}ms', test: '测试 {value}ms',
    selectedRequest: '选中请求', client: '客户端', status: '状态', duration: '耗时', responseSize: '响应大小',
    inFlight: '处理中', unavailable: '不可用', allowPending: '放行待决请求', blockPending: '阻断待决请求',
    blockFuture: '阻断后续匹配流量', selectHint: '点击流量粒子即可查看元数据并执行控制操作。',
    activeRules: '动态规则', noRules: '暂无动态阻断规则。', remove: '移除', language: '语言',
    pendingRemaining: '待人工决定，剩余 {seconds} 秒', pendingInfinite: '待人工决定，等待人工操作', batchRemaining: '待决倒计时：最早 {min} 秒，最晚 {max} 秒', decisionExpired: '该请求已不再等待人工决定。',
    allowedForwarding: '已放行——正在转发上游并等待响应。', dragSelect: '在画布空白处拖拽可框选多个请求', selectedCount: '已选 {count} 条请求',
    selectedPending: '其中 {count} 条仍待人工决定', batchHistory: '这些请求已经结束；只能为后续同类流量创建规则。', allowSelected: '放行选中待决请求', blockSelected: '阻断选中待决请求', blockSelectedFuture: '阻断选中路径的后续流量',
    burst: '并发 20 条', emergencyBlock: '紧急阻断全部待决', rendering: '绘制 {shown}/{total}',
    overloadMode: '高流量聚合', overloadSummary: '已聚合 {total} 个普通事件 · {rate}/秒', overloadHint: '待决和阻断请求仍逐条展示、可直接操作。', blockFlow: '阻断此流量',
    policyActive: '策略 C v{version} 已生效', policyPending: '策略 C v{version} 同步中', policyDisabled: '策略 C 未启用'
  },
  en: {
    commandSurface: 'Traffic command surface', connecting: 'connecting', live: 'live', closed: 'closed',
    tracked: '{count} tracked · {pending} waiting', gateOn: 'Manual gate: ON', gateOff: 'Manual gate: OFF',
    sendCurrent: 'Send current request', guidedDemo: 'Run guided 2s demo', request: 'request', response: 'response',
    waiting: 'waiting for manual decision', blocked: 'blocked', tapParticle: 'tap a particle for details', liveState: 'Live state',
    gateActive: 'Manual gate is active.', gateInstruction: 'The next matching request will pause as a yellow particle until you select Allow or Block.',
    ready: 'Ready — run the guided demo to create one visible request.', waitingDecision: 'Waiting for your decision — tap the yellow particle, then Allow or Block.',
    flowing: 'Request flowing to upstream: {method} {path}', returning: 'Upstream has responded — response is returning to the client.',
    completed: 'Completed: {status} in {duration}ms. Its trail remains visible briefly.', clientAborted: 'The client disconnected and the request has ended.', trafficControls: 'Traffic controls',
    managedPath: 'Managed path', gateTimeout: 'Gate timeout', timeoutAction: 'On timeout', block: 'Block', allow: 'Allow',
    regulateTraffic: 'Regulate matching traffic', showEvents: 'Show events', render: 'Render {value}ms', trail: 'Trail {value}ms', test: 'Test {value}ms',
    selectedRequest: 'Selected request', client: 'Client', status: 'Status', duration: 'Duration', responseSize: 'Response size',
    inFlight: 'in flight', unavailable: 'not available', allowPending: 'Allow pending', blockPending: 'Block pending',
    blockFuture: 'Block future matches', selectHint: 'Tap a request particle to inspect its metadata and apply a control action.',
    activeRules: 'Active dynamic rules', noRules: 'No dynamic block rules.', remove: 'Remove', language: 'Language',
    pendingRemaining: 'Waiting for a decision — {seconds}s remaining', pendingInfinite: 'Waiting for a manual decision', batchRemaining: 'Pending countdown: {min}s earliest, {max}s latest', decisionExpired: 'This request is no longer waiting for a decision.',
    allowedForwarding: 'Allowed — forwarding to the upstream and awaiting its response.', dragSelect: 'Drag across empty canvas space to box-select requests', selectedCount: '{count} requests selected',
    selectedPending: '{count} still waiting for a decision', batchHistory: 'These requests have completed; only future-match rules can be created.', allowSelected: 'Allow selected pending', blockSelected: 'Block selected pending', blockSelectedFuture: 'Block future traffic for selected paths',
    burst: 'Burst ×20', emergencyBlock: 'Emergency block all waiting', rendering: 'rendering {shown}/{total}',
    overloadMode: 'High-traffic aggregation', overloadSummary: '{total} ordinary events aggregated · {rate}/s', overloadHint: 'Waiting and blocked requests remain individually visible and actionable.', blockFlow: 'Block this flow',
    policyActive: 'Policy C v{version} active', policyPending: 'Policy C v{version} syncing', policyDisabled: 'Policy C disabled'
  }
}

const subject = ref<Subject>({
  id: 'demo-api', name: 'Demo API', enabled: true, display: true,
  match: { host: '', path_prefix: '/', method: '' },
  gate: { enabled: false, timeout_ms: 30000, timeout_action: 'block' }
})
const rules = ref<Rule[]>([])
const traffic = ref(new Map<string, TrafficRecord>())
const selectedID = ref<string>()
const selectedIds = ref<string[]>([])
const renderDelayMs = ref(0)
const trailMs = ref(3000)
const requestDelayMs = ref(1500)
const connectionState = ref<'connecting' | 'live' | 'closed'>('connecting')
const locale = ref<Locale>(localStorage.getItem('httpv-locale') === 'en' ? 'en' : 'zh')
const nowMs = ref(Date.now())
const burstBusy = ref(false)
const overloadSummary = ref<TrafficSummary>()
const policyStatus = ref<NativePolicyStatus>({ version: 0, state: 'unpublished', acks: [] })
const timeoutEventGraceMs = 2_000
let socket: WebSocket | undefined
let clockTimer: number | undefined
let policyTimer: number | undefined
let eventFrame: number | undefined
const eventQueue: EventMessage[] = []

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

const records = computed(() => Array.from(traffic.value.values()).sort((a, b) => a.startedAt - b.startedAt))
const selected = computed(() => selectedID.value ? traffic.value.get(selectedID.value) : undefined)
const selectedRecords = computed(() => selectedIds.value.map((id) => traffic.value.get(id)).filter((record): record is TrafficRecord => Boolean(record)))
const pendingCount = computed(() => records.value.filter(isPendingActive).length)
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
  if (event.phase === 'request_started') record.startedAt = event.timestamp_ms
  if (event.phase === 'request_pending') record.pendingAt = event.timestamp_ms
  if (event.phase === 'response_started') record.responseStartedAt = event.timestamp_ms
  if (event.phase === 'response_finished') {
    record.finishedAt = event.timestamp_ms
    record.status = event.status
    record.durationMs = event.duration_ms
    record.responseBytes = event.response_bytes
  }
  if (event.phase === 'request_blocked' || event.phase === 'request_timeout' || event.phase === 'request_decided') {
    record.action = event.action
    if (event.status) record.status = event.status
  }
  if ((event.phase === 'request_decided' || event.phase === 'request_timeout') && event.action === 'allow') {
    record.releasedAt = event.timestamp_ms
  }
  if (event.phase === 'request_aborted') {
    record.action = 'abort'
    record.abortedAt = event.timestamp_ms
    record.status = event.status ?? 499
    record.durationMs = event.duration_ms
  }
  if ((event.phase === 'request_decided' || event.phase === 'request_blocked' || event.phase === 'request_timeout') && event.action === 'block') {
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
    ?? (record.action === 'block' ? record.blockedAt : undefined)
    ?? record.abortedAt
  if (terminalAt === undefined) return undefined
  return terminalAt + renderDelayMs.value + 420 + Math.max(1_000, trailMs.value)
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
  const response = await fetch('/api/bootstrap')
  if (!response.ok) throw new Error('Unable to load control state')
  const data = await response.json()
  subject.value = data.config.subjects[0]
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
    const response = await fetch('/api/policy-status')
    if (response.ok) policyStatus.value = await response.json() as NativePolicyStatus
  } catch {
    // Policy status is advisory; a transient control-plane read must not affect traffic control.
  }
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
  const response = await fetch('/api/subjects/demo-api', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(subject.value)
  })
  if (!response.ok) alert('OpenResty has not accepted the configuration yet.')
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
  const response = await fetch(`/api/pending/${selected.value.requestId}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action })
  })
  if (!response.ok) alert(t('decisionExpired'))
}

function selectOne(record: TrafficRecord) {
  selectedID.value = record.requestId
  selectedIds.value = [record.requestId]
}

function selectMany(records: TrafficRecord[]) {
  const ids = [...new Set(records.map((record) => record.requestId))]
  selectedIds.value = ids
  selectedID.value = ids.length === 1 ? ids[0] : undefined
}

async function decideSelected(action: 'allow' | 'block') {
  const pending = selectedPendingRecords.value
  if (pending.length === 0) {
    alert(t('decisionExpired'))
    return
  }
  const response = await fetch('/api/pending/decide-batch', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ request_ids: pending.map((record) => record.requestId), action })
  })
  if (!response.ok) alert(t('decisionExpired'))
}

async function blockAllPending() {
  const response = await fetch('/api/pending/block-all', { method: 'POST' })
  if (!response.ok) alert(t('decisionExpired'))
}

async function blockSelectedPaths() {
  const unique = new Map(selectedRecords.value.map((record) => [`${record.method}:${record.path}`, record]))
  const created = await Promise.all([...unique.values()].map(async (record) => {
    const response = await fetch('/api/rules', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action: 'block', path_prefix: record.path, method: record.method, client_ip: '' })
    })
    return response.ok ? response.json() as Promise<Rule> : undefined
  }))
  rules.value.push(...created.filter((rule): rule is Rule => Boolean(rule)))
}

async function blockPath() {
  if (!selected.value) return
  const response = await fetch('/api/rules', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'block', path_prefix: selected.value.path, method: selected.value.method, client_ip: '' })
  })
  if (!response.ok) { alert('Unable to create the block rule.'); return }
  rules.value.push(await response.json())
}

async function blockAggregatePath(path: TrafficSummaryPath) {
  if (!path.path || path.path === 'other') return
  const response = await fetch('/api/rules', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action: 'block', path_prefix: path.path, method: path.method, client_ip: '' })
  })
  if (!response.ok) { alert('Unable to create the block rule.'); return }
  rules.value.push(await response.json())
}

async function removeRule(id: string) {
  const response = await fetch(`/api/rules/${id}`, { method: 'DELETE' })
  if (response.ok) rules.value = rules.value.filter((rule) => rule.id !== id)
}

async function sendTestRequest() {
  try {
    await fetch('/api/demo/burst', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ count: 1, delay_ms: requestDelayMs.value })
    })
  } catch {
    // The visual control flow is the subject of this prototype; an unavailable test generator is non-fatal.
  }
}

async function sendBurst() {
  if (burstBusy.value) return
  burstBusy.value = true
  try {
    await fetch('/api/demo/burst', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ count: 20, delay_ms: requestDelayMs.value })
    })
  } finally {
    window.setTimeout(() => { burstBusy.value = false }, 200)
  }
}

async function runGuidedDemo() {
  renderDelayMs.value = 0
  trailMs.value = 4000
  requestDelayMs.value = 2000
  selectedID.value = undefined
  await sendTestRequest()
}

onMounted(async () => {
  clockTimer = window.setInterval(() => {
    nowMs.value = Date.now()
    pruneExpiredRecords()
  }, 250)
  policyTimer = window.setInterval(refreshPolicyStatus, 1_000)
  try { await bootstrap() } catch (error) { console.error(error) }
  await refreshPolicyStatus()
  connect()
})
onBeforeUnmount(() => {
  socket?.close()
  if (clockTimer) window.clearInterval(clockTimer)
  if (policyTimer) window.clearInterval(policyTimer)
  if (eventFrame !== undefined) window.cancelAnimationFrame(eventFrame)
})
</script>

<template>
  <main class="app-shell">
    <header class="topbar">
      <p class="eyebrow">HTTPV / LOCAL PROTOTYPE</p>
      <div class="topbar-right">
        <div class="policy-state" :class="policyStatus.state">{{ policyLabel }}</div>
        <label class="language-picker"><span>{{ t('language') }}</span><select :value="locale" @change="onLocaleChange"><option value="zh">中文</option><option value="en">English</option></select></label>
        <div class="status" :class="connectionState"><span></span>{{ t(connectionState) }}</div>
      </div>
    </header>

    <div class="command-layout">
      <section class="canvas-panel" :class="{ 'has-overload': overloadActive && overloadSummary }">
        <div class="canvas-header">
          <div><strong>{{ subject.name }}</strong><span>{{ t('tracked', { count: records.length, pending: pendingCount }) }} · {{ t('rendering', { shown: animatedIds.length, total: records.length }) }}</span></div>
          <div class="header-actions">
            <button class="gate-toggle" :class="{ active: subject.gate.enabled }" @click="toggleGate">{{ subject.gate.enabled ? t('gateOn') : t('gateOff') }}</button>
            <button v-if="pendingCount > 0" class="emergency-button" @click="blockAllPending">{{ t('emergencyBlock') }}</button>
            <button class="outline compact" @click="sendTestRequest">{{ t('sendCurrent') }}</button>
            <button class="outline compact" :disabled="burstBusy" @click="sendBurst">{{ t('burst') }}</button>
            <button class="test-button" @click="runGuidedDemo">{{ t('guidedDemo') }}</button>
          </div>
        </div>
        <div v-if="overloadActive && overloadSummary" class="overload-strip">
          <div class="overload-copy"><b>{{ t('overloadMode') }}</b><span>{{ t('overloadSummary', { total: overloadSummary.total, rate: summaryRate(overloadSummary) }) }}</span><small>{{ t('overloadHint') }}</small></div>
          <div class="overload-paths"><button v-for="path in overloadSummary.paths" :key="`${path.subject_id}:${path.method}:${path.path}`" :disabled="!path.path || path.path === 'other'" @click="blockAggregatePath(path)"><span>{{ path.method || '*' }} {{ path.path || '*' }}</span><b>{{ path.count }}</b><em>{{ t('blockFlow') }}</em></button></div>
        </div>
        <TrafficCanvas :records="records" :animated-ids="animatedIds" :render-delay-ms="renderDelayMs" :trail-ms="trailMs" :selected-ids="selectedIds" @select="selectOne" @select-many="selectMany" />
        <div class="legend"><span class="legend-blue"></span>{{ t('request') }} <span class="legend-green"></span>{{ t('response') }} <span class="legend-yellow"></span>{{ t('waiting') }} <span class="legend-red"></span>{{ t('blocked') }} · {{ t('tapParticle') }} · {{ t('dragSelect') }}</div>
        <div class="flow-status" :class="{ waiting: pendingCount > 0 }"><b>{{ t('liveState') }}</b>{{ flowStatus }}</div>
        <div v-if="subject.gate.enabled" class="gate-banner"><b>{{ t('gateActive') }}</b>{{ t('gateInstruction') }}</div>
      </section>

      <aside class="command-rail">
        <article class="panel operating-panel">
          <h2>{{ t('trafficControls') }}</h2>
          <div class="field-row">
            <label><span>{{ t('managedPath') }}</span><input v-model="subject.match.path_prefix" @change="saveSubject" /></label>
            <label><span>{{ t('gateTimeout') }}</span><input v-model.number="subject.gate.timeout_ms" min="0" step="1000" type="number" @change="saveSubject" /></label>
            <label><span>{{ t('timeoutAction') }}</span><select v-model="subject.gate.timeout_action" @change="saveSubject"><option value="block">{{ t('block') }}</option><option value="allow">{{ t('allow') }}</option></select></label>
          </div>
          <div class="inline-switches">
            <label class="switch"><input v-model="subject.enabled" type="checkbox" @change="saveSubject" /><span>{{ t('regulateTraffic') }}</span></label>
            <label class="switch"><input v-model="subject.display" type="checkbox" @change="saveSubject" /><span>{{ t('showEvents') }}</span></label>
          </div>
          <div class="slider-row">
            <label><span>{{ t('render', { value: renderDelayMs }) }}</span><input v-model.number="renderDelayMs" min="0" max="10000" step="25" type="range" /></label>
            <label><span>{{ t('trail', { value: trailMs }) }}</span><input v-model.number="trailMs" min="100" max="10000" step="100" type="range" /></label>
            <label><span>{{ t('test', { value: requestDelayMs }) }}</span><input v-model.number="requestDelayMs" min="0" max="20000" step="50" type="range" /></label>
          </div>
        </article>

        <article class="panel detail-panel">
        <h2>{{ t('selectedRequest') }}</h2>
        <template v-if="selected">
          <dl>
            <div><dt>{{ t('request') }}</dt><dd>{{ selected.method }} {{ selected.path }}</dd></div>
            <div><dt>{{ t('client') }}</dt><dd>{{ selected.clientIp || t('unavailable') }}</dd></div>
            <div><dt>{{ t('status') }}</dt><dd>{{ selected.status ?? t('inFlight') }}</dd></div>
            <div><dt>{{ t('duration') }}</dt><dd>{{ selected.durationMs === undefined ? t('inFlight') : `${selected.durationMs}ms` }}</dd></div>
            <div><dt>{{ t('responseSize') }}</dt><dd>{{ selected.responseBytes === undefined ? t('inFlight') : `${selected.responseBytes} bytes` }}</dd></div>
          </dl>
          <div class="actions">
            <p v-if="selected.pendingAt && !selected.releasedAt && !selected.blockedAt && !selected.abortedAt && !selected.finishedAt" class="pending-countdown" :class="{ expired: !selectedPendingActive }">{{ selectedPendingActive ? selectedPendingLabel : t('decisionExpired') }}</p>
            <button :disabled="!selectedPendingActive" class="allow" @click="decide('allow')">{{ t('allowPending') }}</button>
            <button :disabled="!selectedPendingActive" class="block" @click="decide('block')">{{ t('blockPending') }}</button>
            <button class="outline" @click="blockPath">{{ t('blockFuture') }}</button>
          </div>
        </template>
        <template v-else-if="selectedRecords.length > 0">
          <p class="batch-summary">{{ t('selectedCount', { count: selectedRecords.length }) }}</p>
          <p class="hint">{{ t('selectedPending', { count: selectedPendingRecords.length }) }}</p>
          <p v-if="selectedPendingRecords.length > 0" class="pending-countdown">{{ batchPendingRemaining }}</p>
          <p v-if="selectedPendingRecords.length === 0" class="hint">{{ t('batchHistory') }}</p>
          <div class="actions">
            <button :disabled="selectedPendingRecords.length === 0" class="allow" @click="decideSelected('allow')">{{ t('allowSelected') }}</button>
            <button :disabled="selectedPendingRecords.length === 0" class="block" @click="decideSelected('block')">{{ t('blockSelected') }}</button>
            <button class="outline" @click="blockSelectedPaths">{{ t('blockSelectedFuture') }}</button>
          </div>
        </template>
        <p v-else class="hint">{{ t('selectHint') }}</p>
      </article>

        <article class="panel rules-panel">
          <h2>{{ t('activeRules') }}</h2>
          <p v-if="rules.length === 0" class="hint">{{ t('noRules') }}</p>
        <ul v-else>
          <li v-for="rule in rules" :key="rule.id"><span><b>{{ t('block') }}</b> {{ rule.method || '*' }} {{ rule.path_prefix || rule.client_ip }}</span><button class="remove" @click="removeRule(rule.id)">{{ t('remove') }}</button></li>
        </ul>
        </article>
      </aside>
    </div>
  </main>
</template>
