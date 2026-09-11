<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { Component } from 'vue'
import { NIcon } from 'naive-ui'
import {
  KeyOutline,
  ListOutline,
  LogOutOutline,
  ShuffleOutline,
  ServerOutline,
} from '@vicons/ionicons5'
import RateNoticeInbox from '@/components/RateNoticeInbox.vue'
import { useAuthStore } from '@/stores/auth'

function renderIcon(icon: Component) {
  return () => h(NIcon, null, { default: () => h(icon) })
}

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const menuOptions = [
  { label: '提供商', key: '/upstreams', icon: renderIcon(ServerOutline) },
  { label: 'API 密钥', key: '/api-keys', icon: renderIcon(KeyOutline) },
  { label: '调度', key: '/scheduler', icon: renderIcon(ShuffleOutline) },
  { label: '请求记录', key: '/logs', icon: renderIcon(ListOutline) },
]

const STORAGE_KEY = 'sum-sider-collapsed'

function readCollapsed() {
  try {
    return localStorage.getItem(STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

const collapsed = ref(readCollapsed())
const activeKey = computed(() => route.path)
const pageTitle = computed(() => (route.meta.title as string) || '控制台')

function onCollapse(value: boolean) {
  collapsed.value = value
  try {
    localStorage.setItem(STORAGE_KEY, value ? '1' : '0')
  } catch {
    /* ignore quota / private mode */
  }
}

function onMenu(key: string) {
  void router.push(key)
}

function logout() {
  auth.logout()
  void router.push('/login')
}
</script>

<template>
  <n-layout has-sider class="shell">
    <n-layout-sider
      bordered
      collapse-mode="width"
      :collapsed="collapsed"
      :collapsed-width="64"
      :width="216"
      show-trigger
      :native-scrollbar="false"
      content-style="display:flex;flex-direction:column;height:100%"
      style="background: #10161c"
      @update:collapsed="onCollapse"
    >
      <div class="brand" :class="{ collapsed }">
        <span class="mark" />
        <div v-if="!collapsed">
          <strong>供货商管理</strong>
          <small>Admin Console</small>
        </div>
      </div>
      <n-menu
        :value="activeKey"
        :options="menuOptions"
        :collapsed="collapsed"
        :root-indent="16"
        :indent="16"
        inverted
        @update:value="onMenu"
      />
      <div v-if="!collapsed" class="sider-foot">内部运维 · v1</div>
    </n-layout-sider>
    <n-layout>
      <n-layout-header bordered class="topbar">
        <div class="crumb">{{ pageTitle }}</div>
        <div class="top-actions">
          <RateNoticeInbox />
          <n-button quaternary size="small" @click="logout">
            <template #icon>
              <n-icon><LogOutOutline /></n-icon>
            </template>
            退出
          </n-button>
        </div>
      </n-layout-header>
      <n-layout-content class="content" :native-scrollbar="false">
        <router-view />
      </n-layout-content>
    </n-layout>
  </n-layout>
</template>

<style scoped>
.shell {
  height: 100vh;
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 16px 14px;
  color: #e8eef4;
}
.brand.collapsed {
  justify-content: center;
  padding: 18px 8px 14px;
}
.brand strong {
  display: block;
  font-size: 14px;
  letter-spacing: 0.04em;
}
.brand small {
  color: #8b98a5;
  font-size: 11px;
}
.mark {
  width: 10px;
  height: 22px;
  border-radius: 2px;
  background: linear-gradient(180deg, #2dd4bf, #0f766e);
}
.sider-foot {
  margin-top: auto;
  padding: 12px 16px 16px;
  color: #64748b;
  font-size: 11px;
}
.topbar {
  height: 48px;
  padding: 0 18px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #f7f9fb;
}
.crumb {
  font-size: 14px;
  font-weight: 600;
}
.top-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}
.content {
  padding: 16px 18px 24px;
  background: #e8edf2;
}
:deep(.n-menu) {
  background: transparent;
}
:deep(.n-layout-sider) {
  color: #cbd5e1;
}
:deep(.n-layout-toggle-bar) {
  background: #1f2937;
}
</style>
