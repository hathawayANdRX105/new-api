/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'
import type { LoadingBarRef } from 'react-top-loading-bar'

import { createPendingProgressScheduler } from '../navigation-progress-scheduler'

type RecordedCall =
  | { kind: 'start'; type?: 'static' | 'continuous'; value?: number }
  | { kind: 'complete' }

function makeRecorder(): {
  ref: LoadingBarRef
  calls: RecordedCall[]
} {
  const calls: RecordedCall[] = []
  const ref: LoadingBarRef = {
    start: (type, value) => {
      calls.push({ kind: 'start', type, value })
    },
    staticStart: (value) => {
      calls.push({ kind: 'start', type: 'static', value })
    },
    continuousStart: (value) => {
      calls.push({ kind: 'start', type: 'continuous', value })
    },
    complete: () => {
      calls.push({ kind: 'complete' })
    },
    increase: () => undefined,
    decrease: () => undefined,
    getProgress: () => 0,
  }
  return { ref, calls }
}

function findStart(calls: RecordedCall[]) {
  return calls.find((c) => c.kind === 'start')
}
function findComplete(calls: RecordedCall[]) {
  return calls.find((c) => c.kind === 'complete')
}

// Wait long enough for any showDelayMs=0 timer to fire in the next tick.
const flush = () =>
  new Promise<void>((resolve) => {
    setTimeout(resolve, 5)
  })

describe('createPendingProgressScheduler', () => {
  test('does not show the bar when pending resolves within the show delay', () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 150 })

    s.notifyPending()
    // Simulate the router resolving before the bar appears.
    s.notifyIdle()

    assert.equal(calls.length, 0, 'no bar ops should run for instant transitions')
  })

  test('shows the bar at initialProgress once pending lasts past the delay', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, {
      showDelayMs: 0,
      initialProgress: 40,
    })

    s.notifyPending()
    await flush()

    const start = findStart(calls)
    assert.ok(start, 'start should be called after the delay elapses')
    assert.equal(start?.type, 'static')
    assert.equal(start?.value, 40)
  })

  test('notifyIdle after the bar is visible completes it exactly once', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 0 })

    s.notifyPending()
    await flush()
    s.notifyIdle()

    assert.equal(calls.filter((c) => c.kind === 'start').length, 1)
    assert.ok(findComplete(calls), 'complete should be called when idle')
  })

  test('repeated notifyPending calls only show the bar once', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 0 })

    s.notifyPending()
    s.notifyPending()
    s.notifyPending()
    await flush()

    assert.equal(calls.filter((c) => c.kind === 'start').length, 1)
  })

  test('rapid pending/idle cycles do not flash the bar', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 0 })

    // First three cycles: pending then idle before the show timer fires.
    // With showDelayMs=0 the timer fires in the next tick, so we must
    // call notifyIdle synchronously before awaiting.
    s.notifyPending()
    s.notifyIdle()
    s.notifyPending()
    s.notifyIdle()
    s.notifyPending()
    s.notifyIdle()
    await flush()

    assert.equal(
      calls.length,
      0,
      'rapid cycles should cancel before the bar ever shows',
    )
  })

  test('pending followed by a delayed idle shows and then completes the bar', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 0 })

    s.notifyPending()
    await flush()
    assert.equal(calls.filter((c) => c.kind === 'start').length, 1)

    s.notifyIdle()
    assert.equal(calls.filter((c) => c.kind === 'complete').length, 1)
  })

  test('dispose cancels a pending show', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 0 })

    s.notifyPending()
    s.dispose()
    await flush()

    assert.equal(calls.length, 0, 'dispose must not show the bar')
  })

  test('dispose after the bar is visible completes it', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 0 })

    s.notifyPending()
    await flush()
    assert.equal(calls.filter((c) => c.kind === 'start').length, 1)

    s.dispose()
    assert.ok(findComplete(calls), 'dispose should complete the visible bar')
  })

  test('notifyPending after dispose is a no-op', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, { showDelayMs: 0 })
    s.dispose()

    s.notifyPending()
    await flush()

    assert.equal(calls.length, 0)
  })

  test('uses the provided options', async () => {
    const { ref, calls } = makeRecorder()
    const s = createPendingProgressScheduler(() => ref, {
      showDelayMs: 0,
      initialProgress: 75,
    })

    s.notifyPending()
    await flush()

    const start = findStart(calls)
    assert.equal(start?.value, 75)
  })

  test('tolerates a null ref (e.g. before mount)', async () => {
    let refOrNull: LoadingBarRef | null = null
    const s = createPendingProgressScheduler(() => refOrNull, { showDelayMs: 0 })

    // Before the ref is wired, notifyPending must not throw. Whether it
    // records a call or not is irrelevant — the caller will issue another
    // notifyPending once the ref is ready.
    assert.doesNotThrow(() => s.notifyPending())
    assert.doesNotThrow(() => s.notifyIdle())
    assert.doesNotThrow(() => s.dispose())

    const calls: RecordedCall[] = []
    refOrNull = {
      start: (type, value) => {
        calls.push({ kind: 'start', type, value })
      },
      staticStart: () => undefined,
      continuousStart: () => undefined,
      complete: () => {
        calls.push({ kind: 'complete' })
      },
      increase: () => undefined,
      decrease: () => undefined,
      getProgress: () => 0,
    }

    // A fresh scheduler with the ref wired works as expected.
    const s2 = createPendingProgressScheduler(() => refOrNull, { showDelayMs: 0 })
    s2.notifyPending()
    await flush()
    assert.equal(calls.length, 1)
    s2.notifyIdle()
    assert.equal(calls.length, 2)
    assert.equal(calls[1].kind, 'complete')
  })
})
