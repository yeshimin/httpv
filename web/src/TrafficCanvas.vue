<script setup lang="ts">
import { Application, Graphics, Particle, ParticleContainer, Rectangle, Texture } from 'pixi.js'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

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
  responseStartedAt?: number
  finishedAt?: number
  status?: number
  action?: string
  durationMs?: number
  responseBytes?: number
}

const props = defineProps<{
  records: TrafficRecord[]
  animatedIds: string[]
  renderDelayMs: number
  trailMs: number
  selectedIds: string[]
}>()

const emit = defineEmits<{ select: [record: TrafficRecord], selectMany: [records: TrafficRecord[]] }>()
const host = ref<HTMLDivElement | null>(null)

type Point = { x: number; y: number }
type DynamicParticle = { dot: Graphics; record: TrafficRecord }
type StaticParticle = { particle: Particle; record: TrafficRecord; point: Point; key: string }

let app: Application | undefined
let staticContainer: ParticleContainer | undefined
let resizeObserver: ResizeObserver | undefined
let selectionBox: Graphics | undefined
let dragStart: Point | undefined
let isDragging = false

const dynamicParticles = new Map<string, DynamicParticle>()
const staticParticles = new Map<string, StaticParticle>()
const retired = new Set<string>()

function colorFor(record: TrafficRecord): number {
  if (record.action === 'block' || (record.status ?? 0) >= 400) return 0xff5c70
  if (record.pendingAt && !record.releasedAt && !record.finishedAt) return 0xffc857
  if (record.responseStartedAt) return 0x63e6be
  return 0x75a7ff
}

function hashFraction(requestId: string, salt: string) {
  let hash = 0
  const input = `${requestId}:${salt}`
  for (let index = 0; index < input.length; index += 1) hash = ((hash << 5) - hash + input.charCodeAt(index)) | 0
  return (Math.abs(hash) % 10_000) / 10_000
}

function requestPoints(requestId: string, width: number, height: number) {
  const top = 48
  const bottom = Math.max(top + 1, height - 34)
  const range = bottom - top
  const y = top + range * (0.04 + hashFraction(requestId, 'lane') * 0.92)
  return {
    client: { x: width * 0.1, y },
    gateway: { x: width * 0.48, y },
    upstream: { x: width * 0.86, y }
  }
}

function interpolate(from: Point, to: Point, progress: number) {
  return { x: from.x + (to.x - from.x) * progress, y: from.y + (to.y - from.y) * progress }
}

function staticPosition(record: TrafficRecord) {
  if (!app) return { x: 0, y: 0 }
  const points = requestPoints(record.requestId, app.renderer.width, app.renderer.height)
  if (record.pendingAt && !record.releasedAt && !record.blockedAt && !record.finishedAt) return points.gateway
  if (record.action === 'block' || record.responseStartedAt) return points.gateway
  if (record.finishedAt) return points.client
  return points.upstream
}

function staticKey(record: TrafficRecord, selected: boolean) {
  return `${record.pendingAt}:${record.releasedAt}:${record.blockedAt}:${record.responseStartedAt}:${record.finishedAt}:${record.action}:${record.status}:${selected}`
}

function updateStaticParticle(entry: StaticParticle, selected: boolean) {
  const point = staticPosition(entry.record)
  entry.point = point
  entry.particle.x = point.x
  entry.particle.y = point.y
  entry.particle.color = selected ? 0xf5fbff : colorFor(entry.record)
  entry.particle.scaleX = selected ? 8 : 6
  entry.particle.scaleY = selected ? 8 : 6
  entry.key = staticKey(entry.record, selected)
}

function addStaticParticle(record: TrafficRecord, selected: boolean) {
  if (!staticContainer) return
  const point = staticPosition(record)
  const particle = new Particle({
    texture: Texture.WHITE,
    x: point.x,
    y: point.y,
    anchorX: 0.5,
    anchorY: 0.5,
    scaleX: selected ? 8 : 6,
    scaleY: selected ? 8 : 6,
    tint: selected ? 0xf5fbff : colorFor(record)
  })
  staticContainer.addParticle(particle)
  staticParticles.set(record.requestId, { particle, record, point, key: staticKey(record, selected) })
}

function removeStaticParticle(id: string) {
  const entry = staticParticles.get(id)
  if (!entry || !staticContainer) return
  staticContainer.removeParticle(entry.particle)
  staticParticles.delete(id)
}

