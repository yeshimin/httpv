import assert from 'node:assert/strict'
import test from 'node:test'
import { applyTrafficEvent, reconcileAnimationAdmissions, terminalTimestamp, visualExpiry } from '../src/traffic-lifecycle.ts'

test('a complete manual-gate lifecycle becomes terminal and receives a finite visual expiry', () => {
  const records = new Map()
  const events = [
    { phase: 'request_started', request_id: 'request-1', subject_id: 'demo', timestamp_ms: 100, method: 'GET', path: '/' },
    { phase: 'request_pending', request_id: 'request-1', subject_id: 'demo', timestamp_ms: 120 },
    { phase: 'request_timeout', request_id: 'request-1', subject_id: 'demo', timestamp_ms: 5_000, action: 'allow' },
    { phase: 'response_started', request_id: 'request-1', subject_id: 'demo', timestamp_ms: 5_020 },
    { phase: 'response_finished', request_id: 'request-1', subject_id: 'demo', timestamp_ms: 5_040, status: 200, duration_ms: 4_940, response_bytes: 256 }
  ]

  for (const event of events) applyTrafficEvent(event, records)
  const record = records.get('request-1')
  assert.ok(record)
  assert.equal(record.releasedAt, 5_000)
  assert.equal(record.responseStartedAt, 5_020)
  assert.equal(record.finishedAt, 5_040)
  assert.equal(record.durationMs, 4_940)
  assert.equal(terminalTimestamp(record), 5_040)
  assert.equal(visualExpiry(record, 0, 3_000), 8_460)
})

test('a blocked request is terminal without waiting for a response lifecycle', () => {
  const records = new Map()
  applyTrafficEvent({ phase: 'request_started', request_id: 'request-2', subject_id: 'demo', timestamp_ms: 100 }, records)
  applyTrafficEvent({ phase: 'request_blocked', request_id: 'request-2', subject_id: 'demo', timestamp_ms: 160, action: 'block', status: 403 }, records)

  const record = records.get('request-2')
  assert.ok(record)
  assert.equal(record.blockedAt, 160)
  assert.equal(record.status, 403)
  assert.equal(terminalTimestamp(record), 160)
  assert.equal(visualExpiry(record, 100, 1_000), 1_680)
})

test('animation admission is sticky and never promotes previously overflowed requests', () => {
  const admissions = new Set<string>()
  const seen = new Set<string>()
  const record = (requestId: string) => ({ requestId, subjectId: 'demo', method: 'GET', path: '/', clientIp: '', startedAt: 1 })

  assert.deepEqual(reconcileAnimationAdmissions([record('one'), record('two')], admissions, seen, 2), ['one', 'two'])
  assert.deepEqual(reconcileAnimationAdmissions([record('one'), record('two'), record('three')], admissions, seen, 2), ['one', 'two'])
  assert.deepEqual(reconcileAnimationAdmissions([record('two'), record('three')], admissions, seen, 2), ['two'])
  assert.deepEqual(reconcileAnimationAdmissions([record('two'), record('three'), record('four')], admissions, seen, 2), ['two', 'four'])
})
