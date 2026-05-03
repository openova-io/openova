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
