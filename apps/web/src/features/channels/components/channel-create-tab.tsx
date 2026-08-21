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
import { ChannelMutateDrawer } from './drawers/channel-mutate-drawer'
import { useChannels } from './channels-provider'

export function ChannelCreateTab(props: {
  actionsContainer: HTMLElement | null
  active: boolean
}) {
  const { setPageTab } = useChannels()

  return (
    <div className='flex min-h-full flex-col'>
      <ChannelMutateDrawer
        open={props.active}
        presentation='inline'
        currentRow={null}
        formId='channel-create-form'
        actionsContainer={props.actionsContainer}
        onOpenChange={(isOpen) => {
          if (!isOpen) setPageTab('channels')
        }}
      />
    </div>
  )
}
