import { computed, defineComponent, h, type PropType } from 'vue'
import { UiEmpty, UiSpin } from './controls'
import { useViewport } from '@/utils/viewport'

export const UiPagination = defineComponent({
  props: { page: { type: Number, default: 1 }, pageSize: { type: Number, default: 20 }, itemCount: { type: Number, default: 0 }, pageCount: Number, showSizePicker: Boolean, pageSizes: { type: Array as PropType<number[]>, default: () => [20, 50, 100] }, disabled: Boolean },
  emits: { 'update:page': (_v: number) => true, 'update:pageSize': (_v: number) => true },
  setup(p, { emit }) {
    const count = computed(() => Math.max(1, p.pageCount ?? Math.ceil(p.itemCount / p.pageSize)))
    const pages = computed(() => {
      const start = Math.max(1, Math.min(p.page - 2, count.value - 4))
      return Array.from({ length: Math.min(5, count.value) }, (_, i) => start + i)
    })
    return () => h('nav', { class: 'ui-pagination', 'aria-label': '分页' }, [
      h('span', { class: 'muted' }, '共 ' + p.itemCount + ' 条'),
      h('button', { type: 'button', class: 'secondary', 'aria-label': '上一页', disabled: p.disabled || p.page <= 1, onClick: () => emit('update:page', p.page - 1) }, '‹'),
      pages.value.map(n => h('button', { type: 'button', class: [n === p.page ? 'primary' : 'secondary', 'ui-pagination-item'], 'aria-label': '第 ' + n + ' 页', 'aria-current': n === p.page ? 'page' : undefined, disabled: p.disabled, onClick: () => emit('update:page', n) }, n)),
      h('button', { type: 'button', class: 'secondary', 'aria-label': '下一页', disabled: p.disabled || p.page >= count.value, onClick: () => emit('update:page', p.page + 1) }, '›'),
      p.showSizePicker ? h('select', { 'aria-label': '每页条数', value: p.pageSize, disabled: p.disabled, onChange: (e: Event) => emit('update:pageSize', Number((e.target as HTMLSelectElement).value)) }, p.pageSizes.map(size => h('option', { value: size }, size + ' 条/页'))) : null,
    ])
  },
})

