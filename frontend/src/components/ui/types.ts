import type { VNodeChild } from 'vue'

export interface SelectOption { label: string; value: string | number; disabled?: boolean; [key: string]: unknown }
export interface DropdownOption { label: string; key: string; disabled?: boolean; icon?: () => VNodeChild }
export interface FormRule { required?: boolean; type?: string; min?: number; message?: string; trigger?: string | string[] }
export type FormRules = Record<string, FormRule | FormRule[]>
export interface FormInst { validate(): Promise<void> }
export interface DataTableColumn<T> {
  title: string | (() => VNodeChild)
  key: string
  width?: number
  minWidth?: number
  align?: 'left' | 'right' | 'center'
  fixed?: 'left' | 'right'
  className?: string
  ellipsis?: boolean | { tooltip?: boolean }
  /** 手机卡片模式：作为卡片标题 / 标题右侧 tag 渲染 */
  mobileTitle?: boolean
  mobileTag?: boolean
  /** 手机卡片模式：渲染进卡片头部（如勾选框），不进主体 */
  mobileHead?: boolean
  /** 手机卡片模式：主体不展示；cardCollapse 开启时收进「更多字段」折叠区 */
  mobileHide?: boolean
  rowSpan?: (row: T, index: number) => number
  colSpan?: (row: T, index: number) => number
  render?: (row: T, index: number) => VNodeChild
}
export type DataTableColumns<T> = DataTableColumn<T>[]
