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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Main } from '@/components/layout'
import { PageFooterProvider } from '@/components/layout/components/page-footer'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  hasPermission,
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
} from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import { ChannelCreateTab } from './components/channel-create-tab'
import { ChannelListTab } from './components/channel-list-tab'
import { ChannelsDialogs } from './components/channels-dialogs'
import { ChannelsPrimaryButtons } from './components/channels-primary-buttons'
import { ChannelsProvider, useChannels } from './components/channels-provider'

function ChannelsContent() {
  const { t } = useTranslation()
  const { pageTab, setPageTab, setCurrentRow } = useChannels()
  const currentUser = useAuthStore((state) => state.auth.user)
  const canCreateChannel = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const [footerContainer, setFooterContainer] = useState<HTMLDivElement | null>(
    null
  )
  const [createActionsContainer, setCreateActionsContainer] =
    useState<HTMLDivElement | null>(null)
  const createScrollContainerRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (pageTab !== 'create') return

    const frameId = requestAnimationFrame(() => {
      createScrollContainerRef.current?.focus({ preventScroll: true })
    })

    return () => cancelAnimationFrame(frameId)
  }, [pageTab])

  return (
    <>
      <PageFooterProvider container={footerContainer}>
        <Main>
          <Tabs
            value={pageTab}
            onValueChange={(value) => {
              const next = value as 'create' | 'channels'
              if (next === 'create') setCurrentRow(null)
              setPageTab(next)
            }}
            className='flex h-full min-h-0 flex-col'
          >
            <div className='shrink-0 px-3 pt-1 sm:px-4 sm:pt-5 sm:pb-3'>
              <div className='flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between sm:gap-x-3 sm:gap-y-2'>
                <TabsList className='grid w-full grid-cols-2 items-center bg-muted/60 p-1 sm:w-fit group-data-horizontal/tabs:h-auto'>
                  <TabsTrigger
                    value='create'
                    disabled={!canCreateChannel}
                    className='h-7 px-3 text-xs font-medium data-active:bg-background data-active:shadow-sm'
                  >
                    <span className='sm:hidden'>{t('Create')}</span>
                    <span className='hidden sm:inline'>{t('Create Channel')}</span>
                  </TabsTrigger>
                  <TabsTrigger
                    value='channels'
                    className='h-7 px-3 text-xs font-medium data-active:bg-background data-active:shadow-sm'
                  >
                    {t('Channels')}
                  </TabsTrigger>
                </TabsList>
                <div
                  ref={setCreateActionsContainer}
                  className={
                    pageTab === 'create'
                      ? 'flex w-full flex-wrap items-center justify-center gap-2 sm:w-auto sm:justify-end'
                      : 'hidden'
                  }
                />
              </div>
            </div>

            <div
              ref={createScrollContainerRef}
              className={
                pageTab === 'create'
                  ? 'min-h-0 flex-1 overflow-y-auto overflow-x-hidden px-3 pt-1 pb-3 sm:px-4 sm:pt-1.5 sm:pb-4'
                  : 'min-h-0 flex-1 overflow-hidden px-3 pt-1 pb-3 sm:px-4 sm:pt-1.5 sm:pb-4'
              }
              tabIndex={pageTab === 'create' ? -1 : undefined}
            >
              <TabsContent
                value='create'
                keepMounted
                className='m-0 flex min-h-full flex-col data-hidden:hidden'
              >
                <ChannelCreateTab
                  actionsContainer={createActionsContainer}
                  active={pageTab === 'create'}
                />
              </TabsContent>

              <TabsContent
                value='channels'
                keepMounted
                className='m-0 flex h-full min-h-0 flex-col gap-3 overflow-hidden data-hidden:hidden'
              >
                <div className='flex shrink-0 flex-wrap items-center justify-end gap-2 px-1 pb-3 sm:gap-x-4'>
                  <ChannelsPrimaryButtons />
                </div>
                <ChannelListTab />
              </TabsContent>
            </div>

            <div
              ref={setFooterContainer}
              className='bg-background shrink-0 border-t px-3 py-2.5 empty:hidden sm:px-4 sm:py-3'
              hidden={pageTab !== 'channels'}
            />
          </Tabs>
        </Main>
      </PageFooterProvider>

      <ChannelsDialogs />
    </>
  )
}

export function Channels() {
  return (
    <ChannelsProvider>
      <ChannelsContent />
    </ChannelsProvider>
  )
}
