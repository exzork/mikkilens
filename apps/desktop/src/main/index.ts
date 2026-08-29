import { app, BrowserWindow, ipcMain, Menu, shell, Tray, nativeImage } from 'electron'
import { join } from 'node:path'
import { readFileSync } from 'node:fs'
import { Daemon } from './daemon.js'
import { availableLanguages, catalog, fallbackLanguage, translate, type Catalog } from './i18n.js'

/**
 * The main process owns the engine and the window, in that order of
 * importance.
 *
 * Everything the renderer can reach is declared in the preload; the page
 * itself has no Node, no remote module and no direct filesystem. It talks to
 * the engine over its local HTTP API, exactly as anything else would.
 */

const engineURL = process.env.MIKKILENS_API ?? 'http://127.0.0.1:8760'
const daemon = new Daemon(engineURL)

let window: BrowserWindow | null = null
let tray: Tray | null = null
let quitting = false

/** The window follows the language the engine speaks, so both agree. */
let language = fallbackLanguage
let strings: Catalog = catalog(fallbackLanguage)

const t = (key: string, values?: Record<string, string | number>): string =>
  translate(strings, key, values)

/**
 * One instance only. A second window would start a second engine, and two
 * engines would fight over the microphone -- which she would experience as
 * MikkiLens randomly ignoring her.
 */
if (!app.requestSingleInstanceLock()) {
  app.quit()
} else {
  app.on('second-instance', () => showWindow())
  main().catch((error: unknown) => {
    // Startup must never fail silently: the window is the only place a problem
    // here can be reported.
    console.error('MikkiLens could not start', error)
    window?.webContents.send('engine-status', {
      reachable: false,
      owned: false,
      url: engineURL,
      detail: String(error),
    })
  })
}

async function main(): Promise<void> {
  await app.whenReady()

  createWindow()
  createTray()
  Menu.setApplicationMenu(buildMenu())

  const status = await daemon.ensureRunning()
  if (status.reachable) {
    await adoptEngineLanguage()
  }
  window?.webContents.send('engine-status', status)

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow()
    } else {
      showWindow()
    }
  })
}

/**
 * Take the language from the engine's configuration.
 *
 * The engine is the source of truth: she sets the language once, and the
 * window it speaks through follows. The menus and tray are rebuilt afterwards,
 * because they were created before the engine was known to be up.
 */
async function adoptEngineLanguage(): Promise<void> {
  try {
    const response = await fetch(`${engineURL}/api/config`, {
      signal: AbortSignal.timeout(3000),
    })
    if (!response.ok) {
      return
    }
    const settings = (await response.json()) as { language?: { output?: string } }
    const wanted = settings.language?.output
    if (!wanted || wanted === language) {
      return
    }
    if (!availableLanguages().includes(wanted)) {
      return // no window translation for it yet; English stays
    }
    language = wanted
    strings = catalog(language)
    Menu.setApplicationMenu(buildMenu())
    applyTrayMenu()
  } catch {
    // Being unable to read the config is not worth interrupting startup for:
    // the window opens in English and everything still works.
  }
}

