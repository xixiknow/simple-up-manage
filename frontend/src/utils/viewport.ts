import { computed, onMounted, onUnmounted, ref } from 'vue'

const PHONE = '(max-width: 760px)'
const NARROW = '(max-width: 1100px)'

/** Phone ≤760, pad 761–1100, desktop ≥1101. Matches the rotation console. */
export function useViewport() {
  const phone = ref(window.matchMedia(PHONE).matches)
  const narrow = ref(window.matchMedia(NARROW).matches)
  const pad = computed(() => narrow.value && !phone.value)
  const desktop = computed(() => !narrow.value)

  const phoneMedia = window.matchMedia(PHONE)
  const narrowMedia = window.matchMedia(NARROW)

  function read() {
    phone.value = phoneMedia.matches
    narrow.value = narrowMedia.matches
  }

  onMounted(() => {
    phoneMedia.addEventListener('change', read)
    narrowMedia.addEventListener('change', read)
    // Some webviews / embedded browsers miss matchMedia change events (the
    // flag then goes stale and phone layouts keep desktop tables with a
    // horizontal scrollbar). Resize/orientation events are more reliable,
    // so re-read the flags on them as a self-healing fallback.
    window.addEventListener('resize', read)
    window.addEventListener('orientationchange', read)
  })

  onUnmounted(() => {
    phoneMedia.removeEventListener('change', read)
    narrowMedia.removeEventListener('change', read)
    window.removeEventListener('resize', read)
    window.removeEventListener('orientationchange', read)
  })

  return { phone, pad, desktop }
}
