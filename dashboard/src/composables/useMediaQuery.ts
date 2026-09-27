// 移动端断点 hook：matchMedia 变化实时驱动（侧栏抽屉/头部汉堡等共用）。
// 断点与 assets/mobile.css 的媒体查询保持一致（布局折叠走 CSS，这里只驱动 JS 分支）。
import { onBeforeUnmount, ref } from 'vue'

export const MOBILE_BREAKPOINT = '(max-width: 768px)'

export function useMediaQuery(query: string = MOBILE_BREAKPOINT) {
  const matches = ref(false)

  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    const mql = window.matchMedia(query)
    const update = () => { matches.value = mql.matches }
    update()
    if (typeof mql.addEventListener === 'function') {
      // 标准 API（WebView Chromium 74+）
      mql.addEventListener('change', update)
      onBeforeUnmount(() => mql.removeEventListener('change', update))
    } else {
      // 旧实现回退（addListener 已废弃但仍可用）
      mql.addListener(update)
      onBeforeUnmount(() => mql.removeListener(update))
    }
  }

  return { matches }
}
