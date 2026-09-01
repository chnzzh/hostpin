<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, apiDownload, unwrap } from '../../api'
import { bytes as formatBytes } from '../../format'
import { useLocale } from '../../i18n'

interface BackupStatus {
  driver: string
  available: boolean
  encrypted: boolean
  format_version: number
  pending_restore: boolean
  external_master_key: boolean
  maximum_bytes: number
}

interface RestoreAccepted {
  accepted: boolean
  restart: string
  created_at: string
  source_version: string
  sessions_revoked: boolean
}

const backupStatus = ref<BackupStatus | null>(null)
const busy = ref<'export' | 'import' | ''>('')
const message = ref('')
const error = ref('')
const exportPassword = ref('')
const exportPassphrase = ref('')
const exportConfirmation = ref('')
const importPassword = ref('')
const importPassphrase = ref('')
const restoreConfirmation = ref('')
const backupFile = ref<File | null>(null)
const dragging = ref(false)
const restarting = ref(false)
const { text } = useLocale()

const driverLabel = computed(() => backupStatus.value?.driver.toUpperCase() ?? '—')
const canExport = computed(() => Boolean(
  backupStatus.value?.available && exportPassword.value && exportPassphrase.value.length >= 12 &&
  exportPassphrase.value === exportConfirmation.value && !busy.value,
))
const canImport = computed(() => Boolean(
  backupStatus.value?.available && backupFile.value && importPassword.value &&
  importPassphrase.value.length >= 12 && restoreConfirmation.value === 'RESTORE' && !busy.value,
))

async function load() {
  try {
    backupStatus.value = unwrap(await api<{ data: BackupStatus }>('/api/v1/admin/backups/status'))
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('无法加载备份状态。', 'Backup status could not be loaded.')
  }
}

async function exportBackup() {
  if (!canExport.value) return
  busy.value = 'export'; message.value = ''; error.value = ''
  try {
    const download = await apiDownload('/api/v1/admin/backups/export', {
      method: 'POST',
      body: JSON.stringify({ current_password: exportPassword.value, passphrase: exportPassphrase.value }),
    })
    const url = URL.createObjectURL(download.blob)
    const link = document.createElement('a')
    link.href = url; link.download = download.filename; link.click()
    URL.revokeObjectURL(url)
    message.value = text(`加密备份已导出为 ${download.filename}，请单独保存备份口令。`, `Encrypted backup exported as ${download.filename}. Keep its passphrase separately.`)
    exportPassword.value = ''; exportPassphrase.value = ''; exportConfirmation.value = ''
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('备份导出失败。', 'Backup export failed.')
  } finally {
    busy.value = ''
  }
}

function selectFile(event: Event) {
  const input = event.target as HTMLInputElement
  backupFile.value = input.files?.[0] ?? null
}

function dropFile(event: DragEvent) {
  dragging.value = false
  backupFile.value = event.dataTransfer?.files?.[0] ?? null
}

async function importBackup() {
  if (!canImport.value || !backupFile.value) return
  busy.value = 'import'; message.value = ''; error.value = ''
  const form = new FormData()
  form.append('current_password', importPassword.value)
  form.append('passphrase', importPassphrase.value)
  form.append('confirmation', restoreConfirmation.value)
  form.append('backup', backupFile.value)
  try {
    const result = await api<RestoreAccepted>('/api/v1/admin/backups/import', { method: 'POST', body: form })
    restarting.value = result.accepted
    message.value = text('备份验证通过。Hostpin 正在替换数据并重新加载，所有浏览器会话将退出登录。', 'Backup validated. Hostpin is replacing data and reloading; all browser sessions will be signed out.')
    importPassword.value = ''; importPassphrase.value = ''; restoreConfirmation.value = ''
    await waitForReload()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : text('备份导入失败。', 'Backup import failed.')
    restarting.value = false
  } finally {
    busy.value = ''
  }
}

