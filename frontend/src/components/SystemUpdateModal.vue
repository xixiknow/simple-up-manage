<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { UiAlert, UiButton, UiTag, useMessage } from '@/components/ui'
import { useSystemStore } from '@/stores/system'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (e: 'update:show', value: boolean): void }>()

const message = useMessage()
const system = useSystemStore()

const view = ref<'confirm' | 'progress'>('confirm')

const STEP_ORDER = ['pulling', 'creating', 'waiting', 'switching'] as const
const STEP_LABELS: Record<string, string> = {
  pulling: '拉取新镜像',
  creating: '创建新容器',
  waiting: '等待新实例就绪',
  switching: '切换流量',
}
const PHASE_LABELS: Record<string, string> = {
  idle: '空闲',
  pulling: STEP_LABELS.pulling,
  creating: STEP_LABELS.creating,
  waiting: STEP_LABELS.waiting,
  switching: STEP_LABELS.switching,
  done: '更新完成',
  failed: '更新失败',
}

const activePhase = computed(() => system.status?.phase ?? 'idle')
const updating = computed(() => system.updating)
const stepIndex = computed(() => {
  const idx = (STEP_ORDER as readonly string[]).indexOf(activePhase.value)
  if (activePhase.value === 'done') return STEP_ORDER.length
  return idx
})

const targetText = computed(() => {
  const t = system.info?.target_version
  return t && t !== system.info?.version ? t : 'latest'
})

const currentVersion = computed(() => system.info?.version || '—')
const rollbackTo = computed(() => system.info?.rollback_to || '')

watch(
  () => props.show,
  async (show) => {
    if (!show) return
    await Promise.all([system.fetchVersion(), system.fetchStatus()])
    view.value = updating.value ? 'progress' : 'confirm'
  },
)

let timer: number | null = null
function stopPoll() {
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
}
function startPoll() {
  stopPoll()
  timer = window.setInterval(() => void system.fetchStatus(), 2000)
}
watch(
  [() => props.show, view],
  ([show, v]) => {
    if (show && (v === 'progress' || updating.value)) startPoll()
    else stopPoll()
  },
  { immediate: true },
)
watch(
  () => system.status?.phase,
  (phase, prev) => {
    if (phase === 'done' && prev !== 'done') {
      void system.fetchVersion()
      message.success('更新完成，请刷新页面使用新版本')
    }
    if (phase === 'failed' && prev !== 'failed') {
      message.error('更新失败：' + (system.status?.message || '未知错误'), { duration: 6000 })
    }
  },
)
onBeforeUnmount(stopPoll)

async function start(target: string) {
  const ok = await system.startUpdate(target)
  if (ok) {
    view.value = 'progress'
    startPoll()
  } else if (system.error) {
    message.error(system.error)
  }
}

function close() {
  emit('update:show', false)
}

function refresh() {
  window.location.reload()
}
</script>

