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
import { Loader2 } from 'lucide-react'
import type { CSSProperties } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

interface LoadingStateProps {
  className?: string
  message?: string
  size?: 'sm' | 'md' | 'lg'
  inline?: boolean
}

const sizeMap = {
  sm: 'size-4',
  md: 'size-6',
  lg: 'size-8',
} as const

export function LoadingState(props: LoadingStateProps) {
  const { t } = useTranslation()
  const iconSize = sizeMap[props.size ?? 'md']

  if (props.inline) {
    return (
      <span className={cn('inline-flex items-center gap-2', props.className)}>
        <Loader2 className={cn(iconSize, 'animate-spin')} />
        {props.message != null && (
          <span className='text-muted-foreground text-sm'>{props.message}</span>
        )}
   </span>
    )
  }

  return (
    <div
      className={cn(
        'flex min-h-[200px] flex-col items-center justify-center gap-3',
        props.className
      )}
    >
      <div className='animate-spin'>
        <Loader2 className={iconSize} />
     </div>
      <p className='text-muted-foreground text-sm'>
        {props.message ?? t('Loading...')}
     </p>
   </div>
  )
}

/**
 * Shared skeleton-style placeholder primitive used by both `LoadingState`
 * (full-page loading) and `RouteLoadingFallback` (lazy route pending).
 *
 * Reads `--skeleton-base`, `--skeleton-highlight`, and `--radius-md` from the
 * active theme so the placeholders match the rest of the UI automatically.
 */
export function SkeletonBlock({
  className,
  style,
}: {
  className?: string
  style?: CSSProperties
}) {
  return (
    <div
      aria-hidden='true'
      className={cn('skeleton-shimmer rounded-md', className)}
      style={{
        borderRadius: 'var(--radius-md)',
        ...style,
      }}
    />
  )
}

interface RouteLoadingFallbackProps {
  /**
   * Optional override for the top title row height. Defaults to a small
   * header shape that matches the standard section title.
   */
  titleClassName?: string
}

/**
 * Full-viewport placeholder shown by TanStack Router when a lazy route's
 * chunk is being fetched. Mounted via `defaultPendingComponent` in
 * `apps/web/src/main.tsx`. Reads theme tokens so it blends with the
 * surrounding UI.
 */
export function RouteLoadingFallback(props: RouteLoadingFallbackProps) {
  return (
    <div
      role='status'
      aria-live='polite'
      className='flex w-full flex-col gap-4 p-4 sm:p-6'
    >
      <div className='flex items-center justify-between'>
        <SkeletonBlock
          className={cn('h-8 w-48 rounded-lg', props.titleClassName ?? '')}
        />
        <SkeletonBlock className='h-8 w-24 rounded-lg' />
      </div>
      <div className='grid gap-3 sm:gap-4'>
        <SkeletonBlock className='h-20 w-full rounded-xl' />
        <SkeletonBlock className='h-64 w-full rounded-xl' />
      </div>
    </div>
  )
}

interface SectionFallbackProps {
  /**
   * Shape of the placeholder. Each variant is a thin layout shell that
   * hints at the eventual content without committing to copy or chart
   * details. All blocks read theme tokens via `SkeletonBlock`.
   */
  variant: 'stat-cards' | 'chart' | 'metrics'
}

/**
 * Shared `<Suspense>` fallback for lazy dashboard sections. Replaces the
 * six ad-hoc `*Fallback` components that previously lived inline in
 * `features/dashboard/index.tsx`. The variants mirror the dominant
 * layouts of the lazy children: a row of stat cards, a tall chart card,
 * or a horizontal metrics strip.
 */
export function SectionFallback(props: SectionFallbackProps) {
  if (props.variant === 'stat-cards') {
    return (
      <div className='overflow-hidden rounded-lg border'>
        <div className='grid grid-cols-2 divide-x divide-border/60 sm:grid-cols-3 lg:grid-cols-5'>
          {['count', 'quota', 'tokens', 'rpm', 'tpm'].map((key) => (
            <div key={key} className='px-2.5 py-1.5 sm:px-5 sm:py-4'>
              <div className='flex items-center gap-1.5 sm:gap-2'>
                <SkeletonBlock className='size-4 rounded-sm sm:size-7 sm:rounded-md' />
                <SkeletonBlock className='h-4 w-16' />
             </div>
              <SkeletonBlock className='mt-1 h-5 w-16 sm:mt-2 sm:h-7 sm:w-20' />
              <SkeletonBlock className='mt-1 hidden h-3.5 w-28 md:block' />
           </div>
          ))}
       </div>
     </div>
    )
  }

  if (props.variant === 'chart') {
    return (
      <div className='overflow-hidden rounded-lg border'>
        <div className='flex items-center justify-between border-b px-4 py-3 sm:px-5'>
          <SkeletonBlock className='h-5 w-32' />
          <SkeletonBlock className='h-8 w-72' />
       </div>
        <div className='h-96 p-2'>
          <SkeletonBlock className='h-full w-full' />
       </div>
     </div>
    )
  }

  // 'metrics'
  return (
    <div className='overflow-hidden rounded-lg border'>
      <div className='flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3 sm:px-5'>
        <div className='flex items-center gap-2'>
          <SkeletonBlock className='h-4 w-24' />
       </div>
        {['success-rate', 'latency', 'throughput'].map((key) => (
          <div key={key} className='flex items-center gap-1.5'>
            <SkeletonBlock className='h-3 w-14' />
            <SkeletonBlock className='h-4 w-16' />
         </div>
        ))}
        <div className='ml-auto flex items-center gap-2'>
          {['primary', 'secondary'].map((key) => (
            <SkeletonBlock
              key={key}
              className='h-5 w-28 rounded-full'
            />
          ))}
       </div>
     </div>
   </div>
  )
}
