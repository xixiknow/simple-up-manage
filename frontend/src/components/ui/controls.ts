/** Native Vue controls using the rotation console's HTML/CSS primitives. */
import { defineComponent, h, inject, provide, ref, useId, watch, type InjectionKey, type PropType, type VNodeChild } from 'vue'

const sizeProps = { size: String, disabled: Boolean, loading: Boolean }
type Value = string | number | boolean | null
type ChoiceContext = { value: () => any; update: (value: any) => void; name: string; disabled: () => boolean }
const radioKey: InjectionKey<ChoiceContext> = Symbol('radio')
const checkboxKey: InjectionKey<ChoiceContext> = Symbol('checkbox')
type Rule = { required?: boolean; type?: string; min?: number; message?: string }
type FormContext = { disabled: () => boolean; errors: Record<string, string>; labelPlacement: () => string; labelWidth: () => string | number }
const formKey: InjectionKey<FormContext> = Symbol('form')
const fieldKey: InjectionKey<{ id: string; label: () => string }> = Symbol('field')
const px = (value: string | number | undefined) => typeof value === 'number' || (typeof value === 'string' && /^\d+(\.\d+)?$/.test(value)) ? value + 'px' : value
const flatten = (children: any): string => Array.isArray(children) ? children.map(flatten).join(' ') : children && typeof children === 'object' ? flatten(children.children) : typeof children === 'string' || typeof children === 'number' ? String(children) : ''

export const UiButton = defineComponent({
  name: 'UiButton', props: { ...sizeProps, type: String, attrType: { type: String, default: 'button' }, block: Boolean, text: Boolean, quaternary: Boolean, secondary: Boolean, circle: Boolean, bordered: Boolean },
  setup(p, { slots }) {
    const form = inject(formKey, null)
    return () => h('button', { type: p.attrType as 'button', disabled: p.disabled || p.loading || form?.disabled(), 'aria-busy': p.loading || undefined,
      class: ['ui-button', p.text || p.quaternary ? 'link' : p.type === 'primary' && !p.secondary ? 'primary' : 'secondary', p.size, { block: p.block, circle: p.circle, danger: p.type === 'error' }] },
    [p.loading ? h('span', { class: 'ui-spinner', 'aria-hidden': true }) : slots.icon?.(), slots.default?.()])
  },
})
export const UiIcon = defineComponent({ props: { size: [Number, String], component: [Object, Function] as PropType<any> }, setup: (p, { slots }) => () => h('span', { class: 'ui-icon', 'aria-hidden': true, style: { width: px(p.size || 16), height: px(p.size || 16) } }, p.component ? [h(p.component)] : slots.default?.()) })
export const UiSpace = defineComponent({ props: { size: [Number, String, Array] as PropType<any>, justify: String, align: String, vertical: Boolean, wrap: { type: Boolean, default: true } }, setup: (p, { slots }) => () => h('div', { class: 'ui-space', style: { display: 'flex', gap: Array.isArray(p.size) ? p.size.map(px).join(' ') : px(typeof p.size === 'number' ? p.size : p.size === 'small' ? 8 : 12), flexDirection: p.vertical ? 'column' : 'row', flexWrap: p.wrap ? 'wrap' : 'nowrap', justifyContent: p.justify === 'end' ? 'flex-end' : p.justify, alignItems: p.align } }, slots.default?.()) })
export const UiTag = defineComponent({ props: { type: String, size: String, bordered: Boolean, round: Boolean }, setup: (p, { slots }) => () => h('span', { class: ['ui-tag', 'badge', p.type === 'error' ? 'failed' : p.type === 'warning' ? 'waiting' : p.type === 'info' ? 'active' : p.type] }, [h('i', { 'aria-hidden': true }), slots.default?.()]) })
export const UiEmpty = defineComponent({ props: { description: String }, setup: (p, { slots }) => () => h('div', { class: 'ui-empty', role: 'status' }, [slots.default?.() || p.description || '暂无数据', slots.extra?.()]) })
export const UiAlert = defineComponent({ props: { title: String, type: String, bordered: Boolean, closable: Boolean }, emits: ['close'], setup: (p, { slots, emit }) => () => h('div', { class: ['ui-alert', p.type || 'info'], role: p.type === 'error' ? 'alert' : 'status' }, [h('div', [p.title ? h('strong', p.title) : null, slots.default?.()]), p.closable ? h('button', { type: 'button', 'aria-label': '关闭提示', onClick: () => emit('close') }, '×') : null]) })
export const UiSpin = defineComponent({ props: { show: Boolean }, setup: (p, { slots }) => () => h('div', { class: 'ui-spin', 'aria-busy': p.show }, [h('div', { class: 'ui-spin-content' }, slots.default?.()), p.show ? h('div', { class: 'ui-loading-shade', role: 'status', 'aria-label': '加载中' }, [h('span', { class: 'ui-spinner' })]) : null]) })
export const UiCard = defineComponent({ props: { title: String, size: String, bordered: Boolean, loading: Boolean }, setup: (p, { slots }) => () => h('article', { class: 'ui-card workspace-card', 'aria-busy': p.loading }, [p.title || slots.header || slots['header-extra'] ? h('div', { class: 'ui-card-header' }, [h('h3', slots.header?.() || p.title), h('div', { class: 'ui-card-header__extra' }, slots['header-extra']?.())]) : null, h('div', { class: 'ui-card__content' }, slots.default?.()), slots.footer ? h('footer', slots.footer()) : null, p.loading ? h('span', { class: 'ui-spinner card-loading', 'aria-label': '加载中' }) : null]) })

