<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { getIntelResultOutput, listIntelResults, type IntelPlanItem, type IntelTestResult } from '@/api/intel'
import { extractPelicanHtml } from '@/utils/pelicanHtml'
import { errText, formatDurationMs, formatTime } from '@/utils/format'

const props = defineProps<{
  plan: IntelPlanItem
  tick: number
}>()

const message = useMessage()

type Artwork = {
  result: IntelTestResult
  html: string
  state: 'idle' | 'loading' | 'ready' | 'failed'
}

const MAX_CARDS = 60
const loading = ref(false)
const error = ref('')
const artworks = ref<Artwork[]>([])
const preview = ref<Artwork | null>(null)
let loadSequence = 0
let observer: IntersectionObserver | null = null
const queue: number[] = []
let inflight = 0

const readyCount = computed(() => artworks.value.filter(a => a.state === 'ready').length)

async function load() {
  const sequence = ++loadSequence
  loading.value = artworks.value.length === 0
  error.value = ''
  try {
    const page = await listIntelResults({ plan_id: props.plan.id, has_output: true, page_size: MAX_CARDS, page: 1 })
    if (sequence !== loadSequence) return
    // Keep already-fetched artwork bodies across refreshes; only new cards load.
    const previous = new Map(artworks.value.map(a => [a.result.id, a]))
    artworks.value = (page.items ?? []).map(result => {
      const prev = previous.get(result.id)
      return prev ? { result, html: prev.html, state: prev.state } : { result, html: '', state: 'idle' as const }
    })
    resetObserver()
  } catch (e) {
    if (sequence === loadSequence) {
      error.value = errText(e, '画廊加载失败')
      artworks.value = []
    }
  } finally {
    if (sequence === loadSequence) loading.value = false
  }
}

function resetObserver() {
  observer?.disconnect()
  queue.length = 0
  inflight = 0
  observer = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (!entry.isIntersecting) continue
      const id = Number((entry.target as HTMLElement).dataset.resultId)
      observer?.unobserve(entry.target)
      const artwork = artworks.value.find(a => a.result.id === id)
      if (artwork && artwork.state === 'idle') {
        artwork.state = 'loading'
        queue.push(id)
      }
    }
    pump()
  }, { rootMargin: '220px 0px' })
}

function registerCard(id: number) {
  return (el: unknown) => {
    const element = el as HTMLElement | null
    if (!element || !observer) return
    element.dataset.resultId = String(id)
    observer.observe(element)
  }
}

function pump() {
  while (inflight < 4 && queue.length > 0) {
    const id = queue.shift()
    if (id === undefined) break
    inflight++
    void getIntelResultOutput(id)
      .then(data => {
        const artwork = artworks.value.find(a => a.result.id === id)
        if (!artwork) return
        const html = extractPelicanHtml(data.output_text)
        if (html) {
          artwork.html = html
          artwork.state = 'ready'
        } else {
          artwork.state = 'failed'
        }
      })
      .catch(e => {
        const artwork = artworks.value.find(a => a.result.id === id)
        if (artwork) artwork.state = 'failed'
        message.error(errText(e, `作品 #${id} 加载失败`))
      })
      .finally(() => {
        inflight--
        pump()
      })
  }
}

function download(artwork: Artwork) {
  const blob = new Blob([artwork.html], { type: 'text/html;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `pelican-${artwork.result.id}.html`
  anchor.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

watch(() => [props.plan.id, props.tick], () => void load(), { immediate: true })

onBeforeUnmount(() => {
  loadSequence++
  observer?.disconnect()
  observer = null
})
</script>

<template>
  <div>
    <div class="gallery-meta">
      <span class="muted">共 {{ artworks.length }} 件作品，已加载 {{ readyCount }} 件 · 点击卡片放大预览</span>
    </div>
    <ui-alert v-if="error" type="error" :bordered="false">{{ error }}</ui-alert>
    <ui-spin v-if="loading" style="display: flex; justify-content: center; padding: 34px 0" />
    <div v-else-if="!artworks.length" class="muted empty-hint">还没有成功的鹈鹕作品，点击「立即测试」发起一轮</div>
    <div v-else class="gallery">
      <div
        v-for="art in artworks"
        :key="art.result.id"
        :ref="registerCard(art.result.id)"
        class="art-card"
        @click="preview = art"
      >
        <div class="art-frame">
          <iframe
            v-if="art.state === 'ready' && art.html"
            :srcdoc="art.html"
            class="art-iframe"
            sandbox="allow-scripts"
            scrolling="no"
            title="鹈鹕作品预览"
          />
          <div v-else-if="art.state === 'failed'" class="art-placeholder">作品加载失败</div>
          <div v-else class="art-placeholder"><ui-spin size="small" /></div>
        </div>
        <div class="art-meta">
          <div class="art-title" :title="`${art.result.upstream_name} · ${art.result.key_name}`">
            {{ art.result.upstream_name }} · {{ art.result.key_name }}
          </div>
          <div class="art-sub">
            <span>{{ art.result.model }}</span>
            <span>{{ formatDurationMs(art.result.latency_ms) }}</span>
            <span>{{ formatTime(art.result.created_at) }}</span>
          </div>
        </div>
      </div>
    </div>

    <ui-modal :show="!!preview" preset="card" title="鹈鹕作品"
      style="width: min(980px, calc(100vw - 24px))" @update:show="preview = null">
      <template v-if="preview">
        <div class="preview-info">
          <span>{{ preview.result.upstream_name }} · {{ preview.result.key_name }}</span>
          <span>{{ preview.result.model }} · {{ preview.result.protocol }}</span>
          <span>耗时 {{ formatDurationMs(preview.result.latency_ms) }} · {{ formatTime(preview.result.created_at) }}</span>
        </div>
        <iframe
          v-if="preview.html"
          :srcdoc="preview.html"
          class="preview-frame"
          sandbox="allow-scripts"
          title="鹈鹕作品预览"
        />
      </template>
      <template #footer>
        <ui-space justify="end">
          <ui-button :disabled="!preview?.html" @click="preview && download(preview)">下载 HTML</ui-button>
          <ui-button type="primary" @click="preview = null">关闭</ui-button>
        </ui-space>
      </template>
    </ui-modal>
  </div>
</template>

<style scoped>
.gallery-meta {
  margin-bottom: 12px;
}
.gallery {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 14px;
}
.art-card {
  background: #ffffff;
  border: 1px solid #e1e7df;
  border-radius: 11px;
  overflow: hidden;
  cursor: zoom-in;
  transition: box-shadow 0.15s ease, transform 0.15s ease;
}
.art-card:hover {
  box-shadow: 0 6px 18px #263b3414;
  transform: translateY(-1px);
}
.art-frame {
  aspect-ratio: 4 / 3;
  background: #f7f9f4;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
}
.art-iframe {
  width: 100%;
  height: 100%;
  border: 0;
  pointer-events: none;
  background: transparent;
}
.art-placeholder {
  color: #819087;
  font-size: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  width: 100%;
}
.art-meta {
  padding: 10px 12px 12px;
  border-top: 1px solid #e1e7df;
}
.art-title {
  font-size: 12.5px;
  font-weight: 600;
  color: #263b34;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.art-sub {
  margin-top: 4px;
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: #819087;
  flex-wrap: wrap;
}
.preview-info {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  font-size: 12px;
  color: #546c58;
  margin-bottom: 10px;
}
.preview-frame {
  width: 100%;
  height: min(62vh, 560px);
  border: 1px solid #e1e7df;
  border-radius: 8px;
  background: #ffffff;
}
.empty-hint {
  padding: 34px 0;
  text-align: center;
}
</style>