<template>
  <UiModal :show="show" title="系统更新" :width="560" @update:show="emit('update:show', $event)">
    <!-- 确认视图 -->
    <template v-if="view === 'confirm'">
      <div class="version-compare">
        <div class="version-cell">
          <small>当前版本</small>
          <strong class="mono">{{ currentVersion }}</strong>
        </div>
        <template v-if="system.updateAvailable">
          <span class="version-arrow">→</span>
          <div class="version-cell">
            <small>目标版本</small>
            <strong class="mono">{{ targetText }}</strong>
          </div>
        </template>
      </div>

      <UiAlert v-if="system.info && !system.info.can_self_update" type="warning" title="当前环境不支持在线更新">
        {{ system.info.unsupported_reason || '需要蓝绿部署环境（deploy/compose.prod.yml）' }}
      </UiAlert>
      <UiAlert v-else-if="system.info?.check_error" type="warning" title="版本检查未成功">
        {{ system.info.check_error }}
      </UiAlert>
      <UiAlert v-else-if="!system.updateAvailable && system.info?.checked_at" type="info" title="已是最新版本">
        当前无可用更新;新版本发布后,这里会出现「开始更新」按钮。如需强制重新部署当前最新镜像,可使用下方按钮。
      </UiAlert>

      <p class="update-note">
        更新以蓝绿方式执行：先启动新版本实例并通过健康检查，再原子切换流量，<b>全程服务不中断</b>；旧实例将继续处理完在途请求（最长约 5 分钟）后退出。新实例就绪通常需要 1-2 分钟。
      </p>

      <div v-if="rollbackTo" class="rollback-row">
        <span>上一版本可用：<b class="mono">{{ rollbackTo }}</b></span>
        <UiButton size="small" :disabled="!system.canSelfUpdate || updating" @click="start('previous')">
          回滚到此版本
        </UiButton>
      </div>
    </template>

    <!-- 进度视图 -->
    <template v-else>
      <div class="phase-line">
        <UiTag :type="activePhase === 'failed' ? 'error' : activePhase === 'done' ? 'success' : 'info'">
          {{ PHASE_LABELS[activePhase] || activePhase }}
        </UiTag>
        <span class="phase-message">{{ system.status?.message }}</span>
      </div>

      <ol class="phase-steps">
        <li
          v-for="(step, i) in STEP_ORDER"
          :key="step"
          :class="{ done: stepIndex > i || activePhase === 'done', active: stepIndex === i && activePhase !== 'done' }"
        >
          <span class="step-dot" />
          {{ STEP_LABELS[step] }}
        </li>
      </ol>

      <UiAlert v-if="activePhase === 'failed'" type="error" title="更新失败">
        {{ system.status?.message }} 旧实例未受影响，可关闭后重试。
      </UiAlert>
      <UiAlert v-else-if="activePhase === 'done'" type="success" title="更新完成">
        新版本已接管流量，旧实例正在后台排空退出。刷新页面即用新版本。
      </UiAlert>

      <pre v-if="system.status?.log_tail?.length" class="update-log">{{ system.status.log_tail.join('\n') }}</pre>
    </template>

    <template #footer>
      <template v-if="view === 'confirm'">
        <UiButton @click="close">取消</UiButton>
        <UiButton
          v-if="system.updateAvailable"
          type="primary"
          :loading="system.starting"
          :disabled="!system.canSelfUpdate"
          @click="start('latest')"
        >
          开始更新
        </UiButton>
        <UiButton
          v-else
          :disabled="!system.canSelfUpdate || system.starting"
          @click="start('latest')"
        >
          重新部署最新镜像
        </UiButton>
      </template>
      <template v-else-if="activePhase === 'done'">
        <UiButton @click="close">关闭</UiButton>
        <UiButton type="primary" @click="refresh">刷新页面</UiButton>
      </template>
      <template v-else>
        <UiButton :disabled="updating" @click="close">{{ activePhase === 'failed' ? '关闭' : '后台运行' }}</UiButton>
      </template>
    </template>
  </UiModal>
</template>

<style scoped>
.version-compare {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 14px;
}
.version-cell {
  flex: 1;
  padding: 12px 14px;
  border: 1px solid var(--line, #e1e7df);
  border-radius: 10px;
  background: #f7faf4;
}
.version-cell small {
  display: block;
  font-size: 10px;
  letter-spacing: 1px;
  color: #7c8d81;
  margin-bottom: 5px;
}
.version-cell strong {
  font-size: 15px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.version-arrow {
  color: #9db5a5;
  font-size: 17px;
  flex: none;
}
.update-note {
  font-size: 12px;
  line-height: 1.8;
  color: #546c58;
  margin: 12px 0 0;
}
.rollback-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px dashed var(--line, #e1e7df);
  border-radius: 8px;
  font-size: 12px;
  color: #546c58;
}
.phase-line {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.phase-message {
  font-size: 12px;
  color: #546c58;
  min-width: 0;
}
.phase-steps {
  list-style: none;
  margin: 0 0 12px;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.phase-steps li {
  display: flex;
  align-items: center;
  gap: 9px;
  font-size: 13px;
  color: #9aa89e;
}
.phase-steps li .step-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #d7ded4;
  flex: none;
}
.phase-steps li.active {
  color: #234435;
  font-weight: 600;
}
.phase-steps li.active .step-dot {
  background: #9bc177;
  animation: pulse 1.2s ease-in-out infinite;
}
.phase-steps li.done {
  color: #546c58;
}
.phase-steps li.done .step-dot {
  background: #4f9d6b;
}
@keyframes pulse {
  50% {
    opacity: 0.35;
  }
}
.update-log {
  max-height: 130px;
  overflow: auto;
  margin: 0;
  padding: 9px 11px;
  border-radius: 8px;
  background: #12271f;
  color: #b7d0ba;
  font-size: 11px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