export const UiForm = defineComponent({
  props: { model: Object as PropType<Record<string, any>>, rules: Object as PropType<Record<string, Rule | Rule[]>>, disabled: Boolean, labelPlacement: { type: String, default: 'top' }, labelWidth: { type: [String, Number], default: '100px' } },
  setup(p, { slots, expose }) {
    const errors = ref<Record<string, string>>({})
    const context: FormContext = { disabled: () => p.disabled, get errors() { return errors.value }, labelPlacement: () => p.labelPlacement, labelWidth: () => p.labelWidth }
    provide(formKey, context)
    watch(() => p.model, () => { errors.value = {} }, { deep: true })
    expose({ async validate() {
      const next: Record<string, string> = {}
      for (const [key, raw] of Object.entries(p.rules || {})) {
        const value = p.model?.[key]
        for (const rule of Array.isArray(raw) ? raw : [raw]) {
          if ((rule.required && (value == null || value === '' || (Array.isArray(value) && !value.length))) || (rule.type === 'array' && rule.min != null && (!Array.isArray(value) || value.length < rule.min)) || (rule.type === 'number' && value != null && typeof value !== 'number')) next[key] = rule.message || '请检查此项'
        }
      }
      errors.value = next
      if (Object.keys(next).length) throw new Error(Object.values(next)[0])
    } })
    return () => h('form', { class: 'ui-form', onSubmit: (e: Event) => e.preventDefault() }, [h('fieldset', { disabled: p.disabled }, slots.default?.())])
  },
})
export const UiFormItem = defineComponent({
  props: { label: String, path: String }, setup(p, { slots }) {
    const id = useId(), form = inject(formKey, null)
    provide(fieldKey, { id, label: () => p.label || '' })
    return () => h('div', { class: ['ui-form-item', 'field', { 'is-left': form?.labelPlacement() === 'left', 'has-error': p.path && form?.errors[p.path] }], style: { '--label-width': typeof form?.labelWidth() === 'number' || /^\d+$/.test(String(form?.labelWidth())) ? form?.labelWidth() + 'px' : form?.labelWidth() } }, [
      h('label', { for: id, class: 'ui-form-item-label' }, p.label), h('div', { class: 'ui-field-control' }, slots.default?.()),
      p.path && form?.errors[p.path] ? h('small', { class: 'ui-field-error', role: 'alert' }, form.errors[p.path]) : null,
      slots.feedback ? h('small', { class: 'ui-field-feedback' }, slots.feedback()) : null,
    ])
  },
})
export const UiInput = defineComponent({
  inheritAttrs: false, props: { ...sizeProps, value: null, type: { type: String, default: 'text' }, placeholder: String, clearable: Boolean, inputProps: Object, showPasswordOn: String, rows: Number, autosize: [Object, Boolean] as PropType<any>, maxlength: [String, Number], showCount: Boolean },
  emits: { 'update:value': (_v: string) => true, clear: () => true },
  setup(p, { slots, attrs, emit }) {
    const field = inject(fieldKey, null), form = inject(formKey, null), visible = ref(false)
    return () => h('div', { class: ['ui-input', p.size, attrs.class], style: attrs.style as any }, [slots.prefix?.(), h(p.type === 'textarea' ? 'textarea' : 'input', {
      ...attrs, style: undefined, class: undefined, id: field?.id, 'aria-label': field?.label() || p.placeholder, ...p.inputProps,
      type: p.type === 'password' && visible.value ? 'text' : p.type, value: p.value ?? '', placeholder: p.placeholder,
      disabled: p.disabled || form?.disabled(), maxlength: p.maxlength, rows: p.rows || p.autosize?.minRows || 3,
      onInput: (e: Event) => emit('update:value', (e.target as HTMLInputElement).value),
    }), p.clearable && p.value ? h('button', { type: 'button', class: 'input-clear', 'aria-label': '清空' + (field?.label() || p.placeholder || ''), disabled: p.disabled || form?.disabled(), onClick: () => { emit('update:value', ''); emit('clear') } }, '×') : null,
    p.type === 'password' && p.showPasswordOn ? h('button', { type: 'button', class: 'password-toggle', 'aria-label': visible.value ? '隐藏密码' : '显示密码', onClick: () => { visible.value = !visible.value } }, visible.value ? '隐藏' : '显示') : null, slots.suffix?.(), p.showCount ? h('small', { class: 'input-count' }, String(p.value?.length || 0) + (p.maxlength ? ' / ' + p.maxlength : '')) : null])
  },
})
export const UiInputNumber = defineComponent({
  inheritAttrs: false, props: { ...sizeProps, value: null, min: Number, max: Number, step: { type: Number, default: 1 }, precision: Number, clearable: Boolean, placeholder: String, showButton: Boolean },
  emits: { 'update:value': (_v: number | null) => true }, setup(p, { attrs, emit }) {
    const field = inject(fieldKey, null), form = inject(formKey, null)
    function update(e: Event) {
      const input = e.target as HTMLInputElement
      if (!input.value) { emit('update:value', null); return }
      let value = input.valueAsNumber
      if (!Number.isFinite(value)) return
      if (p.min != null) value = Math.max(value, p.min)
      if (p.max != null) value = Math.min(value, p.max)
      if (p.precision != null) value = Number(value.toFixed(p.precision))
      emit('update:value', value)
    }
    return () => h('div', { class: ['ui-input-number', p.size, attrs.class], style: attrs.style as any }, [h('input', { ...attrs, style: undefined, class: undefined, id: field?.id, 'aria-label': field?.label() || p.placeholder, type: 'number', value: p.value ?? '', min: p.min, max: p.max, step: p.step, disabled: p.disabled || form?.disabled(), placeholder: p.placeholder, onInput: update }), p.clearable && p.value != null ? h('button', { type: 'button', class: 'input-clear', 'aria-label': '清空' + (field?.label() || p.placeholder || ''), disabled: p.disabled || form?.disabled(), onClick: () => emit('update:value', null) }, '×') : null])
  },
})
export const UiRadioGroup = defineComponent({ props: { value: null, size: String, disabled: Boolean }, emits: { 'update:value': (_v: any) => true }, setup(p, { slots, emit }) { const form = inject(formKey, null); provide(radioKey, { value: () => p.value, update: v => emit('update:value', v), name: useId(), disabled: () => p.disabled || !!form?.disabled() }); return () => h('div', { class: 'ui-radio-group segmented', role: 'radiogroup' }, slots.default?.()) } })
export const UiRadio = defineComponent({ props: { value: null, disabled: Boolean }, setup(p, { slots }) { const group = inject(radioKey)!; return () => h('label', { class: ['ui-radio', { on: group.value() === p.value }] }, [h('input', { type: 'radio', name: group.name, checked: group.value() === p.value, disabled: p.disabled || group.disabled(), onChange: () => group.update(p.value) }), slots.default?.()]) } })
export const UiRadioButton = UiRadio
export const UiCheckboxGroup = defineComponent({ props: { value: Array as PropType<Value[]>, disabled: Boolean }, emits: { 'update:value': (_v: any[]) => true }, setup(p, { slots, emit }) { const form = inject(formKey, null); provide(checkboxKey, { value: () => p.value || [], update: v => emit('update:value', v), name: useId(), disabled: () => p.disabled || !!form?.disabled() }); return () => h('div', { class: 'ui-checkbox-group' }, slots.default?.()) } })
export const UiCheckbox = defineComponent({ props: { value: null, checked: Boolean, label: String, disabled: Boolean, indeterminate: Boolean, size: String }, emits: { 'update:checked': (_v: boolean) => true }, setup(p, { slots, emit }) { const group = inject(checkboxKey, null), form = inject(formKey, null), field = inject(fieldKey, null); return () => h('label', { class: 'ui-checkbox checkbox' }, [h('input', { id: field?.id, type: 'checkbox', checked: group ? group.value().includes(p.value) : p.checked, indeterminate: p.indeterminate, disabled: p.disabled || group?.disabled() || form?.disabled(), onChange: (e: Event) => { const on = (e.target as HTMLInputElement).checked; if (group) group.update(on ? [...group.value(), p.value] : group.value().filter((x: Value) => x !== p.value)); else emit('update:checked', on) } }), slots.default?.() || p.label]) } })
export const UiSwitch = defineComponent({ inheritAttrs: false, props: { ...sizeProps, value: Boolean }, emits: { 'update:value': (_v: boolean) => true, updateValue: (_v: boolean) => true }, setup(p, { slots, attrs, emit }) { const form = inject(formKey, null), field = inject(fieldKey, null); return () => h('label', { class: ['ui-switch', 'checkbox', { pending: p.loading }] }, [h('input', { ...attrs, id: field?.id, 'aria-label': attrs['aria-label'] || field?.label() || '切换状态', type: 'checkbox', role: 'switch', checked: p.value, disabled: p.disabled || p.loading || form?.disabled(), onChange: (e: Event) => { const value = (e.target as HTMLInputElement).checked; emit('update:value', value); emit('updateValue', value) } }), p.value ? slots.checked?.() : slots.unchecked?.()]) } })
export const UiInputGroup = defineComponent({ setup: (_p, { slots }) => () => h('div', { class: 'ui-input-group' }, slots.default?.()) })
export const UiInputGroupLabel = defineComponent({ props: { size: String }, setup: (_p, { slots }) => () => h('span', { class: 'ui-input-group-label' }, slots.default?.()) })
export const UiDynamicTags = defineComponent({ props: { value: Array as PropType<string[]> }, emits: { 'update:value': (_v: string[]) => true }, setup(p, { emit }) { const draft = ref(''); const add = () => { const v = draft.value.trim(); if (v && !p.value?.includes(v)) emit('update:value', [...(p.value || []), v]); draft.value = '' }; return () => h('div', { class: 'ui-dynamic-tags' }, [(p.value || []).map((v, i) => h('span', { class: 'badge' }, [v, h('button', { type: 'button', 'aria-label': '移除 ' + v, onClick: () => emit('update:value', p.value!.filter((_, n) => n !== i)) }, '×')])), h('input', { 'aria-label': '添加模型模式', placeholder: '回车添加', value: draft.value, onInput: (e: Event) => { draft.value = (e.target as HTMLInputElement).value }, onKeydown: (e: KeyboardEvent) => { if (e.key === 'Enter') { e.preventDefault(); add() } } })]) } })
export const UiDatePicker = defineComponent({ props: { value: Array as unknown as PropType<[number, number] | null>, type: String, clearable: Boolean, startPlaceholder: String, endPlaceholder: String, size: String }, emits: { 'update:value': (_v: [number, number] | null) => true }, setup(p, { emit }) {
  const parts = ref(['', ''])
  function local(v: number) { const d = new Date(v); return new Date(v - d.getTimezoneOffset() * 60000).toISOString().slice(0, 19) }
  watch(() => p.value, v => { parts.value = v ? v.map(local) : ['', ''] }, { immediate: true })
  return () => h('div', { class: 'ui-date-picker' }, [parts.value.map((v, i) => h('input', { type: 'datetime-local', step: 1, 'aria-label': (i ? p.endPlaceholder : p.startPlaceholder) || (i ? '结束时间' : '开始时间'), value: v, onInput: (e: Event) => { parts.value[i] = (e.target as HTMLInputElement).value; if (parts.value.every(Boolean)) { const range = parts.value.map(x => new Date(x).getTime()); if (range[0]! <= range[1]!) emit('update:value', range as [number, number]) } } })), p.clearable ? h('button', { type: 'button', 'aria-label': '清空日期', onClick: () => { parts.value = ['', '']; emit('update:value', null) } }, '×') : null])
} })
export const UiTooltip = defineComponent({ props: { trigger: String, placement: String }, setup: (_p, { slots }) => () => h('span', { class: 'ui-tooltip-trigger', title: flatten(slots.default?.()) }, slots.trigger?.()) })
export const UiBadge = defineComponent({ props: { value: Number, max: Number, show: Boolean }, setup: (p, { slots }) => () => h('span', { class: 'ui-badge-wrap' }, [slots.default?.(), p.show ? h('sup', { class: 'ui-badge-count' }, (p.value || 0) > (p.max || 99) ? String(p.max || 99) + '+' : p.value) : null]) })
export const UiTimeline = defineComponent({ setup: (_p, { slots }) => () => h('div', { class: 'ui-timeline' }, slots.default?.()) })
export const UiTimelineItem = defineComponent({ props: { title: String, time: String, type: String }, setup: (p, { slots }) => () => h('article', { class: ['ui-timeline-item', p.type] }, [h('time', p.time), h('strong', p.title), h('div', slots.default?.())]) })

export { fieldKey, formKey, px }
export type { VNodeChild }
