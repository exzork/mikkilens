import { existsSync, watch } from 'node:fs'
import { join } from 'node:path'
import type { BrowserWindow } from 'electron'
import { clearCatalogCache } from './i18n.js'

/**
 * Reloading the page while the app keeps running.
 *
 * Only in dev, and only for the page: the main process and the preload cannot
 * be swapped out from underneath a running Electron, so the dev script
 * restarts the app for those. Everything else -- the markup, the stylesheet,
 * the compiled renderer, the window's own strings -- is read fresh on every
 * load, so a reload is enough and is worth preferring. A restart throws away
 * the tab you were on, the scroll position and the fields you had half filled
 * in, which is most of what you were looking at when you made the change.
 *
 * Watching the built output rather than the source is deliberate. tsc writes
 * out/renderer only once it has compiled, so a reload triggered from there can
 * never race a half-written file, and a change that does not compile leaves
 * the last working page on screen instead of a blank one.
 */

/** Whether this process was started by the dev script. */
export function isDev(): boolean {
  return process.argv.includes('--dev') || process.env.MIKKILENS_DEV === '1'
}

/**
 * Reload the window when the page's build output changes.
 *
 * @param getWindow how to find the current window; it is looked up per event
 *   rather than captured, because the window is recreated when it is closed
 *   and reopened from the tray.
 * @param onLocales called when the window's own strings changed, so the menus
 *   and the tray can be rebuilt with them -- those are in the main process and
 *   a page reload does not touch them.
 */
export function watchRenderer(
  getWindow: () => BrowserWindow | null,
  onLocales: () => void,
): void {
  // tsc writes a file per module, so one save arrives as a burst.
  let pending: NodeJS.Timeout | null = null
  let localesChanged = false

  const schedule = (): void => {
    if (pending) {
      clearTimeout(pending)
    }
    pending = setTimeout(() => {
      pending = null
      if (localesChanged) {
        localesChanged = false
        clearCatalogCache()
        onLocales()
      }
      const window = getWindow()
      if (window && !window.isDestroyed()) {
        console.log('[dev] reloading the page')
        window.webContents.reloadIgnoringCache()
      }
    }, 150)
  }

  const targets = [
    join(__dirname, '..', 'renderer'),
    join(__dirname, '..', 'locales'),
  ]

  for (const directory of targets) {
    if (!existsSync(directory)) {
      continue
    }
    try {
      watch(directory, { recursive: true }, (_event, name) => {
        const file = String(name ?? '')
        if (file.endsWith('.map')) {
          return
        }
        if (directory.endsWith('locales')) {
          localesChanged = true
        }
        schedule()
      })
    } catch (error) {
      // Watching is a convenience. Losing it must not stop the app opening,
      // which is what an unwatchable directory would otherwise do.
      console.warn(`[dev] not watching ${directory}: ${String(error)}`)
    }
  }
}
