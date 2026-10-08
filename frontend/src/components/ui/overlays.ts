import { computed, defineComponent, h, inject, nextTick, onBeforeUnmount, onMounted, provide, ref, Teleport, useId, watch, type PropType } from 'vue'
import { fieldKey, formKey, UiButton, px } from './controls'
import { ChevronDownOutline } from './icons'

// Native dialog supplies focus trapping, Escape, inert background and focus restoration.
// The card/body/footer structure and visual tokens follow the rotation console.
const closeDrawer = Symbol('close-drawer')
function overlay(drawer = false) {
  return defineComponent({
    name: drawer ? 'UiDrawer' : 'UiModal', inheritAttrs: false,
    props: { show: Boolean, title: String, preset: String, width: [Number, String], placement: String, closable: { type: Boolean, default: true }, maskClosable: { type: Boolean, default: true }, closeOnEsc: { type: Boolean, default: true } },
    emits: { 'update:show': (_show: boolean) => true },
    setup(p, { slots, attrs, emit }) {
      const dialog = ref<HTMLDialogElement>(), id = useId()
      const close = () => { if (p.closable) emit('update:show', false) }
      provide(closeDrawer, close)
      async function sync() { await nextTick(); if (p.show && !dialog.value?.open) dialog.value?.showModal(); else if (!p.show && dialog.value?.open) dialog.value.close() }
      watch(() => p.show, sync)
      onMounted(sync)
      onBeforeUnmount(() => dialog.value?.close())
      return () => h(Teleport, { to: 'body' }, [h('dialog', {
        ref: dialog, class: ['ui-overlay', { 'is-drawer': drawer }], 'aria-labelledby': p.title ? id : undefined, 'aria-label': drawer ? '请求明细' : p.title,
        onCancel: (e: Event) => { e.preventDefault(); if (p.closeOnEsc) close() },
        onClick: (e: MouseEvent) => { if (e.target === dialog.value && p.maskClosable) close() },
      }, p.show ? [h('section', { ...attrs, class: [drawer ? 'ui-drawer drawer' : 'ui-modal modal', attrs.class], style: [p.width ? { width: px(p.width) } : {}, attrs.style as any] }, drawer ? slots.default?.() : [
        h('div', { class: 'modal-head' }, [h('h2', { id }, p.title || slots.header?.()), p.closable ? h('button', { type: 'button', 'aria-label': '关闭弹窗', onClick: close }, '×') : null]),
        h('div', { class: 'modal-body' }, slots.default?.()), slots.footer ? h('div', { class: 'modal-foot' }, slots.footer()) : null,
      ])] : [])])
    },
  })
}
export const UiModal = overlay()
export const UiDrawer = overlay(true)
export const UiDrawerContent = defineComponent({ props: { title: String, closable: Boolean, nativeScrollbar: Boolean }, setup(p, { slots }) { const close = inject<() => void>(closeDrawer)!; return () => h('div', { class: 'ui-drawer-content' }, [h('div', { class: 'modal-head' }, [h('h2', p.title), p.closable ? h('button', { type: 'button', 'aria-label': '关闭明细', onClick: close }, '×') : null]), h('div', { class: 'drawer-body' }, slots.default?.()), slots.footer ? h('div', { class: 'modal-foot' }, slots.footer()) : null]) } })

