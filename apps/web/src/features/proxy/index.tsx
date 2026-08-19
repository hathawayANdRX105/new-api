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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Download, RefreshCw, Save } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ProxyNodeView } from './proxy-node-view'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Switch } from '@/components/ui/switch'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { toast } from '@/hooks/use-toast'
import { api } from '@/lib/api'

type ApiResponse<T> = {
  success: boolean
  message?: string
  data: T
}

type OutboundType =
  | 'vless'
  | 'vmess'
  | 'trojan'
  | 'shadowsocks'
  | 'hysteria2'
  | 'socks5'
  | 'http'

type OutboundConfig = {
  type: string
  server: string
  server_port: number
  uuid: string
  password: string
  flow: string
  encryption: string
  tls_enabled: boolean
  tls_server_name: string
  network: string
  packet_encoding: string
  method: string
  masquerade: string
  obfs: string
  obfs_password: string
  hop_ports: string
  transport_type: string
  transport_path: string
  transport_host: string
  transport_service_name: string
}

type ProxyConfig = {
  outbound: OutboundConfig | null
  global_proxy_url: string
  enabled: boolean
}

type ProxyStatus = {
  running: boolean
  error?: string
}

type GenerateResponse = {
  config_json: string
}

type ReloadResponse = {
  success: boolean
  message: string
}

const PROTOCOL_OPTIONS: { value: OutboundType; label: string }[] = [
  { value: 'vless', label: 'VLESS' },
  { value: 'vmess', label: 'VMess' },
  { value: 'trojan', label: 'Trojan' },
  { value: 'shadowsocks', label: 'Shadowsocks' },
  { value: 'hysteria2', label: 'Hysteria2' },
  { value: 'socks5', label: 'SOCKS5' },
  { value: 'http', label: 'HTTP' },
]

const FLOW_OPTIONS = [
  { value: 'xtls-rprx-vision', label: 'xtls-rprx-vision' },
  { value: 'none', label: 'None' },
]

const ENCRYPTION_OPTIONS = [
  { value: 'aes-256-gcm', label: 'aes-256-gcm' },
  { value: 'chacha20-ietf-poly1305', label: 'chacha20-ietf-poly1305' },
  { value: 'none', label: 'None' },
]

const NETWORK_OPTIONS = [
  { value: 'tcp', label: 'tcp' },
  { value: 'udp', label: 'udp' },
]

const PACKET_ENCODING_OPTIONS = [
  { value: 'none', label: 'None' },
  { value: 'xtls-rprx-vision', label: 'xtls-rprx-vision' },
  { value: 'xudp', label: 'xudp' },
]

const SS_METHOD_OPTIONS = [
  { value: 'none', label: 'None' },
  { value: 'aes-256-gcm', label: 'aes-256-gcm' },
  { value: 'aes-128-gcm', label: 'aes-128-gcm' },
  { value: 'chacha20-ietf-poly1305', label: 'chacha20-ietf-poly1305' },
]

const OBFS_OPTIONS = [
  { value: 'none', label: 'none' },
  { value: 'salamander', label: 'salamander' },
]

const TRANSPORT_TYPE_OPTIONS = [
  { value: 'none', label: 'none' },
  { value: 'ws', label: 'ws' },
  { value: 'kcp', label: 'kcp' },
  { value: 'quic', label: 'quic' },
  { value: 'grpc', label: 'grpc' },
]

const DEFAULT_OUTBOUND: OutboundConfig = {
  type: 'vless',
  server: '',
  server_port: 443,
  uuid: '',
  password: '',
  flow: 'none',
  encryption: 'none',
  tls_enabled: false,
  tls_server_name: '',
  network: 'tcp',
  packet_encoding: '',
  method: '',
  masquerade: '',
  obfs: 'none',
  obfs_password: '',
  hop_ports: '',
  transport_type: 'none',
  transport_path: '',
  transport_host: '',
  transport_service_name: '',
}

const DEFAULT_CONFIG: ProxyConfig = {
  outbound: DEFAULT_OUTBOUND,
  global_proxy_url: '',
  enabled: false,
}

async function fetchProxyConfig(): Promise<ProxyConfig> {
  const res = await api.get<ApiResponse<ProxyConfig>>('/api/proxy/config')
  if (!res.data.success) {
    throw new Error(res.data.message || 'Failed to load proxy config')
  }
  return res.data.data
}

async function fetchProxyStatus(): Promise<ProxyStatus> {
  const res = await api.get<ApiResponse<ProxyStatus>>('/api/proxy/status')
  if (!res.data.success) {
    throw new Error(res.data.message || 'Failed to load proxy status')
  }
  return res.data.data
}

