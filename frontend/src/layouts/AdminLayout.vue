<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { CloseOutline, KeyOutline, ListOutline, LogOutOutline, MenuOutline, PulseOutline, ShuffleOutline, ServerOutline, SparklesOutline, ChevronBackOutline, ChevronForwardOutline } from '@/components/ui/icons'
import NoticeInbox from '@/components/NoticeInbox.vue'
import { useAuthStore } from '@/stores/auth'
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const navOpen = ref(false)
const ASIDE_COLLAPSED_KEY = 'console.asideCollapsed'
const asideCollapsed = ref(localStorage.getItem(ASIDE_COLLAPSED_KEY) === '1')
function toggleAside() {
  asideCollapsed.value = !asideCollapsed.value
  localStorage.setItem(ASIDE_COLLAPSED_KEY, asideCollapsed.value ? '1' : '0')
}
const navItems = [
  { label: '仪表盘', key: '/dashboard', icon: PulseOutline },
  { label: '提供商', key: '/upstreams', icon: ServerOutline },
  { label: 'API 密钥', key: '/api-keys', icon: KeyOutline },
  { label: '调度', key: '/scheduler', icon: ShuffleOutline },
  { label: '测智', key: '/intel', icon: SparklesOutline },
  { label: '请求记录', key: '/logs', icon: ListOutline },
]
const pageTitle = computed(() => (route.meta.title as string) || '控制台')
function go(key: string) { navOpen.value = false; void router.push(key) }
function logout() { auth.logout(); void router.push('/login') }
</script>

<template>
  <div class="console-shell" :class="{ collapsed: asideCollapsed }">
    <aside class="console-aside">
      <div class="brand"><span>供</span><div>供货商管理<small>ADMIN CONSOLE</small></div></div>
      <div class="nav-label">工作台 / WORKSPACE</div>
      <nav aria-label="主导航">
        <button v-for="item in navItems" :key="item.key" type="button" :title="asideCollapsed ? item.label : undefined" :class="{ selected: route.path === item.key }" :aria-current="route.path === item.key ? 'page' : undefined" @click="go(item.key)">
          <component :is="item.icon" class="nav-icon" /><span class="nav-text">{{ item.label }}</span><ChevronForwardOutline v-if="route.path === item.key" class="nav-chevron" />
        </button>
      </nav>
      <div class="aside-bottom"><span class="live-dot" />内部运维<small>管理时区 · Asia/Shanghai</small></div>
      <button class="aside-collapse" type="button" :aria-expanded="!asideCollapsed" :aria-label="asideCollapsed ? '展开菜单' : '收起菜单'" @click="toggleAside">
        <ChevronForwardOutline v-if="asideCollapsed" class="nav-icon" /><ChevronBackOutline v-else class="nav-icon" /><span class="nav-text">收起菜单</span>
      </button>
    </aside>
    <main class="console-main">
      <header class="console-header">
        <button class="nav-toggle" type="button" aria-label="打开导航" @click="navOpen = true"><MenuOutline /></button>
        <div class="header-brand brand"><span>供</span><div>供货商管理<small>ADMIN CONSOLE</small></div></div>
        <div class="breadcrumb"><span class="crumb-root">管理控制台</span><ChevronForwardOutline class="crumb-root" />{{ pageTitle }}</div>
        <div class="header-right"><NoticeInbox /><button class="logout" type="button" @click="logout"><LogOutOutline />退出</button></div>
      </header>
      <div class="console-content"><router-view /></div>
    </main>
    <UiDrawer :show="navOpen" width="min(300px, 84vw)" @update:show="navOpen = $event">
      <div class="nav-drawer">
        <div class="nav-drawer-head">
          <div class="brand"><span>供</span><div>供货商管理<small>ADMIN CONSOLE</small></div></div>
          <button class="nav-drawer-close" type="button" aria-label="关闭导航" @click="navOpen = false"><CloseOutline /></button>
        </div>
        <nav aria-label="主导航">
          <button v-for="item in navItems" :key="item.key" type="button" :class="{ selected: route.path === item.key }" :aria-current="route.path === item.key ? 'page' : undefined" @click="go(item.key)">
            <component :is="item.icon" class="nav-icon" />{{ item.label }}<ChevronForwardOutline v-if="route.path === item.key" class="nav-chevron" />
          </button>
        </nav>
        <div class="aside-bottom"><span class="live-dot" />内部运维<small>管理时区 · Asia/Shanghai</small></div>
      </div>
    </UiDrawer>
  </div>
</template>

<style scoped>
.console-shell {
  --aside-w:224px;
  display:flex;
  min-height:100vh}
.console-shell.collapsed {
  --aside-w:64px}
.console-aside {
  width:var(--aside-w);
  position:fixed;
  inset:0 auto 0 0;
  background:#153d32;
  color:#c0d0c5;
  display:flex;
  flex-direction:column;
  padding:31px 18px;
  z-index:20;
  transition:width .25s ease, padding .25s ease}
.brand {
  display:flex;
  align-items:center;
  gap:12px;
  font-size:18px;
  font-weight:650;
  letter-spacing:1px}
.brand>span {
  display:grid;
  place-items:center;
  background:#d9e8b3;
  color:#244734;
  width:39px;
  height:39px;
  flex:none;
  border-radius:11px;
  font-family:serif;
  font-size:26px}