export const UiPopover = defineComponent({
  props: { show: { type: Boolean, default: undefined }, trigger: String, placement: { type: String, default: 'bottom-start' }, disabled: Boolean, raw: Boolean, showArrow: Boolean },
  emits: { 'update:show': (_show: boolean) => true },
  setup(p, { slots, emit, expose }) {
    const panel = ref<HTMLElement>(), anchor = ref<HTMLElement>(), active = ref(false), id = useId()
    const state = computed(() => p.show ?? active.value)
    function update(show: boolean) { if (show && p.disabled) return; active.value = show; emit('update:show', show) }
    function position() {
      if (!anchor.value || !panel.value) return
      const a = anchor.value.getBoundingClientRect(), el = panel.value, b = el.getBoundingClientRect()
      const left = Math.max(12, Math.min(p.placement.endsWith('end') ? a.right - b.width : a.left, innerWidth - b.width - 12))
      const top = a.bottom + b.height + 8 <= innerHeight ? a.bottom + 6 : Math.max(12, a.top - b.height - 6)
      el.style.left = left + 'px'; el.style.top = top + 'px'
    }
    function hide() { update(false) }
    // 视口或容器尺寸变化(手机地址栏收起、软键盘弹出、抽屉内滚动)时重新定位而不是关闭:
    // 小屏上这些变化非常频繁,关闭会让下拉框刚打开就被收起,表现为闪动后无法选择。
    let repositionFrame = 0
    function reposition() {
      if (!state.value) return
      cancelAnimationFrame(repositionFrame)
      repositionFrame = requestAnimationFrame(position)
    }
    async function sync() {
      await nextTick()
      if (state.value) {
        panel.value?.showPopover(); position()
        window.addEventListener('resize', reposition)
        window.addEventListener('scroll', reposition, true)
      } else {
        panel.value?.hidePopover()
        window.removeEventListener('resize', reposition)
        window.removeEventListener('scroll', reposition, true)
      }
    }
    watch(state, sync)
    onBeforeUnmount(() => {
      cancelAnimationFrame(repositionFrame)
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
    })
    expose({ close: hide })
    return () => h('span', { class: 'ui-popover-anchor', ref: anchor }, [
      h('span', { class: 'ui-popover-trigger', 'aria-controls': id, 'aria-expanded': state.value, onClick: (e: MouseEvent) => { e.stopPropagation(); update(!state.value) } }, slots.trigger?.()),
      h('div', { ref: panel, id, popover: 'auto', class: ['ui-popover', { raw: p.raw }], onToggle: (e: Event) => { if ((e as ToggleEvent).newState === 'closed' && state.value) update(false) } }, state.value ? slots.default?.() : undefined),
    ])
  },
})
export const UiDropdown = defineComponent({
  props: { options: { type: Array as PropType<any[]>, default: () => [] }, trigger: String, placement: String }, emits: { select: (_key: string) => true },
  setup(p, { slots, emit }) { const open = ref(false); return () => h(UiPopover, { show: open.value, placement: p.placement, 'onUpdate:show': (v: boolean) => { open.value = v } }, { trigger: slots.default, default: () => h('div', { class: 'ui-menu', role: 'menu' }, p.options.map(o => h('button', { type: 'button', role: 'menuitem', disabled: o.disabled, onClick: () => { emit('select', String(o.key)); open.value = false } }, [o.icon?.(), o.label]))) }) },
})
export const UiSelect = defineComponent({
  inheritAttrs: false,
  props: { value: null, options: { type: Array as PropType<any[]>, default: () => [] }, placeholder: String, disabled: Boolean, loading: Boolean, clearable: Boolean, filterable: Boolean, multiple: Boolean, tag: Boolean, size: String, renderLabel: Function as PropType<(option: any) => any> },
  emits: { 'update:value': (_v: any) => true, clear: () => true },
  setup(p, { attrs, slots, emit }) {
    const form = inject(formKey, null), field = inject(fieldKey, null), open = ref(false), query = ref(''), highlighted = ref(0)
    const available = computed(() => p.options.filter(o => String(o.label ?? o.value).toLowerCase().includes(query.value.toLowerCase())))
    const selected = computed(() => p.options.filter(o => p.multiple ? (p.value || []).includes(o.value) : o.value === p.value))
    function choose(option: any) {
      if (!option || option.disabled) return
      emit('update:value', p.multiple ? (p.value || []).includes(option.value) ? p.value.filter((v: any) => v !== option.value) : [...(p.value || []), option.value] : option.value)
      if (!p.multiple) open.value = false
    }
    function keydown(e: KeyboardEvent) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); open.value = true; highlighted.value = Math.max(0, Math.min(available.value.length - 1, highlighted.value + (e.key === 'ArrowDown' ? 1 : -1))) }
      if (e.key === 'Enter' && open.value) { e.preventDefault(); choose(p.tag && query.value && !available.value.length ? { value: query.value } : available.value[highlighted.value]) }
      if (e.key === 'Escape') { e.stopPropagation(); open.value = false }
    }
    watch(query, () => { highlighted.value = 0 })
    return () => h('div', { class: ['ui-select', p.size, attrs.class], style: attrs.style as any }, [h(UiPopover, { show: open.value, disabled: p.disabled || form?.disabled(), 'onUpdate:show': (v: boolean) => { open.value = v; if (v) query.value = '' } }, {
      trigger: () => h('button', { ...attrs, style: undefined, id: field?.id, type: 'button', role: 'combobox', 'aria-label': attrs['aria-label'] || field?.label() || p.placeholder, 'aria-expanded': open.value, 'aria-haspopup': 'listbox', class: 'ui-select-trigger', disabled: p.disabled || form?.disabled(), onKeydown: keydown }, [h('span', { class: { placeholder: !selected.value.length && p.value == null } }, selected.value.map(o => o.label).join('、') || (p.value != null && p.value !== '' ? String(p.value) : p.placeholder || '请选择')), p.loading ? h('span', { class: 'ui-spinner' }) : h(ChevronDownOutline, { class: 'select-chevron', 'aria-hidden': true })]),
      default: () => h('div', { class: 'ui-select-menu' }, [
        p.filterable || p.tag ? h('input', { type: 'search', autofocus: true, value: query.value, placeholder: p.tag ? '搜索或输入' : '搜索选项', 'aria-label': '搜索选项', onInput: (e: Event) => { query.value = (e.target as HTMLInputElement).value }, onKeydown: keydown }) : null,
        h('div', { role: 'listbox', 'aria-multiselectable': p.multiple || undefined, class: 'ui-select-options' }, available.value.length ? available.value.map((o, i) => h('button', { type: 'button', role: 'option', disabled: o.disabled, 'aria-selected': selected.value.includes(o), class: { selected: selected.value.includes(o), highlighted: i === highlighted.value }, onClick: () => choose(o), onKeydown: keydown }, p.renderLabel ? [p.renderLabel(o)] : [o.label])) : [p.tag && query.value ? h('button', { type: 'button', onClick: () => choose({ value: query.value }) }, '使用 ' + query.value) : slots.empty?.() || h('span', { class: 'muted' }, '没有匹配选项')]),
        slots.action ? h('div', { class: 'ui-select-action' }, slots.action()) : null,
      ]),
    }), p.clearable && p.value != null ? h('button', { type: 'button', class: 'input-clear', 'aria-label': '清空' + (field?.label() || p.placeholder || ''), disabled: p.disabled || form?.disabled(), onClick: () => { emit('update:value', p.multiple ? [] : null); emit('clear') } }, '×') : null])
  },
})
export const UiPopselect = defineComponent({ props: { show: Boolean, value: Array as PropType<any[]>, options: Array as PropType<any[]>, multiple: Boolean, scrollable: Boolean, trigger: String, placement: String, disabled: Boolean }, emits: { 'update:show': (_v: boolean) => true, 'update:value': (_v: any[]) => true }, setup(p, { slots, emit }) { return () => h(UiPopover, { show: p.show, disabled: p.disabled, placement: p.placement, 'onUpdate:show': (v: boolean) => emit('update:show', v) }, { trigger: () => h('span', { role: 'button', tabindex: 0, 'aria-label': '修改路由分组', onKeydown: (e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); emit('update:show', !p.show) } } }, slots.default?.()), default: () => h('div', { class: 'ui-select-options', role: 'group', 'aria-label': '路由分组' }, p.options?.length ? p.options.map(o => h('label', { class: 'checkbox' }, [h('input', { type: 'checkbox', checked: p.value?.includes(o.value), disabled: p.disabled || o.disabled, onChange: (e: Event) => emit('update:value', (e.target as HTMLInputElement).checked ? [...(p.value || []), o.value] : (p.value || []).filter(v => v !== o.value)) }), o.label])) : slots.empty?.()) }) } })

