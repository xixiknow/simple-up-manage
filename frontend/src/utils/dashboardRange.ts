export type DashboardPeriod = 'today' | '7d' | '30d' | 'custom' | 'all'

function shanghaiDay(d: Date) {
  return new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
  }).format(d)
}

function addDays(day: string, n: number) {
  const [y, m, d] = day.split('-').map(Number)
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10)
}

const midnight = (day: string) => `${day}T00:00:00+08:00`

export function dashboardRange(period: DashboardPeriod, custom: [number, number] | null, availableFrom: string, now = new Date()) {
  const today = shanghaiDay(now)
  if (period === 'today') {
    return { from: midnight(today), to: now.toISOString(), gran: 'hour' as const }
  }
  if (period === '7d') {
    return { from: new Date(now.getTime() - 7 * 86400000).toISOString(), to: now.toISOString(), gran: 'hour' as const }
  }
  if (period === '30d') {
    // Thirty Shanghai calendar days, including today; daily data is retained.
    return { from: midnight(addDays(today, -29)), to: midnight(addDays(today, 1)), gran: 'day' as const }
  }
  if (period === 'custom' && custom) {
    const [start, end] = custom
    const from = new Date(start)
    const to = new Date(end)
    if (end - start > 2 * 86400000 || now.getTime() - start >= 30 * 86400000) {
      const fromDay = shanghaiDay(from)
      let toDay = shanghaiDay(to)
      if (toDay === fromDay || new Date(midnight(toDay)).getTime() < end) toDay = addDays(toDay, 1)
      return { from: midnight(fromDay), to: midnight(toDay), gran: 'day' as const }
    }
    return { from: from.toISOString(), to: to.toISOString(), gran: 'hour' as const }
  }
  const startDay = availableFrom ? shanghaiDay(new Date(availableFrom)) : addDays(today, -29)
  return { from: midnight(startDay), to: midnight(addDays(today, 1)), gran: 'day' as const }
}
