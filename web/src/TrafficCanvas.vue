<script setup lang="ts">
import { Application, Graphics, Particle, ParticleContainer, Rectangle, Texture } from 'pixi.js'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { TrafficRecord, TrafficRiskLevel } from './traffic-lifecycle'

export type { TrafficRecord, TrafficRiskLevel } from './traffic-lifecycle'

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
type DynamicParticle = { record: TrafficRecord; point: Point; visible: boolean; alpha: number; lastPosition?: Point; lastDirection: -1 | 0 | 1; tailDirection: -1 | 1; tailLength: number; tailStoppedAt?: number; effectColor: number; effectKey: string }
type StaticParticle = { particle: Particle; record: TrafficRecord; point: Point; key: string }
type GatewayQueueAssignment = { lane: number; slot: number }

let app: Application | undefined
let staticContainer: ParticleContainer | undefined
let resizeObserver: ResizeObserver | undefined
let selectionBox: Graphics | undefined
let markerTexture: Texture | undefined
let residualCanvas: HTMLCanvasElement | undefined
let residualContext: CanvasRenderingContext2D | undefined
let lastResidualFrame = 0
let residualInkUntil = 0
let motionCanvas: HTMLCanvasElement | undefined
let motionContext: CanvasRenderingContext2D | undefined
const cometSprites = new Map<string, HTMLCanvasElement>()
const colorStyles = new Map<number, string>()
let dragStart: Point | undefined
let isDragging = false

const dynamicParticles = new Map<string, DynamicParticle>()
const staticParticles = new Map<string, StaticParticle>()
const retired = new Set<string>()
const gatewayQueueAssignments = new Map<string, GatewayQueueAssignment>()
const visualStartTimes = new Map<string, number>()
const visualReleaseTimes = new Map<string, number>()
const visualResponseTimes = new Map<string, number>()
const visualAbortTimes = new Map<string, number>()

const gatewayQueueSlots = 14
const gatewayQueueGap = 5
const gatewayQueueRightOffset = 33.5
const gatewayLaneHeight = 5

const riskPalettes: Record<TrafficRiskLevel, readonly number[]> = {
  unknown: [0x7890a2, 0x8aa3b5, 0x6f8da1],
  normal: [0x67e8f9, 0x38bdf8, 0x60a5fa, 0x818cf8, 0xa78bfa, 0xc084fc, 0x2dd4bf, 0x22d3ee, 0x7dd3fc, 0x93c5fd],
  trusted: [0x6ee7b7, 0x4ade80, 0x34d399, 0x22c55e],
  suspicious: [0xfacc15, 0xfbbf24, 0xfb923c, 0xf59e0b],
  dangerous: [0xfb7185, 0xf43f5e, 0xef4444, 0xe11d48]
}

function effectColorFor(record: TrafficRecord): number {
  const palette = riskPalettes[record.riskLevel ?? 'normal']
  return palette[Math.min(palette.length - 1, Math.floor(hashFraction(record.requestId, 'effect-color') * palette.length))]
}

function effectColorKey(record: TrafficRecord) {
  return `${record.requestId}:${record.riskLevel ?? 'normal'}`
}

function dotColorFor(record: TrafficRecord): number {
  if (record.blockedAt !== undefined || record.action === 'block' || record.action === 'reject' || record.action === 'abort') return 0xff4d68
  if (record.pendingAt && !record.releasedAt && !record.finishedAt) return 0xffc857
  return effectColorFor(record)
}

function terminalAt(record: TrafficRecord) {
  if (record.finishedAt !== undefined) return record.finishedAt
  if (record.abortedAt !== undefined) return record.abortedAt
  if (record.blockedAt !== undefined) return record.blockedAt
  return undefined
}

function hashFraction(requestId: string, salt: string) {
  let hash = 0
  const input = `${requestId}:${salt}`
  for (let index = 0; index < input.length; index += 1) hash = ((hash << 5) - hash + input.charCodeAt(index)) | 0
  return (Math.abs(hash) % 10_000) / 10_000
}

