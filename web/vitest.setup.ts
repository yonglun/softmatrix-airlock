import '@testing-library/jest-dom/vitest'

// jsdom 不实现 matchMedia。antd 的响应式断点（Grid、Table 的响应式列）
// 在挂载时就调用它，不 polyfill 的话任何渲染 Table 的测试都会崩在这里。
if (typeof window !== 'undefined' && !window.matchMedia) {
  window.matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })
}