function addDynamicParticle(record: TrafficRecord) {
  if (!app) return
  const dot = new Graphics()
  dot.eventMode = 'none'
  app.stage.addChild(dot)
  dynamicParticles.set(record.requestId, { dot, record })
}

function removeDynamicParticle(id: string) {
  const entry = dynamicParticles.get(id)
  if (!entry || !app) return
  app.stage.removeChild(entry.dot)
  entry.dot.destroy()
  dynamicParticles.delete(id)
}

function syncParticles() {
  if (!app || !staticContainer) return
  const recordByID = new Map(props.records.map((record) => [record.requestId, record]))
  const animated = new Set(props.animatedIds)
  const selected = new Set(props.selectedIds)
  let staticDirty = false

  for (const id of dynamicParticles.keys()) if (!recordByID.has(id)) removeDynamicParticle(id)
  for (const id of staticParticles.keys()) {
    if (!recordByID.has(id)) {
      removeStaticParticle(id)
      staticDirty = true
    }
  }

  for (const record of props.records) {
    if (retired.has(record.requestId)) continue
    if (animated.has(record.requestId)) {
      if (staticParticles.has(record.requestId)) {
        removeStaticParticle(record.requestId)
        staticDirty = true
      }
      const entry = dynamicParticles.get(record.requestId)
      if (entry) entry.record = record
      else addDynamicParticle(record)
    } else {
      if (dynamicParticles.has(record.requestId)) removeDynamicParticle(record.requestId)
      const entry = staticParticles.get(record.requestId)
      if (entry) {
        entry.record = record
        const key = staticKey(record, selected.has(record.requestId))
        if (entry.key !== key) {
          updateStaticParticle(entry, selected.has(record.requestId))
          staticDirty = true
        }
      } else {
        addStaticParticle(record, selected.has(record.requestId))
        staticDirty = true
      }
    }
  }
  if (staticDirty) staticContainer.update()
}

function pointFromEvent(event: PointerEvent) {
  if (!app || !host.value) return undefined
  const bounds = host.value.getBoundingClientRect()
  return {
    x: (event.clientX - bounds.left) * (app.renderer.width / bounds.width),
    y: (event.clientY - bounds.top) * (app.renderer.height / bounds.height)
  }
}

function drawSelection(start: Point, end: Point) {
  if (!app) return
  if (!selectionBox) {
    selectionBox = new Graphics()
    selectionBox.eventMode = 'none'
    app.stage.addChild(selectionBox)
  }
  const left = Math.min(start.x, end.x)
  const top = Math.min(start.y, end.y)
  const width = Math.abs(start.x - end.x)
  const height = Math.abs(start.y - end.y)
  selectionBox.clear()
  selectionBox.rect(left, top, width, height).fill({ color: 0x75a7ff, alpha: 0.13 })
  selectionBox.rect(left, top, width, height).stroke({ color: 0xaac7ff, width: 1.5, alpha: 0.9 })
}

function selectableRecords() {
  return [
    ...[...dynamicParticles.values()].filter(({ dot }) => dot.visible && dot.alpha > 0).map(({ dot, record }) => ({ record, point: { x: dot.x, y: dot.y } })),
    ...[...staticParticles.values()].map(({ record, point }) => ({ record, point }))
  ]
}

function onPointerDown(event: PointerEvent) {
  if (event.button !== 0) return
  dragStart = pointFromEvent(event)
  isDragging = false
  if (dragStart && host.value) host.value.setPointerCapture(event.pointerId)
}

function onPointerMove(event: PointerEvent) {
  if (!dragStart) return
  const point = pointFromEvent(event)
  if (!point) return
  if (!isDragging && Math.hypot(point.x - dragStart.x, point.y - dragStart.y) > 6) isDragging = true
  if (isDragging) drawSelection(dragStart, point)
}