function requestPoints(requestId: string, width: number, height: number) {
  const top = 34
  const bottom = Math.max(top + 1, height - 18)
  const range = bottom - top
  const y = top + range * (0.04 + hashFraction(requestId, 'lane') * 0.92)
  return {
    client: { x: width * 0.05, y },
    gateway: { x: width * 0.5, y },
    upstream: { x: width * 0.95, y }
  }
}

function waitingAtGateway(record: TrafficRecord) {
  return record.pendingAt !== undefined
    && record.releasedAt === undefined
    && record.blockedAt === undefined
    && record.abortedAt === undefined
    && record.responseStartedAt === undefined
    && record.finishedAt === undefined
}

function gatewayLane(record: TrafficRecord, width: number, height: number) {
  return Math.round(requestPoints(record.requestId, width, height).gateway.y / gatewayLaneHeight)
}

function syncGatewayQueueAssignments(records: TrafficRecord[]) {
  if (!app) return
  const recordIDs = new Set(records.map((record) => record.requestId))
  for (const id of gatewayQueueAssignments.keys()) {
    if (!recordIDs.has(id)) gatewayQueueAssignments.delete(id)
  }

  const waiting = records.filter(waitingAtGateway).sort((left, right) => (left.pendingAt ?? left.startedAt) - (right.pendingAt ?? right.startedAt))
  const usedByLane = new Map<number, Set<number>>()
  for (const record of waiting) {
    const assignment = gatewayQueueAssignments.get(record.requestId)
    if (!assignment) continue
    const used = usedByLane.get(assignment.lane) ?? new Set<number>()
    used.add(assignment.slot)
    usedByLane.set(assignment.lane, used)
  }

  for (const record of waiting) {
    if (gatewayQueueAssignments.has(record.requestId)) continue
    const lane = gatewayLane(record, app.renderer.width, app.renderer.height)
    const used = usedByLane.get(lane) ?? new Set<number>()
    let slot = 0
    while (slot < gatewayQueueSlots - 1 && used.has(slot)) slot += 1
    gatewayQueueAssignments.set(record.requestId, { lane, slot })
    used.add(slot)
    usedByLane.set(lane, used)
  }
}

function gatewayQueuePoint(record: TrafficRecord, point: Point) {
  const assignment = gatewayQueueAssignments.get(record.requestId)
  if (!assignment) return point
  return {
    x: point.x + gatewayQueueRightOffset - assignment.slot * gatewayQueueGap,
    y: assignment.lane * gatewayLaneHeight
  }
}

function interpolate(from: Point, to: Point, progress: number) {
  return { x: from.x + (to.x - from.x) * progress, y: from.y + (to.y - from.y) * progress }
}

function colorStyle(color: number) {
  const cached = colorStyles.get(color)
  if (cached) return cached
  const style = `#${color.toString(16).padStart(6, '0')}`
  colorStyles.set(color, style)
  return style
}

function cometSprite(color: number, direction: -1 | 1) {
  const key = `${color}:${direction}`
  const cached = cometSprites.get(key)
  if (cached) return cached
  const canvas = document.createElement('canvas')
  canvas.width = 256
  canvas.height = 16
  const context = canvas.getContext('2d')
  if (!context) return canvas
  const image = context.createImageData(canvas.width, canvas.height)
  const center = (canvas.height - 1) / 2
  const red = (color >> 16) & 0xff
  const green = (color >> 8) & 0xff
  const blue = color & 0xff
  for (let x = 0; x < canvas.width; x += 1) {
    const progress = direction === 1 ? x / (canvas.width - 1) : 1 - x / (canvas.width - 1)
    const longitudinal = Math.pow(progress, 1.35)
    const halfWidth = 0.2 + progress * 6.8
    for (let y = 0; y < canvas.height; y += 1) {
      const edge = Math.max(0, 1 - Math.abs(y - center) / halfWidth)
      const alpha = Math.round(255 * longitudinal * Math.pow(edge, 0.7) * 0.86)
      const offset = (y * canvas.width + x) * 4
      image.data[offset] = red
      image.data[offset + 1] = green
      image.data[offset + 2] = blue
      image.data[offset + 3] = alpha
    }
  }
  context.putImageData(image, 0, 0)
  cometSprites.set(key, canvas)
  return canvas
}