// All current grids use server pagination (or explicitly pre-sliced provider data).
// Keep row callbacks, merged provider cells and sticky columns; never re-page the response.
export const UiDataTable = defineComponent({
  props: { columns: { type: Array as PropType<any[]>, default: () => [] }, data: { type: Array as PropType<any[]>, default: () => [] }, loading: Boolean, scrollX: Number, maxHeight: [String, Number], rowKey: Function, rowProps: Function, rowClassName: [String, Function] as PropType<any>, pagination: [Object, Boolean] as PropType<any>, size: String, remote: Boolean, singleLine: Boolean, card: Boolean, cardCollapse: Boolean },
  setup(p, { slots }) {
    const viewport = useViewport()
    function style(col: any, index: number, header = false) {
      const side = col.fixed
      const offset = side === 'left' ? p.columns.slice(0, index).filter(c => c.fixed === 'left').reduce((sum, c) => sum + (Number(c.width) || 0), 0) : p.columns.slice(index + 1).filter(c => c.fixed === 'right').reduce((sum, c) => sum + (Number(c.width) || 0), 0)
      return { textAlign: col.align || 'left', width: col.width ? col.width + 'px' : undefined, position: side || header && p.maxHeight ? 'sticky' : undefined, [side || 'left']: side ? offset + 'px' : undefined, top: header && p.maxHeight ? 0 : undefined, zIndex: side && header ? 3 : side ? 1 : header ? 2 : undefined }
    }
    function cellValue(col: any, row: any, ri: number) {
      return col.render ? col.render(row, ri) : row[col.key] == null ? '—' : String(row[col.key])
    }
    function cardRow(label: string | (() => any), value: any, key: string) {
      return h('div', { class: 'table-card-row', key }, [
        h('span', { class: 'table-card-label' }, typeof label === 'function' ? label() : label),
        h('span', { class: 'table-card-value' }, value),
      ])
    }
    // Phone card mode: each row renders as a self-contained card instead of a
    // horizontally scrolled table row. Column metadata drives the layout.
    function cardList() {
      const titleCol = p.columns.find(c => c.mobileTitle)
      const tagCol = p.columns.find(c => c.mobileTag)
      const headCols = p.columns.filter(c => c.mobileHead)
      const actionCols = p.columns.filter(c => c.fixed === 'right')
      const bodyCols = p.columns.filter(c => c !== titleCol && c !== tagCol && !c.mobileHead && c.fixed !== 'right' && !c.mobileHide)
      const hiddenCols = p.cardCollapse ? p.columns.filter(c => c.mobileHide) : []
      return h('div', { class: 'table-cards' }, p.data.length ? p.data.map((row, ri) => {
        const cls = typeof p.rowClassName === 'function' ? p.rowClassName(row, ri) : p.rowClassName
        return h('article', { key: p.rowKey ? p.rowKey(row) : row.id ?? ri, class: ['table-card', ...(cls ? [cls] : [])], ...p.rowProps?.(row, ri) }, [
          titleCol || tagCol || headCols.length ? h('header', { class: 'table-card-head' }, [
            ...headCols.map(c => h('div', { class: 'table-card-lead', key: c.key }, [cellValue(c, row, ri)])),
            titleCol ? h('div', { class: 'table-card-title' }, [cellValue(titleCol, row, ri)]) : null,
            tagCol ? h('div', { class: 'table-card-tag' }, [cellValue(tagCol, row, ri)]) : null,
          ]) : null,
          bodyCols.length ? h('div', { class: 'table-card-body' }, bodyCols.map(col => cardRow(col.title, cellValue(col, row, ri), col.key))) : null,
          hiddenCols.length ? h('details', { class: 'table-card-more' }, [
            h('summary', '更多字段'),
            h('div', { class: 'table-card-body' }, hiddenCols.map(col => cardRow(col.title, cellValue(col, row, ri), col.key))),
          ]) : null,
          actionCols.length ? h('footer', { class: 'table-card-actions' }, actionCols.map(col => h('div', { class: 'table-card-action', key: col.key }, [cellValue(col, row, ri)]))) : null,
        ])
      }) : [slots.empty?.() || h(UiEmpty, { description: '暂无数据' })])
    }
    return () => h('div', { class: 'ui-data-table' }, [
      h(UiSpin, { show: p.loading }, { default: () => p.card && viewport.phone.value ? cardList() : h('div', { class: 'table-scroll', style: { maxHeight: typeof p.maxHeight === 'number' ? p.maxHeight + 'px' : p.maxHeight } }, [
        // 空数据时不渲染表格骨架：列宽总和会超出窄容器，「暂无数据」无法居中
        p.data.length ? h('table', { style: { minWidth: p.scrollX ? p.scrollX + 'px' : undefined } }, [
          h('colgroup', p.columns.map(col => h('col', { style: { width: col.width ? col.width + 'px' : undefined } }))),
          h('thead', [h('tr', p.columns.map((col, i) => h('th', { key: col.key, scope: 'col', style: style(col, i, true) }, typeof col.title === 'function' ? col.title() : col.title)))]),
          h('tbody', p.data.map((row, ri) => h('tr', { key: p.rowKey ? p.rowKey(row) : row.id ?? ri, class: typeof p.rowClassName === 'function' ? p.rowClassName(row, ri) : p.rowClassName, ...p.rowProps?.(row, ri) }, p.columns.map((col, ci) => {
            const span = col.rowSpan?.(row, ri) ?? 1
            if (span === 0) return null
            const value = cellValue(col, row, ri)
            return h('td', { key: col.key, rowspan: span, colspan: col.colSpan?.(row, ri) ?? 1, class: [col.className, { 'sticky-cell': col.fixed }], style: style(col, ci) }, col.ellipsis ? h('div', { class: 'ui-cell-ellipsis', title: typeof value === 'string' ? value : undefined }, [value]) : [value])
          })))),
        ]) : (slots.empty?.() || h(UiEmpty, { description: '暂无数据' })),
      ]) }),
      p.pagination ? h(UiPagination, { ...p.pagination, disabled: p.loading, 'onUpdate:page': (n: number) => (p.pagination.onChange || p.pagination.onUpdatePage)?.(n), 'onUpdate:pageSize': (n: number) => p.pagination.onUpdatePageSize?.(n) }) : null,
    ])
  },
})