.brand small {
  font-size:8px;
  letter-spacing:1.7px;
  display:block;
  font-weight:400;
  margin-top:5px;
  color:#9db5a5}
.nav-label {
  font-size:10px;
  letter-spacing:1.5px;
  margin:49px 14px 16px;
  color:#81a18e}
nav {
  display:flex;
  flex-direction:column;
  gap:7px}
nav button {
  justify-content:flex-start;
  padding:13px 14px;
  border-radius:8px;
  font-size:13px;
  gap:13px}
nav button:not(.selected):hover {
  background:#ffffff0c;
  color:#eef5e9}
nav .selected {
  background:#dbe9ba;
  color:#234435;
  font-weight:650}
nav .selected:hover {
  background:#e5efcc}
nav button:focus-visible {
  outline:2px solid #dbe9ba;
  outline-offset:2px}
.nav-icon {
  width:19px;
  height:19px}
.nav-chevron {
  width:14px;
  height:14px;
  margin-left:auto}
.aside-bottom {
  margin-top:auto;
  padding:25px 12px 0;
  font-size:11px;
  line-height:2}
.aside-bottom small {
  display:block;
  color:#88a392}
.live-dot {
  width:6px;
  height:6px;
  display:inline-block;
  background:#9bc177;
  border-radius:50%;
  margin-right:7px}
.aside-collapse {
  justify-content:flex-start;
  margin-top:14px;
  padding:11px 14px;
  border-radius:8px;
  font-size:12px;
  gap:13px;
  color:#88a392}
.aside-collapse:hover {
  background:#ffffff0c;
  color:#eef5e9}
.console-shell.collapsed .console-aside {
  padding:22px 12px 16px}
.console-shell.collapsed .brand {
  justify-content:center}
.console-shell.collapsed .brand>span {
  width:34px;
  height:34px;
  font-size:24px;
  border-radius:10px}
.console-shell.collapsed .brand>div,
.console-shell.collapsed .nav-label,
.console-shell.collapsed .nav-text,
.console-shell.collapsed .nav-chevron,
.console-shell.collapsed .aside-bottom {
  display:none}
.console-shell.collapsed nav {
  margin-top:30px}
.console-shell.collapsed nav button,
.console-shell.collapsed .aside-collapse {
  justify-content:center;
  padding-inline:0}
.console-shell.collapsed .aside-collapse {
  margin-top:auto}
.console-main {
  margin-left:var(--aside-w);
  width:calc(100% - var(--aside-w));
  min-width:0;
  min-height:100vh;
  display:flex;
  flex-direction:column;
  transition:margin-left .25s ease}
.console-header {
  height:76px;
  flex:none;
  border-bottom:1px solid #e1e7df;
  display:flex;
  align-items:center;
  justify-content:space-between;
  padding:0 39px;
  background:#ffffff70}
.breadcrumb,.header-right {
  display:flex;
  align-items:center;
  gap:13px;
  font-size:11px;
  color:#7c8d81}
.breadcrumb svg {
  width:13px;
  height:13px}
.header-right {
  gap:7px}
.logout {
  padding:8px;
  font-size:12px;
  border-radius:7px}
.logout:hover {
  background:#eef3e7}
.logout svg {
  width:16px;
  height:16px}
.console-content {
  padding:36px 39px 55px;
  max-width:1700px;
  width:100%;
  margin:0 auto;
  flex:1;
  min-width:0}
.nav-toggle {
  display:none;
  width:36px;
  height:36px;
  border-radius:8px;
  color:#546c58;
  flex:none}
.nav-toggle:hover {
  background:#eef3e7}
.nav-toggle svg {
  width:20px;
  height:20px}
.header-brand {
  display:none;
  margin-right:auto}
.nav-drawer {
  display:flex;
  flex-direction:column;
  gap:18px;
  height:100%;
  padding:20px 16px;
  overflow:auto;
  background:#153d32;
  color:#c0d0c5}
.nav-drawer .brand {
  font-size:16px}
.nav-drawer .brand>span {
  width:32px;
  height:32px;
  font-size:22px}
.nav-drawer-head {
  display:flex;
  align-items:center;
  justify-content:space-between;
  gap:10px;
  flex:none}
.nav-drawer-close {
  width:34px;
  height:34px;
  border-radius:8px;
  color:#9db5a5;
  flex:none}
.nav-drawer-close:hover {
  background:#ffffff14;
  color:#eef5e9}
.nav-drawer-close svg {
  width:18px;
  height:18px}
.nav-drawer .aside-bottom {
  padding:20px 12px 6px}
@media(max-width:1100px) {
  .console-shell {
  --aside-w:190px}
.console-aside {
  padding-inline:14px}
.brand {
  font-size:16px;
  gap:9px}
.console-content {
  padding:28px 24px}
.console-header {
  padding:0 24px}
}
@media(max-width:760px) {
  .console-shell {
  display:block}
.console-aside {
  display:none}
.console-main {
  margin:0;
  width:100%;
  min-height:0}
.console-header {
  position:sticky;
  top:0;
  z-index:19;
  height:55px;
  padding:0 12px;
  gap:10px;
  background:#ffffff}
.nav-toggle {
  display:inline-flex}
.header-brand {
  display:flex;
  font-size:16px;
  gap:9px}
.header-brand>span {
  width:30px;
  height:30px;
  font-size:21px}
.header-brand small {
  display:none}
.breadcrumb {
  display:none}
.console-content {
  padding:16px 12px 40px}
}

</style>