function createWindow(): void {
  window = new BrowserWindow({
    width: 1000,
    height: 760,
    minWidth: 560,
    minHeight: 420,
    show: false,
    title: 'MikkiLens',
    backgroundColor: '#12131a',
    webPreferences: {
      preload: join(__dirname, '..', 'preload', 'index.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
      spellcheck: false,
    },
  })

  // Showing only once the page is painted avoids a white flash, and avoids a
  // screen reader announcing an empty window before there is anything in it.
  window.once('ready-to-show', () => showWindow())

  window.on('close', (event) => {
    // Closing hides the window and leaves the engine running, because her
    // stream is still live and still needs to be controllable.
    if (!quitting) {
      event.preventDefault()
      window?.hide()
    }
  })
  window.on('closed', () => {
    window = null
  })

  // Any link out of the app opens in her real browser rather than replacing
  // the settings page with something she cannot get back from.
  window.webContents.setWindowOpenHandler(({ url }) => {
    void shell.openExternal(url)
    return { action: 'deny' }
  })

  void window.loadFile(join(__dirname, '..', 'renderer', 'index.html'))
}

function showWindow(): void {
  if (!window) {
    createWindow()
    return
  }
  if (window.isMinimized()) {
    window.restore()
  }
  window.show()
  window.focus()
}

function listenNow(): void {
  void fetch(`${engineURL}/api/listen`, { method: 'POST' }).catch(() => {})
}

function createTray(): void {
  // A one-pixel transparent image keeps the tray icon valid without shipping a
  // binary asset. The tooltip and the menu are what actually matter here: a
  // screen reader reads those, not the picture.
  const icon = nativeImage.createFromDataURL(
    'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAJ0lEQVR42mNk' +
      'YPhfz0AEYBxVSF+FjAxQBWAaLIhWSFDhqEIaKwQAy0kW0eRoRBUAAAAASUVORK5CYII=',
  )
  tray = new Tray(icon)
  applyTrayMenu()
  tray.on('double-click', () => showWindow())
}

function applyTrayMenu(): void {
  if (!tray) {
    return
  }
  tray.setToolTip(t('tray.tooltip'))
  tray.setContextMenu(
    Menu.buildFromTemplate([
      { label: t('tray.openSettings'), click: () => showWindow() },
      { type: 'separator' },
      { label: t('menu.listenNow'), click: listenNow },
      { type: 'separator' },
      {
        label: t('menu.quit'),
        click: () => {
          quitting = true
          app.quit()
        },
      },
    ]),
  )
}

function buildMenu(): Menu {
  return Menu.buildFromTemplate([
    {
      label: t('menu.app'),
      submenu: [
        {
          label: t('menu.listenNow'),
          accelerator: 'CommandOrControl+L',
          click: listenNow,
        },
        { type: 'separator' },
        { label: t('menu.hideWindow'), accelerator: 'CommandOrControl+W', role: 'close' },
        {
          label: t('menu.quit'),
          accelerator: 'CommandOrControl+Q',
          click: () => {
            quitting = true
            app.quit()
          },
        },
      ],
    },
    {
      label: t('menu.view'),
      submenu: [
        { label: t('menu.reload'), role: 'reload' },
        { label: t('menu.actualSize'), role: 'resetZoom' },
        { label: t('menu.zoomIn'), role: 'zoomIn' },
        { label: t('menu.zoomOut'), role: 'zoomOut' },
        { type: 'separator' },
        { label: t('menu.devTools'), role: 'toggleDevTools' },
      ],
    },
    {
      label: t('menu.help'),
      submenu: [
        {
          label: t('menu.openLog'),
          click: () => {
            void shell.openPath(join(process.cwd(), 'data', 'mikkilens.log'))
          },
        },
      ],
    },
  ])
}

// -- what the renderer is allowed to ask for ----------------------------------

ipcMain.handle('engine:url', () => engineURL)

ipcMain.handle('engine:status', async () => ({
  reachable: await daemon.reachable(),
  url: engineURL,
}))

ipcMain.handle('engine:restart', async () => {
  daemon.stop()
  return daemon.ensureRunning()
})

/**
 * The page asks for its strings rather than reading them off disk, which keeps
 * one copy of the catalogue and keeps the renderer free of filesystem access.
 */
ipcMain.handle('locale:load', (_event, wanted: unknown) => {
  const chosen = typeof wanted === 'string' && wanted ? wanted : language
  return { language: chosen, strings: catalog(chosen) }
})

ipcMain.handle('locale:available', () => availableLanguages())

/**
 * Changing the language in the page also changes the menus and the tray, so
 * the whole app switches at once rather than half of it.
 */
ipcMain.handle('locale:apply', (_event, wanted: unknown) => {
  if (typeof wanted !== 'string' || !wanted || !availableLanguages().includes(wanted)) {
    return language
  }
  language = wanted
  strings = catalog(language)
  Menu.setApplicationMenu(buildMenu())
  applyTrayMenu()
  return language
})

ipcMain.handle('open-external', async (_event, url: unknown) => {
  // Only ever a web address: a renderer that has been compromised must not be
  // able to talk the main process into launching something local.
  if (typeof url !== 'string' || !/^https?:\/\//i.test(url)) {
    return false
  }
  await shell.openExternal(url)
  return true
})

ipcMain.handle('read-log-tail', (_event, lines: unknown) => {
  const wanted = typeof lines === 'number' && lines > 0 ? Math.min(lines, 2000) : 200
  try {
    const contents = readFileSync(join(process.cwd(), 'data', 'mikkilens.log'), 'utf8')
    return contents.split(/\r?\n/).slice(-wanted).join('\n')
  } catch (error) {
    return `Could not read the log: ${String(error)}`
  }
})

ipcMain.handle('login-item', (_event, enabled: unknown) => {
  if (typeof enabled === 'boolean') {
    // Electron's own login item is cleaner than a Startup-folder shortcut when
    // the desktop app is what she launches; the engine keeps its own setting
    // for when it runs headless.
    app.setLoginItemSettings({ openAtLogin: enabled, args: [] })
  }
  return app.getLoginItemSettings().openAtLogin
})

app.on('before-quit', () => {
  quitting = true
  daemon.stop()
})

app.on('window-all-closed', () => {
  // Deliberately does not quit: the tray keeps the engine and the app alive,
  // which is what "close the window mid-stream" has to mean.
})