function onPointerUp(event: PointerEvent) {
  if (!dragStart) return
  const end = pointFromEvent(event)
  const records = selectableRecords()
  if (isDragging && end) {
    const left = Math.min(dragStart.x, end.x)
    const right = Math.max(dragStart.x, end.x)
    const top = Math.min(dragStart.y, end.y)
    const bottom = Math.max(dragStart.y, end.y)
    emit('selectMany', records.filter(({ point }) => point.x >= left && point.x <= right && point.y >= top && point.y <= bottom).map(({ record }) => record))
  } else if (end) {
    const hit = records
      .map((entry) => ({ ...entry, distance: Math.hypot(entry.point.x - end.x, entry.point.y - end.y) }))
      .filter(({ distance }) => distance <= 16)
      .sort((a, b) => a.distance - b.distance)[0]
    if (hit) emit('select', hit.record)
    else emit('selectMany', [])
  }
  selectionBox?.clear()
  dragStart = undefined
  isDragging = false
  if (host.value?.hasPointerCapture(event.pointerId)) host.value.releasePointerCapture(event.pointerId)
}

function drawLane() {
  if (!app) return
  const lane = new Graphics()
  const width = app.renderer.width
  const height = app.renderer.height
  const top = 42
  const bottom = height - 30
  const clientX = width * 0.1
  const gatewayX = width * 0.48
  const upstreamX = width * 0.86
  lane.rect(gatewayX - 48, top, 96, bottom - top).fill({ color: 0x75a7ff, alpha: 0.075 })
  lane.rect(gatewayX - 48, top, 96, bottom - top).stroke({ color: 0x5f87a8, width: 1.5, alpha: 0.8 })
  lane.moveTo(clientX, top).lineTo(clientX, bottom).stroke({ color: 0x5f87a8, width: 1.5, alpha: 0.72 })
  lane.moveTo(upstreamX, top).lineTo(upstreamX, bottom).stroke({ color: 0x5f87a8, width: 1.5, alpha: 0.72 })
  app.stage.addChildAt(lane, 0)
}

function createStaticLayer() {
  if (!app) return
  staticContainer = new ParticleContainer({
    boundsArea: new Rectangle(0, 0, app.renderer.width, app.renderer.height),
    dynamicProperties: { position: false, vertex: false, rotation: false, color: false }
  })
  app.stage.addChild(staticContainer)
}

function render() {
  if (!app || !staticContainer) return
  const ingressDuration = 220
  const forwardDuration = 220
  const responseDuration = 420
  const now = Date.now()
  const width = app.renderer.width
  const height = app.renderer.height
  const selectedIds = new Set(props.selectedIds)

  let staticDirty = false
  for (const [id, entry] of staticParticles) {
    const finishedAt = entry.record.finishedAt === undefined ? undefined : entry.record.finishedAt + props.renderDelayMs
    if (finishedAt !== undefined && now > finishedAt + Math.max(1_000, props.trailMs)) {
      removeStaticParticle(id)
      retired.add(id)
      staticDirty = true
    }
  }
  if (staticDirty) staticContainer.update()

  for (const [id, particle] of dynamicParticles) {
    const record = particle.record
    const points = requestPoints(id, width, height)
    const start = record.startedAt + props.renderDelayMs
    if (now < start) {
      particle.dot.visible = false
      continue
    }
    particle.dot.visible = true
    const ingressEnd = start + ingressDuration
    const waitingAtGate = Boolean(record.pendingAt && !record.releasedAt && !record.blockedAt && !record.responseStartedAt)
    const forwardStart = Math.max(ingressEnd, (record.releasedAt ?? start) + props.renderDelayMs)
    const forwardEnd = forwardStart + forwardDuration
    const blockedAtGateway = record.action === 'block'
    const actualResponseStart = record.responseStartedAt === undefined
      ? (record.blockedAt === undefined ? undefined : record.blockedAt + props.renderDelayMs)
      : record.responseStartedAt + props.renderDelayMs
    const responseOrigin = blockedAtGateway ? points.gateway : points.upstream
    const visualResponseStart = actualResponseStart === undefined ? undefined : Math.max(actualResponseStart, blockedAtGateway ? ingressEnd : forwardEnd)
    const responseEnd = visualResponseStart === undefined ? undefined : visualResponseStart + responseDuration
    const trailEnd = responseEnd === undefined ? undefined : responseEnd + props.trailMs
    if (record.finishedAt && trailEnd !== undefined && now > trailEnd) {
      removeDynamicParticle(id)
      retired.add(id)
      continue
    }

    let position = points.gateway
    let alpha = 1
    let pulse = 0
    if (now < ingressEnd) position = interpolate(points.client, points.gateway, (now - start) / ingressDuration)
    else if (waitingAtGate) {
      position = points.gateway
      pulse = 1
    } else if (visualResponseStart === undefined || now < visualResponseStart) {
      if (now < forwardStart) position = points.gateway
      else if (now < forwardEnd) position = interpolate(points.gateway, points.upstream, (now - forwardStart) / forwardDuration)
      else {
        position = points.upstream
        pulse = 1
      }
    } else {
      const progress = Math.min(1, (now - visualResponseStart) / responseDuration)
      if (blockedAtGateway) position = interpolate(points.gateway, points.client, progress)
      else if (progress < 0.5) position = interpolate(responseOrigin, points.gateway, progress * 2)
      else position = interpolate(points.gateway, points.client, (progress - 0.5) * 2)
      if (responseEnd !== undefined && record.finishedAt && now > responseEnd) alpha = Math.max(0, 1 - (now - responseEnd) / Math.max(1, props.trailMs))
    }

    const color = colorFor(record)
    particle.dot.clear()
    particle.dot.moveTo(points.client.x - position.x, points.client.y - position.y)
    particle.dot.lineTo(points.gateway.x - position.x, points.gateway.y - position.y)
    particle.dot.lineTo(points.upstream.x - position.x, points.upstream.y - position.y)
    particle.dot.stroke({ color, width: 1, alpha: alpha * 0.16 })
    particle.dot.circle(0, 0, 11 + pulse * (4 + Math.sin(now / 140) * 2)).fill({ color, alpha: alpha * (pulse > 0 ? 0.2 : 0.12) })
    particle.dot.circle(0, 0, 5).fill({ color, alpha })
    if (selectedIds.has(id)) particle.dot.circle(0, 0, 10).stroke({ color: 0xf5fbff, width: 2, alpha: 0.95 })
    particle.dot.x = position.x
    particle.dot.y = position.y
    particle.dot.alpha = alpha
  }
}

