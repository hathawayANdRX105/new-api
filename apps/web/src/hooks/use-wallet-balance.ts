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
import { useMemo } from 'react'

import { formatQuota } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

/**
 * Custom hook to format the signed-in user's wallet balance
 * Centralizes balance display used by the sidebar, profile dropdown,
 * and mobile drawer wallet entries.
 * Returns null while the user or quota is not loaded yet.
 */
export function useWalletBalance(): string | null {
  const quota = useAuthStore((state) => state.auth.user?.quota)
  return useMemo(() => (quota == null ? null : formatQuota(quota)), [quota])
}
