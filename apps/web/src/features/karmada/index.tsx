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
import axios from 'axios'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { useAuthStore } from '@/stores/auth-store'

import { KARMADA_DASHBOARD_URL } from './config'

/**
 * Karmada Dashboard redirect page.
 *
 * Verifies the current root session via New API backend, establishes a short-lived
 * HttpOnly cookie (`newapi_karmada_session`), then redirects the browser to the
 * same-origin Karmada Dashboard path (`/karmada-dashboard/`).
 *
 * This eliminates iframe embedding issues (scrollbars, theme mismatch, cross-origin security)
 * while keeping the Dashboard root-only and token-free for the browser.
 */
export function KarmadaDashboard() {
  const { t } = useTranslation()
  const accessToken = useAuthStore((state) => state.auth.accessToken)
  const [sessionError, setSessionError] = useState(false)

  useEffect(() => {
    if (!accessToken) {
      setSessionError(true)
      return
    }

    let cancelled = false
    setSessionError(false)

    void axios
      .post(
        '/api/karmada/session',
        undefined,
        {
          headers: { Authorization: `Bearer ${accessToken}` },
          withCredentials: true,
        }
      )
      .then(() => {
        if (!cancelled) {
          window.location.href = KARMADA_DASHBOARD_URL
        }
      })
      .catch(() => {
        if (!cancelled) setSessionError(true)
      })

    return () => {
      cancelled = true
    }
  }, [accessToken])

  if (sessionError) {
    return (
      <div className='flex h-full items-center justify-center p-6'>
        <Alert variant='destructive' className='max-w-xl'>
          <AlertTitle>{t('Unable to open Karmada Dashboard')}</AlertTitle>
          <AlertDescription>
            {t('Your root session could not be verified for Karmada access.')}
          </AlertDescription>
        </Alert>
      </div>
    )
  }

  return (
    <div className='flex h-full items-center justify-center text-sm text-muted-foreground'>
      {t('Redirecting to Karmada Dashboard...')}
    </div>
  )
}
