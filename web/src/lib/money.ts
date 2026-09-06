/**
 * 把后端的 Micro 金额换算成元。1 Micro = 1e-6 元。
 *
 * 保留四位小数：单次调用的成本经常小于一分钱，按两位小数显示会让
 * 整张表变成一片 ¥0.00，看上去像没花钱。
 */
export function yuan(micro: number): string {
  return `¥${(micro / 1_000_000).toFixed(4)}`
}