function resizeMotionLayer() {
  if (!motionCanvas || !residualCanvas || !host.value) return
  const resolution = Math.min(window.devicePixelRatio || 1, 1.5)
  residualCanvas.width = Math.max(1, Math.round(host.value.clientWidth * resolution))
  residualCanvas.height = Math.max(1, Math.round(host.value.clientHeight * resolution))
  residualContext = residualCanvas.getContext('2d', { alpha: true }) ?? undefined
  residualContext?.setTransform(resolution, 0, 0, resolution, 0, 0)
  motionCanvas.width = Math.max(1, Math.round(host.value.clientWidth * resolution))
  motionCanvas.height = Math.max(1, Math.round(host.value.clientHeight * resolution))
  motionContext = motionCanvas.getContext('2d', { alpha: true }) ?? undefined
  motionContext?.setTransform(resolution, 0, 0, resolution, 0, 0)
  lastResidualFrame = 0
  residualInkUntil = 0
}

function beginMotionFrame(now: number) {
  if (!motionContext || !residualContext || !host.value) return
  motionContext.clearRect(0, 0, host.value.clientWidth, host.value.clientHeight)
  if (lastResidualFrame === 0) {
    lastResidualFrame = now
    return
  }
  if (now > residualInkUntil) {
    residualContext.clearRect(0, 0, host.value.clientWidth, host.value.clientHeight)
    lastResidualFrame = now
    return
  }
  const elapsed = Math.min(64, Math.max(0, now - lastResidualFrame))
  const fadeDuration = Math.max(100, props.trailMs)
  const fadeAlpha = 1 - Math.exp(-7.5 * elapsed / fadeDuration)
  residualContext.save()
  residualContext.globalCompositeOperation = 'destination-out'
  residualContext.fillStyle = `rgba(0, 0, 0, ${fadeAlpha})`
  residualContext.fillRect(0, 0, host.value.clientWidth, host.value.clientHeight)
  residualContext.restore()
  lastResidualFrame = now
}

function drawDynamicParticle(particle: DynamicParticle, position: Point, direction: -1 | 0 | 1, now: number, visualAlpha: number, dotColor: number, selected: boolean, pulseScale: number) {
  const maximumLength = Math.max(48, Math.min(110, (host.value?.clientWidth ?? 1) * 0.09))
  const continues = direction !== 0 && direction === particle.lastDirection && particle.lastPosition !== undefined
  if (continues && residualContext) {
    const previous = particle.lastPosition!
    if (Math.hypot(position.x - previous.x, position.y - previous.y) > 0.25) {
      residualContext.globalCompositeOperation = 'source-over'
      residualContext.globalAlpha = visualAlpha * 0.24
      residualContext.strokeStyle = colorStyle(particle.effectColor)
      residualContext.lineWidth = 0.8
      residualContext.lineCap = 'butt'
      residualContext.beginPath()
      residualContext.moveTo(previous.x, previous.y)
      residualContext.lineTo(position.x, position.y)
      residualContext.stroke()
      residualContext.globalAlpha = 1
      residualInkUntil = now + Math.max(100, props.trailMs)
    }
  }
  if (direction !== 0) {
    if (!continues) particle.tailLength = 0
    else particle.tailLength = Math.min(maximumLength, particle.tailLength + Math.hypot(
      position.x - particle.lastPosition!.x,
      position.y - particle.lastPosition!.y
    ))
    particle.tailDirection = direction
    particle.tailStoppedAt = undefined
  } else if (particle.lastDirection !== 0 && particle.tailLength > 0) {
    particle.tailStoppedAt = now
  }

  const lingerDuration = Math.max(100, props.trailMs)
  const lingerAlpha = particle.tailStoppedAt === undefined
    ? (direction === 0 ? 0 : 1)
    : Math.max(0, 1 - (now - particle.tailStoppedAt) / lingerDuration)
  particle.point = { ...position }
  particle.alpha = visualAlpha
  particle.visible = true

  if (motionContext && particle.tailLength > 1 && lingerAlpha > 0) {
    motionContext.globalAlpha = visualAlpha * lingerAlpha
    const tailX = particle.tailDirection === 1 ? position.x - particle.tailLength : position.x
    motionContext.drawImage(cometSprite(particle.effectColor, particle.tailDirection), tailX, position.y - 4.4, particle.tailLength, 8.8)
  }
  if (motionContext) {
    motionContext.globalAlpha = visualAlpha
    motionContext.fillStyle = colorStyle(selected ? 0xf5fbff : dotColor)
    motionContext.beginPath()
    motionContext.arc(position.x, position.y, (selected ? 3.6 : 2.5) + pulseScale, 0, Math.PI * 2)
    motionContext.fill()
    motionContext.globalAlpha = 1
  }
  particle.lastPosition = { ...position }
  particle.lastDirection = direction
}

