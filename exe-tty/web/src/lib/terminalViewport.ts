// Soft-keyboard geometry for the mobile web terminals.
//
// iOS Safari does not shrink the layout viewport when the soft keyboard slides
// up: window.innerHeight stays put and only visualViewport.height shrinks. The
// terminal clamps its own height to the visible part so the prompt can't hide
// under the keyboard.
//
// Pinch-zoom shrinks visualViewport.height too, and by exactly the same
// signal, so a naive innerHeight - visualViewport.height check reads a zoomed
// page as "keyboard is up" and shrinks the terminal for no reason. Scaling the
// visible height back into layout pixels (height * scale) separates the two:
// zooming leaves the layout-pixel height unchanged, the keyboard doesn't.
export interface ViewportMetrics {
  /** visualViewport.height */
  height: number;
  /** visualViewport.scale (1 when not pinch-zoomed) */
  scale: number;
  /** window.innerHeight — the layout viewport, unaffected by the keyboard. */
  innerHeight: number;
}

/**
 * Threshold (layout px) for calling the keyboard up. Bigger than any
 * address-bar transition on common phones, smaller than every soft keyboard
 * we've seen.
 */
export const KEYBOARD_MIN_PX = 120;

export interface ViewportClamp {
  /** True when the soft keyboard is occupying the bottom of the screen. */
  keyboardUp: boolean;
  /**
   * Height (layout px) to clamp the terminal app to, or null to leave it at
   * 100dvh.
   */
  clampPx: number | null;
}

/**
 * How long the visible height must hold steady before we resize the PTY.
 *
 * iOS slides the keyboard up over roughly 250-350ms and fires a
 * visualViewport resize on *every* frame of that animation. Re-fitting on
 * each one sends a SIGWINCH per frame (measured: 34 for a single keyboard
 * open), and a full-screen TUI redraws on every SIGWINCH — which is what
 * painted stacked, half-finished copies of top/vim/Claude Code down the
 * screen. The CSS clamp still tracks every frame so the view never lags;
 * only the PTY resize waits for the animation to settle.
 */
export const KEYBOARD_SETTLE_MS = 250;

export function computeViewportClamp(m: ViewportMetrics): ViewportClamp {
  const scale = m.scale > 0 ? m.scale : 1;
  // Visible height expressed in layout pixels, which is what CSS lays out in.
  const visible = m.height * scale;
  if (!(m.innerHeight - visible > KEYBOARD_MIN_PX)) {
    return { keyboardUp: false, clampPx: null };
  }
  return { keyboardUp: true, clampPx: Math.round(visible) };
}