type Notice = { id: number; content: string | (() => any); type: string; closable?: boolean }
type Confirm = { title: string; content: string | (() => any); positiveText?: string; negativeText?: string; onPositiveClick?: () => unknown }
const notices = ref<Notice[]>([]), confirmation = ref<Confirm | null>(null)
let noticeId = 0
function message(type: string, content: string | (() => any), options: { duration?: number; closable?: boolean } = {}) {
  const id = ++noticeId
  notices.value.push({ id, type, content, closable: options.closable })
  const destroy = () => { notices.value = notices.value.filter(n => n.id !== id) }
  if (options.duration !== 0) window.setTimeout(destroy, options.duration ?? 4000)
  return { destroy }
}
export function useMessage() { return { success: (text: string | (() => any), opts?: any) => message('success', text, opts), error: (text: string | (() => any), opts?: any) => message('error', text, opts), warning: (text: string | (() => any), opts?: any) => message('warning', text, opts), info: (text: string | (() => any), opts?: any) => message('info', text, opts) } }
export function useDialog() { return { warning(options: Confirm) { confirmation.value = options } } }
export const UiFeedback = defineComponent({ setup() { const busy = ref(false); return () => [h(Teleport, { to: 'body' }, [h('div', { class: 'ui-toasts', 'aria-live': 'polite' }, notices.value.map(n => h('div', { class: ['toast', 'ui-message', n.type], key: n.id }, [typeof n.content === 'function' ? n.content() : n.content, n.closable ? h('button', { type: 'button', 'aria-label': '关闭消息', onClick: () => { notices.value = notices.value.filter(x => x.id !== n.id) } }, '×') : null])))]), h(UiModal, { show: !!confirmation.value, title: confirmation.value?.title, closable: !busy.value, 'onUpdate:show': (v: boolean) => { if (!v) confirmation.value = null } }, { default: () => typeof confirmation.value?.content === 'function' ? confirmation.value.content() : confirmation.value?.content, footer: () => [h(UiButton, { disabled: busy.value, onClick: () => { confirmation.value = null } }, () => confirmation.value?.negativeText || '取消'), h(UiButton, { type: 'primary', loading: busy.value, onClick: async () => { busy.value = true; try { const result = await confirmation.value?.onPositiveClick?.(); if (result !== false) confirmation.value = null } finally { busy.value = false } } }, () => confirmation.value?.positiveText || '确认')] })] } })