function resetLayers() {
  if (!app) return
  while (app.stage.children.length > 0) app.stage.removeChildAt(0).destroy()
  dynamicParticles.clear()
  staticParticles.clear()
  retired.clear()
  staticContainer = undefined
  selectionBox = undefined
  drawLane()
  createStaticLayer()
  syncParticles()
}

onMounted(async () => {
  if (!host.value) return
  app = new Application()
  await app.init({
    width: host.value.clientWidth,
    height: host.value.clientHeight,
    backgroundAlpha: 0,
    preference: 'webgl',
    preferWebGLVersion: 2,
    powerPreference: 'high-performance',
    antialias: false,
    resolution: Math.min(window.devicePixelRatio || 1, 1.5),
    autoDensity: true
  })
  host.value.appendChild(app.canvas)
  host.value.addEventListener('pointerdown', onPointerDown)
  host.value.addEventListener('pointermove', onPointerMove)
  host.value.addEventListener('pointerup', onPointerUp)
  host.value.addEventListener('pointercancel', onPointerUp)
  resetLayers()
  app.ticker.add(render)
  resizeObserver = new ResizeObserver(() => {
    if (!app || !host.value) return
    app.renderer.resize(host.value.clientWidth, host.value.clientHeight)
    resetLayers()
  })
  resizeObserver.observe(host.value)
})

watch(() => [props.records, props.animatedIds, props.selectedIds], syncParticles, { deep: true })

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  host.value?.removeEventListener('pointerdown', onPointerDown)
  host.value?.removeEventListener('pointermove', onPointerMove)
  host.value?.removeEventListener('pointerup', onPointerUp)
  host.value?.removeEventListener('pointercancel', onPointerUp)
  app?.destroy(true, { children: true })
})
</script>

<template>
  <div ref="host" class="traffic-canvas" aria-label="Realtime request traffic canvas">
    <div class="lane-label label-client">Client</div>
    <div class="lane-label label-gateway">OpenResty</div>
    <div class="lane-label label-upstream">Upstream</div>
  </div>
</template>

<style scoped>
.traffic-canvas { position: relative; width: 100%; height: 100%; min-height: 330px; overflow: hidden; }
.traffic-canvas :deep(canvas) { position: absolute; inset: 0; width: 100%; height: 100%; z-index: 0; }
.lane-label { position: absolute; z-index: 1; top: 12px; transform: translateX(-50%); color: #b9d2e4; font-size: 11px; font-weight: 700; letter-spacing: .08em; text-transform: uppercase; pointer-events: none; }
.label-client { left: 10%; }
.label-gateway { left: 48%; }
.label-upstream { left: 86%; }
</style>