function staticPosition(record: TrafficRecord) {
  if (!app) return { x: 0, y: 0 }
  const points = requestPoints(record.requestId, app.renderer.width, app.renderer.height)
  const gateway = gatewayQueuePoint(record, points.gateway)
  if (waitingAtGateway(record)) return gateway
  if (record.abortedAt) return gateway
  if (record.blockedAt || record.responseStartedAt) return gateway
  if (record.finishedAt) return points.client
  return points.upstream
}

function staticKey(record: TrafficRecord, selected: boolean) {
  return `${record.pendingAt}:${record.releasedAt}:${record.blockedAt}:${record.abortedAt}:${record.responseStartedAt}:${record.finishedAt}:${record.action}:${record.status}:${record.riskLevel}:${record.riskScore}:${gatewayQueueAssignments.get(record.requestId)?.slot}:${selected}`
}

function updateStaticParticle(entry: StaticParticle, selected: boolean) {
  const point = staticPosition(entry.record)
  entry.point = point
  entry.particle.x = point.x
  entry.particle.y = point.y
  entry.particle.color = selected ? 0xf5fbff : dotColorFor(entry.record)
  entry.particle.scaleX = selected ? 0.9 : 0.62
  entry.particle.scaleY = selected ? 0.9 : 0.62
  entry.key = staticKey(entry.record, selected)
}

