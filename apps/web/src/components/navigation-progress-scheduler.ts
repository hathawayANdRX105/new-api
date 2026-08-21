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
import type { LoadingBarRef } from 'react-top-loading-bar'

/**
 * Decouples "router state changed" from "loading bar visibility".
 *
 * Without this, calling `continuousStart()` on every status flip produces
 * two bad behaviors:
 *  - On cached routes the bar flashes for a single frame
 *    (status goes pending → idle within the same paint).
 *  - On real loads the bar grows to 95% over 1 second using a fixed
 *    interval, regardless of how long the actual load takes.
 *
 * The scheduler:
 *  - delays showing the bar by `showDelayMs` so instant transitions stay
 *    bar-less;
 *  - jumps to a static `initialProgress` (no creep) once shown;
 *  - calls `complete()` exactly when the load actually resolves (caller
 *    drives the transitions, not a fixed timer).
 *
 * ponytail: global setTimeout handle; safe across rapid notifyPending /
 * notifyIdle cycles because the timer is cancelled and the visible flag
 * is checked. If the page becomes a SPA inside a Web Worker we would
 * move this off the main thread, but that is overkill for a top bar.
 */
export interface PendingProgressScheduler {
  /**
   * Call when the router becomes pending. Idempotent — extra calls while
   * already pending or visible are no-ops.
   */
  notifyPending(): void
  /**
   * Call when the router becomes idle. Cancels a pending show and, if
   * the bar is currently visible, completes it.
   */
  notifyIdle(): void
  /**
   * Cancel any pending timer. Safe to call multiple times. Equivalent to
   * `notifyIdle()` plus a guarantee that no future timer fires.
   */
  dispose(): void
}

export interface PendingProgressSchedulerOptions {
  /**
   * Don't show the bar for transitions shorter than this many ms.
   * Defaults to 150 — short enough to feel responsive, long enough to
   * swallow cached-route flashes.
   */
  showDelayMs?: number
  /**
   * Width % to jump to when the bar first appears. Defaults to 40 so
   * users see a clearly-progressed bar without waiting for it to creep.
   */
  initialProgress?: number
}

export function createPendingProgressScheduler(
  getRef: () => LoadingBarRef | null,
  options: PendingProgressSchedulerOptions = {},
): PendingProgressScheduler {
  const showDelayMs = options.showDelayMs ?? 150
  const initialProgress = options.initialProgress ?? 40
  let showTimer: ReturnType<typeof setTimeout> | null = null
  let visible = false
  let disposed = false

  const clearTimer = () => {
    if (showTimer !== null) {
      clearTimeout(showTimer)
      showTimer = null
    }
  }

  const hide = () => {
    clearTimer()
    if (visible) {
      visible = false
      getRef()?.complete()
    }
  }

  return {
    notifyPending() {
      if (disposed || showTimer !== null || visible) return
      showTimer = setTimeout(() => {
        showTimer = null
        if (disposed || visible) return
        // The ref can be null on the very first frame after mount
        // (the DOM ref is assigned by React after render). Bail before
        // flipping `visible`, otherwise `notifyIdle` would later call
        // `complete()` on a bar that was never shown.
        const loadingRef = getRef()
        if (!loadingRef) return
        visible = true
        // Static bar at `initialProgress` so the user sees immediate
        // progress without the slowly-creeping indeterminate animation.
        loadingRef.start('static', initialProgress)
      }, showDelayMs)
    },
    notifyIdle: hide,
    dispose() {
      if (disposed) return
      disposed = true
      hide()
    },
  }
}
