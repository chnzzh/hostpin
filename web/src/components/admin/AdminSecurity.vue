<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, unwrap } from '../../api'
import { dateTime } from '../../format'
import type { AdminNode } from '../../types'
import { useLocale } from '../../i18n'

interface Session { id: string; ip_address: string; user_agent: string; created_at: string; expires_at: string; current: boolean }
interface APIKey { id: string; name: string; scopes: string[]; last_used_at?: string; expires_at?: string; created_at: string }
interface ShareLink { id: string; node_ids: string[]; expires_at: string; created_at: string; revoked_at?: string }

const sessions = ref<Session[]>([])
const keys = ref<APIKey[]>([])
const shares = ref<ShareLink[]>([])
const nodes = ref<AdminNode[]>([])
const twoFactor = ref(false)
const setupToken = ref('')
const totpSecret = ref('')
const totpURL = ref('')
const totpCode = ref('')
const recoveryCodes = ref<string[]>([])
const disablePassword = ref('')
const keyName = ref('automation')
const keyDays = ref(365)
const revealedKey = ref('')
const shareNodes = ref<string[]>([])
const shareExpiry = ref(new Date(Date.now() + 7 * 86400_000).toISOString().slice(0, 16))
const revealedShare = ref('')
const currentPassword = ref('')
const newPassword = ref('')
const status = ref('')
const error = ref('')
const { text } = useLocale()

async function load() {
  const [sessionResponse, keyResponse, shareResponse, nodeResponse, me] = await Promise.all([
    api<{ data: Session[] }>('/api/v1/admin/security/sessions'), api<{ data: APIKey[] }>('/api/v1/admin/api-keys'),
    api<{ data: ShareLink[] }>('/api/v1/admin/share-links'), api<{ data: AdminNode[] }>('/api/v1/admin/nodes'),
    api<{ two_factor_enabled?: boolean }>('/api/v1/auth/me'),
  ])
  sessions.value = unwrap(sessionResponse); keys.value = unwrap(keyResponse); shares.value = unwrap(shareResponse); nodes.value = unwrap(nodeResponse); twoFactor.value = !!me.two_factor_enabled
}
async function beginTOTP() { const result = await api<{ secret: string; otpauth_url: string; setup_token: string }>('/api/v1/admin/security/totp/setup', { method: 'POST' }); totpSecret.value = result.secret; totpURL.value = result.otpauth_url; setupToken.value = result.setup_token }
async function confirmTOTP() { const result = await api<{ recovery_codes: string[] }>('/api/v1/admin/security/totp/confirm', { method: 'POST', body: JSON.stringify({ setup_token: setupToken.value, code: totpCode.value }) }); recoveryCodes.value = result.recovery_codes; twoFactor.value = true; setupToken.value = ''; status.value = text('两步验证已启用，请立即保存恢复码。', 'Two-factor authentication enabled. Save the recovery codes now.') }
async function disableTOTP() { await api('/api/v1/admin/security/totp', { method: 'DELETE', body: JSON.stringify({ password: disablePassword.value }) }); twoFactor.value = false; disablePassword.value = ''; status.value = text('两步验证已关闭。', 'Two-factor authentication disabled.') }
async function changePassword() { await api('/api/v1/admin/security/password', { method: 'PUT', body: JSON.stringify({ current_password: currentPassword.value, new_password: newPassword.value }) }); currentPassword.value = ''; newPassword.value = ''; status.value = text('密码已更换。', 'Password changed.') }
async function createKey() { const result = await api<{ token: string }>('/api/v1/admin/api-keys', { method: 'POST', body: JSON.stringify({ name: keyName.value, expires_in_days: keyDays.value }) }); revealedKey.value = result.token; await load() }
async function revokeKey(key: APIKey) { if (confirm(text(`撤销 API Key“${key.name}”？`, `Revoke API key “${key.name}”?`))) { await api(`/api/v1/admin/api-keys/${key.id}`, { method: 'DELETE' }); await load() } }
async function revokeSession(session: Session) { await api(`/api/v1/admin/security/sessions/${encodeURIComponent(session.id)}`, { method: 'DELETE' }); if (session.current) location.href = '/login'; else await load() }
async function revokeOthers() { await api('/api/v1/admin/security/sessions/revoke-others', { method: 'POST' }); await load() }
async function createShare() { const expires = new Date(shareExpiry.value).toISOString(); const result = await api<{ url: string }>('/api/v1/admin/share-links', { method: 'POST', body: JSON.stringify({ node_ids: shareNodes.value, expires_at: expires }) }); revealedShare.value = result.url; await load() }
async function revokeShare(link: ShareLink) { await api(`/api/v1/admin/share-links/${link.id}`, { method: 'DELETE' }); await load() }
async function act(action: () => Promise<void>) { error.value = ''; status.value = ''; try { await action() } catch (reason) { error.value = reason instanceof Error ? reason.message : text('安全设置操作失败。', 'Security operation failed.') } }
onMounted(() => act(load))
</script>

