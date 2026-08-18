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
import { Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Main } from '@/components/layout'
import { PageFooterProvider } from '@/components/layout/components/page-footer'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
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

  const switchToCreateTab = () => {
    setCurrentRow(null)
    setPageTab('create')
  }

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
            <div className='flex shrink-0 flex-col gap-2 px-3 pt-3 pb-2.5 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between sm:gap-x-3 sm:gap-y-2 sm:px-4 sm:pt-5 sm:pb-3'>
              <div className='flex items-center gap-2 sm:gap-3'>
                <h2 className='truncate text-base font-bold tracking-tight sm:text-lg'>
                  {t('Channels')}
                </h2>
                <Tooltip>
                  <TooltipTrigger render={<span className='inline-flex' />}>
                    <Button
                      onClick={switchToCreateTab}
                      size='sm'
                      disabled={!canCreateChannel}
                    >
                      <Plus className='h-4 w-4' />
                      <span className='max-sm:hidden'>
                        {t('Create Channel')}
                      </span>
                      <span className='sm:hidden'>{t('Create')}</span>
                    </Button>
                  </TooltipTrigger>
                  {!canCreateChannel && (
                    <TooltipContent>
                      {t('No permission to perform this action')}
                    </TooltipContent>
                  )}
                </Tooltip>
              </div>
              <TabsList className='grid w-full grid-cols-2 bg-muted/60 p-1 sm:w-fit'>
                <TabsTrigger
                  value='create'
                  disabled={!canCreateChannel}
                  onClick={() => setCurrentRow(null)}
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
            </div>

            <div className='min-h-0 flex-1 overflow-hidden px-3 pt-1 pb-3 sm:px-4 sm:pt-1.5 sm:pb-4'>
              <TabsContent
                value='create'
                keepMounted
                className='m-0 flex h-full min-h-0 flex-col overflow-hidden data-hidden:hidden'
              >
                <ChannelCreateTab />
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
