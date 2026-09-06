/**
 * 把当前筛选条件拼成 CSV 导出链接。
 *
 * 导出走服务端（同源 + HttpOnly cookie 会自动带上凭据），因此这里只
 * 负责把条件原样搬进 URL。漏掉任何一个条件，导出的就会是与页面不一致
 * 的数据——那比导不出来更糟，因为没人会发现。
 */
export function exportURL(
  path: string,
  params: Record<string, string | number | boolean | undefined>,
): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '') continue
    qs.set(k, String(v))
  }
  qs.set('format', 'csv')
  return `${path}?${qs.toString()}`
}
