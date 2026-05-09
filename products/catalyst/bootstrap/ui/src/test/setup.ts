/**
 * Vitest setup — polyfills + global stubs for jsdom.
 *
 * Issue #669 — FlowCanvasOrganic uses `ResizeObserver` to track its
 * canvas-host pixel size so the SVG viewBox can render at 1:1. jsdom
 * doesn't ship `ResizeObserver`; a minimal stub is enough for the
 * existing tests, which never assert against the canvas dimensions
 * directly (the FlowCanvas test suite asserts node positions / DOM
 * structure, not measured rects).
 */

if (typeof globalThis.ResizeObserver === 'undefined') {
  class StubResizeObserver {
    // eslint-disable-next-line @typescript-eslint/no-unused-vars
    constructor(_cb: ResizeObserverCallback) {}
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(globalThis as any).ResizeObserver = StubResizeObserver
}

/**
 * EPIC-4 X2 (#1099): xterm.js's renderer reads `window.matchMedia` to
 * watch device-pixel-ratio changes. jsdom doesn't implement
 * matchMedia; a no-op stub is enough for the LogViewer / ExecPanel
 * tests which assert against the wrapper widget's data-testids, not
 * the terminal canvas.
 */
if (typeof globalThis.window !== 'undefined' && !globalThis.window.matchMedia) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(globalThis.window as any).matchMedia = (_query: string) => ({
    matches: false,
    media: _query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  })
}