async function fetchProxyGenerate(): Promise<GenerateResponse> {
  const res = await api.get<ApiResponse<GenerateResponse>>(
    '/api/proxy/config/generate',
  )
  if (!res.data.success) {
    throw new Error(res.data.message || 'Failed to generate config')
  }
  return res.data.data
}

async function saveProxyConfig(payload: ProxyConfig): Promise<void> {
  const res = await api.put<ApiResponse<null>>('/api/proxy/config', payload)
  if (!res.data.success) {
    throw new Error(res.data.message || 'Failed to save')
  }
}

async function reloadProxy(): Promise<ReloadResponse> {
  const res = await api.post<ApiResponse<ReloadResponse>>('/api/proxy/reload')
  if (!res.data.success) {
    throw new Error(res.data.message || 'Failed to reload')
  }
  return res.data.data
}

function normalizeConfig(config: ProxyConfig): ProxyConfig {
  const outbound = config.outbound ?? DEFAULT_OUTBOUND
  return {
    enabled: !!config.enabled,
    global_proxy_url: config.global_proxy_url ?? '',
    outbound: {
      ...DEFAULT_OUTBOUND,
      ...outbound,
      flow: outbound.flow || 'none',
      encryption: outbound.encryption || 'none',
      network: outbound.network || 'tcp',
      obfs: outbound.obfs || 'none',
      transport_type: outbound.transport_type || 'none',
    },
  }
}

function toSavePayload(config: ProxyConfig): ProxyConfig {
  const outbound = config.outbound ?? DEFAULT_OUTBOUND
  return {
    ...config,
    outbound: {
      ...outbound,
      flow: outbound.flow === 'none' ? '' : outbound.flow,
      encryption:
        outbound.encryption === 'none' ? '' : outbound.encryption,
      network: outbound.network === 'tcp' ? '' : outbound.network,
      obfs: outbound.obfs === 'none' ? '' : outbound.obfs,
      transport_type:
        outbound.transport_type === 'none' ? '' : outbound.transport_type,
    },
  }
}


export function ProxyPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { copyToClipboard } = useCopyToClipboard()
  const [form, setForm] = useState<ProxyConfig>(DEFAULT_CONFIG)

  const configQuery = useQuery({
    queryKey: ['proxy', 'config'],
    queryFn: fetchProxyConfig,
    staleTime: 60 * 1000,
    retry: false,
  })

  const statusQuery = useQuery({
    queryKey: ['proxy', 'status'],
    queryFn: fetchProxyStatus,
    refetchInterval: 30_000,
    staleTime: 30_000,
    retry: false,
  })

  const generateQuery = useQuery({
    queryKey: ['proxy', 'generate'],
    queryFn: fetchProxyGenerate,
    staleTime: 60 * 1000,
    retry: false,
  })

  useEffect(() => {
    if (configQuery.data) {
      setForm(normalizeConfig(configQuery.data))
    }
  }, [configQuery.data])

  const saveMutation = useMutation({
    mutationFn: saveProxyConfig,
    onSuccess: () => {
      toast.success(t('Saved successfully'))
      queryClient.invalidateQueries({ queryKey: ['proxy', 'config'] })
      queryClient.invalidateQueries({ queryKey: ['proxy', 'status'] })
      queryClient.invalidateQueries({ queryKey: ['proxy', 'generate'] })
    },
    onError: (err: Error) => toast.error(err.message || t('Failed to save')),
  })

  const reloadMutation = useMutation({
    mutationFn: reloadProxy,
    onSuccess: (data) => {
      if (data.success) {
        toast.success(data.message || t('Reloaded successfully'))
      } else {
        toast.error(data.message || t('Failed to reload'))
      }
      queryClient.invalidateQueries({ queryKey: ['proxy', 'status'] })
    },
    onError: (err: Error) => toast.error(err.message || t('Failed to reload')),
  })

  const outbound = form.outbound ?? DEFAULT_OUTBOUND
  const configJson = generateQuery.data?.config_json

  const updateOutbound = useCallback((patch: Partial<OutboundConfig>) => {
    setForm((current) => ({
      ...current,
      outbound: { ...(current.outbound ?? DEFAULT_OUTBOUND), ...patch },
    }))
  }, [])

  const handleDownload = useCallback(() => {
    if (!configJson) return
    const url = URL.createObjectURL(
      new Blob([configJson], { type: 'application/json' }),
    )
    const link = document.createElement('a')
    link.href = url
    link.download = 'singbox-config.json'
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
  }, [configJson])

  const handleCopy = useCallback(() => {
    if (configJson) void copyToClipboard(configJson)
  }, [configJson, copyToClipboard])

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        <span className='truncate'>{t('Proxy Config')}</span>
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <ProxyNodeView />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
