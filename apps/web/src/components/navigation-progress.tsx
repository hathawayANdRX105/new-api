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
import { useRouterState } from '@tanstack/react-router'
import { useEffect, useRef } from 'react'
import LoadingBar, { type LoadingBarRef } from 'react-top-loading-bar'

import {
  createPendingProgressScheduler,
  type PendingProgressScheduler,
} from './navigation-progress-scheduler'

export function NavigationProgress() {
  const ref = useRef<LoadingBarRef>(null)
  const schedulerRef = useRef<PendingProgressScheduler | null>(null)

  if (schedulerRef.current === null) {
    schedulerRef.current = createPendingProgressScheduler(() => ref.current)
  }

  const state = useRouterState()

  useEffect(() => {
    const scheduler = schedulerRef.current
    if (!scheduler) return
    if (state.status === 'pending') {
      scheduler.notifyPending()
    } else {
      scheduler.notifyIdle()
    }
  }, [state.status])

  useEffect(() => {
    const scheduler = schedulerRef.current
    return () => {
      scheduler?.dispose()
      // Strict Mode unmount/remount would otherwise reuse the disposed
      // scheduler (refs persist across mounts); reset so the next
      // mount rebuilds a fresh one.
      schedulerRef.current = null
    }
  }, [])

  return (
    <LoadingBar
      color='var(--muted-foreground)'
      ref={ref}
      shadow={true}
      height={2}
    />
  )
}