function addStaticParticle(record: TrafficRecord, selected: boolean) {
  if (!staticContainer) return
  const point = staticPosition(record)
  const particle = new Particle({
    texture: markerTexture ?? Texture.WHITE,
    x: point.x,
    y: point.y,
    anchorX: 0.5,
    anchorY: 0.5,
    scaleX: selected ? 0.9 : 0.62,
    scaleY: selected ? 0.9 : 0.62,
    tint: selected ? 0xf5fbff : dotColorFor(record)
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
  const effectColor = effectColorFor(record)
  dynamicParticles.set(record.requestId, {
    record,
    point: { x: 0, y: 0 },
    visible: false,
    alpha: 0,
    lastDirection: 0,
    tailDirection: 1,
    tailLength: 0,
    effectColor,
    effectKey: effectColorKey(record)
  })
}

function removeDynamicParticle(id: string) {
  dynamicParticles.delete(id)
}

function syncParticles() {
  if (!app || !staticContainer) return
  const recordByID = new Map(props.records.map((record) => [record.requestId, record]))
  for (const timingMap of [visualStartTimes, visualReleaseTimes, visualResponseTimes, visualAbortTimes]) {
    for (const id of timingMap.keys()) if (!recordByID.has(id)) timingMap.delete(id)
  }
  const observedAt = Date.now()
  for (const record of props.records) {
    if (!visualStartTimes.has(record.requestId)) {
      visualStartTimes.set(record.requestId, Math.max(record.startedAt + props.renderDelayMs, observedAt))
    }
    if (record.releasedAt !== undefined && !visualReleaseTimes.has(record.requestId)) {
      visualReleaseTimes.set(record.requestId, Math.max(record.releasedAt + props.renderDelayMs, observedAt))
    }
    const responseAt = record.blockedAt ?? record.responseStartedAt
    if (responseAt !== undefined && !visualResponseTimes.has(record.requestId)) {
      visualResponseTimes.set(record.requestId, Math.max(responseAt + props.renderDelayMs, observedAt))
    }
    if (record.abortedAt !== undefined && !visualAbortTimes.has(record.requestId)) {
      visualAbortTimes.set(record.requestId, Math.max(record.abortedAt + props.renderDelayMs, observedAt))
    }
  }
  syncGatewayQueueAssignments(props.records)
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
      if (entry) {
        entry.record = record
        const key = effectColorKey(record)
        if (entry.effectKey !== key) {
          entry.effectKey = key
          entry.effectColor = effectColorFor(record)
        }
      }
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
    ...[...dynamicParticles.values()].filter(({ visible, alpha }) => visible && alpha > 0).map(({ point, record }) => ({ record, point })),
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
  const clientX = width * 0.05
  const gatewayX = width * 0.5
  const upstreamX = width * 0.95
  lane.rect(gatewayX - 36, top, 72, bottom - top).fill({ color: 0x75a7ff, alpha: 0.045 })
  lane.rect(gatewayX - 36, top, 72, bottom - top).stroke({ color: 0x5f87a8, width: 1, alpha: 0.72 })
  lane.moveTo(clientX, top).lineTo(clientX, bottom).stroke({ color: 0x5f87a8, width: 1, alpha: 0.6 })
  lane.moveTo(upstreamX, top).lineTo(upstreamX, bottom).stroke({ color: 0x5f87a8, width: 1, alpha: 0.6 })
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
  if (!app || !staticContainer || !motionContext) return
  const ingressDuration = 220
  const forwardDuration = 220
  const responseDuration = 420
  const now = Date.now()
  const width = app.renderer.width
  const height = app.renderer.height
  const selectedIds = new Set(props.selectedIds)
  beginMotionFrame(now)

  let staticDirty = false
  for (const [id, entry] of staticParticles) {
    const terminal = terminalAt(entry.record)
    const visualEnd = terminal === undefined ? undefined : terminal + props.renderDelayMs + Math.max(1_000, props.trailMs)
    if (visualEnd !== undefined && now > visualEnd) {
      removeStaticParticle(id)
      retired.add(id)
      staticDirty = true
    }
  }
  if (staticDirty) staticContainer.update()

  for (const [id, particle] of dynamicParticles) {
    const record = particle.record
    const points = requestPoints(id, width, height)
    const gateway = gatewayQueuePoint(record, points.gateway)
    let start = visualStartTimes.get(id)
    if (start === undefined) {
      start = Math.max(record.startedAt + props.renderDelayMs, now)
      visualStartTimes.set(id, start)
    }
    if (now < start) {
      particle.visible = false
      particle.alpha = 0
      continue
    }
    const ingressEnd = start + ingressDuration
    const waitingAtGate = waitingAtGateway(record)
    const forwardStart = record.releasedAt === undefined
      ? ingressEnd
      : Math.max(ingressEnd, visualReleaseTimes.get(id) ?? record.releasedAt + props.renderDelayMs)
    const forwardEnd = forwardStart + forwardDuration
    const blockedAtGateway = record.blockedAt !== undefined
    const actualResponseStart = record.responseStartedAt === undefined && record.blockedAt === undefined
      ? undefined
      : visualResponseTimes.get(id) ?? (record.blockedAt ?? record.responseStartedAt)! + props.renderDelayMs
    const responseOrigin = blockedAtGateway ? gateway : points.upstream
    const visualResponseStart = actualResponseStart === undefined ? undefined : Math.max(actualResponseStart, blockedAtGateway ? ingressEnd : forwardEnd)
    const responseEnd = visualResponseStart === undefined ? undefined : visualResponseStart + responseDuration
    const trailEnd = responseEnd === undefined ? undefined : responseEnd + props.trailMs
    const terminal = terminalAt(record)
    const abortedTrailEnd = record.abortedAt === undefined
      ? undefined
      : (visualAbortTimes.get(id) ?? record.abortedAt + props.renderDelayMs) + Math.max(1_000, props.trailMs)
    const visualEnd = abortedTrailEnd ?? trailEnd
    if (terminal !== undefined && visualEnd !== undefined && now > visualEnd) {
      removeDynamicParticle(id)
      retired.add(id)
      continue
    }

    let position = gateway
    let alpha = 1
    let pulse = 0
    let direction: -1 | 0 | 1 = 1
    if (now < ingressEnd) position = interpolate(points.client, gateway, (now - start) / ingressDuration)
    else if (record.abortedAt !== undefined) {
      position = gateway
      direction = 0
      const fadeStart = record.abortedAt + props.renderDelayMs
      if (now > fadeStart) alpha = Math.max(0, 1 - (now - fadeStart) / Math.max(1, props.trailMs))
    } else if (waitingAtGate) {
      position = gateway
      direction = 0
      pulse = 1
    } else if (visualResponseStart === undefined || now < visualResponseStart) {
      if (now < forwardStart) position = gateway
      else if (now < forwardEnd) position = interpolate(gateway, points.upstream, (now - forwardStart) / forwardDuration)
      else {
        position = points.upstream
        direction = 0
        pulse = 1
      }
    } else {
      direction = -1
      const progress = Math.min(1, (now - visualResponseStart) / responseDuration)
      if (blockedAtGateway) position = interpolate(gateway, points.client, progress)
      else if (progress < 0.5) position = interpolate(responseOrigin, gateway, progress * 2)
      else position = interpolate(gateway, points.client, (progress - 0.5) * 2)
      if (responseEnd !== undefined && terminal !== undefined && now > responseEnd) alpha = Math.max(0, 1 - (now - responseEnd) / Math.max(1, props.trailMs))
    }

    const dotColor = dotColorFor(record)
    const selected = selectedIds.has(id)
    const pulseScale = pulse > 0 ? (Math.sin(now / 140) + 1) * 0.3 : 0
    drawDynamicParticle(particle, position, direction, now, alpha, dotColor, selected, pulseScale)
  }
}

function resetLayers() {
  if (!app) return
  while (app.stage.children.length > 0) app.stage.removeChildAt(0).destroy()
  dynamicParticles.clear()
  staticParticles.clear()
  retired.clear()
  gatewayQueueAssignments.clear()
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
  const marker = new Graphics().circle(4, 4, 4).fill(0xffffff)
  markerTexture = app.renderer.generateTexture({ target: marker, frame: new Rectangle(0, 0, 8, 8), antialias: true })
  marker.destroy()
  residualCanvas = document.createElement('canvas')
  residualCanvas.className = 'traffic-residual-layer'
  residualCanvas.setAttribute('aria-hidden', 'true')
  motionCanvas = document.createElement('canvas')
  motionCanvas.className = 'traffic-motion-layer'
  motionCanvas.setAttribute('aria-hidden', 'true')
  resizeMotionLayer()
  host.value.appendChild(app.canvas)
  host.value.appendChild(residualCanvas)
  host.value.appendChild(motionCanvas)
  host.value.addEventListener('pointerdown', onPointerDown)
  host.value.addEventListener('pointermove', onPointerMove)
  host.value.addEventListener('pointerup', onPointerUp)
  host.value.addEventListener('pointercancel', onPointerUp)
  resetLayers()
  app.ticker.add(render)
  resizeObserver = new ResizeObserver(() => {
    if (!app || !host.value) return
    app.renderer.resize(host.value.clientWidth, host.value.clientHeight)
    resizeMotionLayer()
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
  markerTexture?.destroy(true)
  markerTexture = undefined
  residualCanvas?.remove()
  residualCanvas = undefined
  residualContext = undefined
  motionCanvas?.remove()
  motionCanvas = undefined
  motionContext = undefined
  cometSprites.clear()
  colorStyles.clear()
  visualStartTimes.clear()
  visualReleaseTimes.clear()
  visualResponseTimes.clear()
  visualAbortTimes.clear()
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
.lane-label { position: absolute; z-index: 1; top: 8px; transform: translateX(-50%); color: #9eb8ca; font-size: 10px; font-weight: 750; letter-spacing: .11em; text-transform: uppercase; pointer-events: none; }
.label-client { left: 5%; }
.label-gateway { left: 50%; }
.label-upstream { left: 95%; }
</style>