<template><section class="admin-page"><header class="page-title"><div><span class="eyebrow">{{ text('账号与会话', 'Identity and sessions') }}</span><h1>{{ text('访问安全', 'Access security') }}</h1><p>{{ text('管理两步验证、密码、API Key、分享链接和登录会话。', 'Manage two-factor authentication, password, API keys, share links, and sessions.') }}</p></div><span class="page-code">2FA / {{ twoFactor ? text('已开启', 'ON') : text('未开启', 'OFF') }}</span></header><div v-if="status" class="notice success"><b>{{ text('安全设置', 'Security') }}</b><span>{{ status }}</span></div><div v-if="error" class="notice error"><b>{{ text('安全设置', 'Security') }}</b><span>{{ error }}</span></div><div class="security-grid"><section class="settings-section"><header><b>01</b><div><h2>{{ text('两步验证', 'Two-factor authentication') }}</h2><p>{{ text('TOTP 密钥使用服务端主密钥加密保存。', 'The TOTP secret is encrypted with the server master key.') }}</p></div></header><div class="security-body"><template v-if="!twoFactor && !setupToken"><button class="secondary-action" @click="act(beginTOTP)">{{ text('开始设置 TOTP', 'Set up TOTP') }}</button></template><template v-else-if="setupToken"><div class="secret-once"><span>{{ text('密钥', 'Secret') }}</span><code>{{ totpSecret }}</code><a :href="totpURL">{{ text('打开验证器链接', 'Open authenticator URI') }} ↗</a></div><form class="inline-pin-form" @submit.prevent="act(confirmTOTP)"><label><span>{{ text('输入 6 位验证码', 'Enter 6-digit code') }}</span><input v-model="totpCode" inputmode="numeric" required /></label><button class="primary-action">{{ text('确认', 'Confirm') }}</button></form></template><template v-else><form class="inline-pin-form" @submit.prevent="act(disableTOTP)"><label><span>{{ text('输入密码以关闭', 'Password to disable') }}</span><input v-model="disablePassword" type="password" required /></label><button class="secondary-action">{{ text('关闭 TOTP', 'Disable TOTP') }}</button></form></template><div v-if="recoveryCodes.length" class="recovery-codes"><b>{{ text('恢复码（仅显示一次）', 'Recovery codes (shown once)') }}</b><code v-for="code in recoveryCodes" :key="code">{{ code }}</code></div></div></section><section class="settings-section"><header><b>02</b><div><h2>{{ text('管理员密码', 'Password') }}</h2><p>{{ text('密码使用 Argon2id 哈希保存。', 'The administrator password is protected with Argon2id.') }}</p></div></header><form class="security-body form-grid" @submit.prevent="act(changePassword)"><label><span>{{ text('当前密码', 'Current password') }}</span><input v-model="currentPassword" type="password" required /></label><label><span>{{ text('新密码（至少 12 位）', 'New password (12+ characters)') }}</span><input v-model="newPassword" type="password" minlength="12" required /></label><button class="secondary-action">{{ text('更换密码', 'Change password') }}</button></form></section></div><section class="settings-section security-wide"><header><b>03</b><div><h2>API Keys</h2><p>{{ text('用于受控的后台 API 访问，可单独撤销。', 'Use for controlled admin API access; each key can be revoked independently.') }}</p></div></header><div class="security-body"><form class="access-create" @submit.prevent="act(createKey)"><label><span>{{ text('名称', 'Key name') }}</span><input v-model="keyName" required /></label><label><span>{{ text('有效天数（0 表示永久）', 'Expiry in days (0 never)') }}</span><input v-model.number="keyDays" type="number" min="0" max="3650" /></label><button class="primary-action">{{ text('创建 Key', 'Create key') }}</button></form><div v-if="revealedKey" class="secret-once"><span>{{ text('新 API Key（请立即复制）', 'New API key (copy now)') }}</span><code>{{ revealedKey }}</code></div><div class="access-list"><div v-for="key in keys" :key="key.id"><div><b>{{ key.name }}</b><small>{{ text('创建', 'Created') }} {{ dateTime(key.created_at) }} · {{ text('最后使用', 'Last used') }} {{ dateTime(key.last_used_at) }}</small></div><span>{{ key.expires_at ? `${text('到期', 'EXP')} ${dateTime(key.expires_at)}` : text('永不过期', 'No expiry') }}</span><button class="danger" @click="act(() => revokeKey(key))">{{ text('撤销', 'Revoke') }}</button></div></div></div></section><section class="settings-section security-wide"><header><b>04</b><div><h2>{{ text('分享链接', 'Share links') }}</h2><p>{{ text('为指定节点创建有时效的只读访问链接。', 'Create time-limited read-only access for selected nodes.') }}</p></div></header><div class="security-body"><form class="access-create share-create" @submit.prevent="act(createShare)"><label><span>{{ text('节点', 'Nodes') }}</span><select v-model="shareNodes" multiple required><option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option></select></label><label><span>{{ text('到期时间', 'Expires at') }}</span><input v-model="shareExpiry" type="datetime-local" required /></label><button class="primary-action">{{ text('创建分享', 'Create share') }}</button></form><div v-if="revealedShare" class="secret-once"><span>{{ text('分享地址（请立即复制）', 'Share URL (copy now)') }}</span><code>{{ revealedShare }}</code></div><div class="access-list"><div v-for="link in shares" :key="link.id"><div><b>{{ text(`${link.node_ids.length} 个节点`, `${link.node_ids.length} nodes`) }}</b><small>{{ link.id }}</small></div><span :class="{ danger: link.revoked_at }">{{ link.revoked_at ? text('已撤销', 'Revoked') : `${text('到期', 'EXP')} ${dateTime(link.expires_at)}` }}</span><button v-if="!link.revoked_at" class="danger" @click="act(() => revokeShare(link))">{{ text('撤销', 'Revoke') }}</button></div></div></div></section><section class="settings-section security-wide"><header><b>05</b><div><h2>{{ text('浏览器会话', 'Browser sessions') }}</h2><p>{{ text('查看并撤销已登录的管理员会话。', 'Review and revoke signed-in administrator sessions.') }}</p></div><button class="secondary-action" @click="act(revokeOthers)">{{ text('撤销其他会话', 'Revoke others') }}</button></header><div class="access-list security-body"><div v-for="item in sessions" :key="item.id"><div><b>{{ item.current ? text('当前会话', 'This session') : item.ip_address }}</b><small>{{ item.user_agent }} · {{ dateTime(item.created_at) }}</small></div><span>{{ text('到期', 'EXP') }} {{ dateTime(item.expires_at) }}</span><button class="danger" @click="act(() => revokeSession(item))">{{ text('撤销', 'Revoke') }}</button></div></div></section></section></template>