async function waitForReload() {
  await new Promise((resolve) => window.setTimeout(resolve, 1200))
  for (let attempt = 0; attempt < 120; attempt += 1) {
    try {
      const response = await fetch('/readyz', { cache: 'no-store' })
      if (response.ok) {
        window.location.assign('/login?restored=1')
        return
      }
    } catch {
      // A refused connection is expected while the in-process reload runs.
    }
    await new Promise((resolve) => window.setTimeout(resolve, 500))
  }
  error.value = text('Hostpin 在 60 秒内未恢复，请检查服务日志和恢复前保留文件。', 'Hostpin has not returned after 60 seconds. Inspect the server log and retained pre-restore files.')
  restarting.value = false
}

onMounted(load)
</script>

<template>
  <section class="admin-page backup-page">
    <header class="page-title">
      <div><span class="eyebrow">{{ text('数据迁移', 'Data portability') }}</span><h1>{{ text('备份与恢复', 'Backup & restore') }}</h1><p>{{ text('导出加密备份，或从已验证的备份文件恢复整个站点。', 'Export an encrypted backup or restore the entire site from a verified file.') }}</p></div>
      <span class="page-code">DB / {{ driverLabel }}</span>
    </header>

    <div v-if="message" class="notice success" role="status"><b>{{ text('备份', 'Backup') }}</b><span>{{ message }}</span></div>
    <div v-if="error" class="notice error" role="alert"><b>{{ text('备份', 'Backup') }}</b><span>{{ error }}</span></div>
    <div v-if="backupStatus?.pending_restore" class="notice warning"><b>{{ text('等待恢复', 'Restore pending') }}</b><span>{{ text('已验证的备份正在等待服务重新加载。', 'A validated restore is waiting for the service reload.') }}</span></div>

    <div v-if="backupStatus" class="backup-status-strip">
      <div><span>{{ text('数据库', 'Database') }}</span><b>{{ driverLabel }}</b><small>{{ backupStatus.available ? text('支持在线快照', 'Online snapshot ready') : text('请使用数据库原生工具', 'Use external database tools') }}</small></div>
      <div><span>{{ text('备份格式', 'Backup format') }}</span><b>HPBK / V{{ backupStatus.format_version }}</b><small>{{ backupStatus.encrypted ? 'ARGON2ID + AES-256-GCM' : text('未加密', 'Unencrypted') }}</small></div>
      <div><span>{{ text('最大导入大小', 'Import limit') }}</span><b>{{ formatBytes(backupStatus.maximum_bytes) }}</b><small>{{ text('解密后数据', 'Decrypted payload') }}</small></div>
      <div><span>{{ text('主密钥', 'Master key') }}</span><b>{{ backupStatus.external_master_key ? text('外部提供', 'External') : text('文件保存', 'File-backed') }}</b><small>{{ backupStatus.external_master_key ? text('必须与备份一致', 'Must match archive') : text('随站点恢复', 'Restored with site') }}</small></div>
    </div>

    <div v-if="backupStatus?.available" class="backup-transfer-grid">
      <section class="backup-operation export-operation">
        <header><span>01 / {{ text('导出', 'Export') }}</span><div><h2>{{ text('导出加密备份', 'Export encrypted backup') }}</h2><p>{{ text('包含 SQLite 数据库、主密钥、主题文件和所有持久化设置。', 'Includes the SQLite database, master key, themes, and all durable settings.') }}</p></div></header>
        <div class="backup-payload-map" :aria-label="text('备份内容', 'Backup contents')"><span>SQLITE</span><i>+</i><span>MASTER.KEY</span><i>+</i><span>THEMES</span><b>→ {{ text('加密', 'Encrypt') }}</b></div>
        <form class="backup-form" @submit.prevent="exportBackup">
          <label><span>{{ text('当前管理员密码', 'Current admin password') }}</span><input v-model="exportPassword" type="password" autocomplete="current-password" required /></label>
          <label><span>{{ text('备份口令（至少 12 位）', 'Backup passphrase (12+)') }}</span><input v-model="exportPassphrase" type="password" autocomplete="new-password" minlength="12" required /><small>{{ text('用于加密下载文件，Hostpin 不会保存此口令。', 'This encrypts the download and is never stored by Hostpin.') }}</small></label>
          <label><span>{{ text('再次输入备份口令', 'Repeat backup passphrase') }}</span><input v-model="exportConfirmation" type="password" autocomplete="new-password" minlength="12" required /><small v-if="exportConfirmation && exportPassphrase !== exportConfirmation" class="field-error">{{ text('两次口令不一致。', 'Passphrases do not match.') }}</small></label>
          <button class="primary-action" :disabled="!canExport">{{ busy === 'export' ? text('正在生成备份…', 'Creating backup…') : text('导出加密备份', 'Export encrypted backup') }}</button>
        </form>
        <footer><b>{{ text('在线导出', 'Online export') }}</b><span>{{ text('导出期间探针可以继续上报。', 'Agents continue reporting during export.') }}</span></footer>
      </section>

      <section class="backup-operation restore-operation">
        <header><span>02 / {{ text('恢复', 'Restore') }}</span><div><h2>{{ text('导入并替换当前数据', 'Import and replace') }}</h2><p>{{ text('替换前会验证加密、校验和、数据库结构和 SQLite 完整性。', 'Encryption, checksums, schema, and SQLite integrity are verified before replacement.') }}</p></div></header>
        <label class="file-drop backup-drop" :class="{ dragging, selected: backupFile }" @dragenter.prevent="dragging = true" @dragover.prevent @dragleave.prevent="dragging = false" @drop.prevent="dropFile">
          <input type="file" accept=".hostpin-backup,application/vnd.hostpin.backup" @change="selectFile" />
          <span>{{ backupFile ? text('已选择备份文件', 'Backup loaded') : text('拖入备份文件或点击选择', 'Drop backup or click to select') }}</span>
          <b>{{ backupFile?.name || text('未选择文件', 'No file selected') }}</b>
          <small>{{ backupFile ? formatBytes(backupFile.size) : text('仅支持 .hostpin-backup 加密文件，不接受 ZIP', 'Encrypted .hostpin-backup files only; ZIP is rejected') }}</small>
        </label>
        <form class="backup-form danger-form" @submit.prevent="importBackup">
          <label><span>{{ text('备份口令', 'Backup passphrase') }}</span><input v-model="importPassphrase" type="password" autocomplete="off" minlength="12" required /></label>
          <label><span>{{ text('当前管理员密码', 'Current admin password') }}</span><input v-model="importPassword" type="password" autocomplete="current-password" required /></label>
          <label><span>{{ text('输入 RESTORE 确认替换本站数据', 'Type RESTORE to replace this site') }}</span><input v-model="restoreConfirmation" class="restore-confirmation" autocomplete="off" placeholder="RESTORE" required /></label>
          <button class="primary-action danger-action" :disabled="!canImport">{{ busy === 'import' ? text('正在验证备份…', 'Verifying backup…') : restarting ? text('正在重新加载…', 'Reloading…') : text('导入并替换', 'Import and replace') }}</button>
        </form>
        <footer><b>{{ text('保留回滚文件', 'Rollback retained') }}</b><span>{{ text('当前数据库、主密钥和主题会以带时间戳的恢复前文件保留。', 'The current database, master key, and themes are kept as timestamped pre-restore files.') }}</span></footer>
      </section>
    </div>

    <section v-else-if="backupStatus" class="postgres-backup-guide">
      <header><span>POSTGRESQL</span><h2>{{ text('使用 PostgreSQL 原生备份', 'Use native PostgreSQL backups') }}</h2><p>{{ text('PostgreSQL 模式不提供一键替换，请使用 pg_dump 和 pg_restore。', 'One-click replacement is unavailable in PostgreSQL mode; use pg_dump and pg_restore.') }}</p></header>
      <div><code>pg_dump --format=custom --file hostpin.dump "$HOSTPIN_DB_DSN"</code><code>pg_restore --clean --if-exists --dbname "$HOSTPIN_DB_DSN" hostpin.dump</code></div>
      <p>{{ text('同时单独备份 Hostpin 数据目录中的', 'Also back up') }} <code>master.key</code> {{ text('和', 'and') }} <code>themes/</code>。</p>
    </section>

    <div class="backup-safety-ledger">
      <span>{{ text('恢复影响', 'Restore effects') }}</span><b>{{ text('所有会话将被撤销', 'All sessions revoked') }}</b><b>{{ text('保留探针令牌', 'Agent tokens preserved') }}</b><b>{{ text('不替换配置和 TLS', 'Config / TLS not replaced') }}</b><b>{{ text('保留旧数据', 'Previous data retained') }}</b>
    </div>
  </section>
</template>
