import { app, BrowserWindow, ipcMain, Menu, shell, Tray, nativeImage } from 'electron'
import { join } from 'node:path'
import { readFileSync } from 'node:fs'
import { Daemon } from './daemon.js'
import { isDev, watchRenderer } from './dev.js'
import { homeDirectory, logFile, seedHome } from './home.js'
import { MusicBox } from './music.js'
import { Updates } from './updates.js'
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

// A real name rather than the package one, so the settings and cache land in
// a folder a person can find: %APPDATA%/MikkiLens. This has to happen before
// anything asks for a path, which is why it is up here rather than with the
// rest of the startup.
app.setName('MikkiLens')

const home = homeDirectory()
seedHome(home)

// Electron's own caches go under data/, with everything else MikkiLens
// writes, rather than beside config.toml. The home directory is somewhere she
// opens to edit her commands, and half a browser's worth of cache folders in
// there makes the two files she actually wants much harder to find.
app.setPath('userData', join(home, 'data', 'window'))

// In dev the engine belongs to air, so this attaches and never spawns.
const development = isDev()
const daemon = new Daemon(engineURL, home, development)
const updates = new Updates(engineURL, daemon, (key, values) => t(key, values), () => {
  // A waiting update changes both menus, so it can be reached from the tray
  // without opening the window.
  Menu.setApplicationMenu(buildMenu())
  applyTrayMenu()
})

// The box she types a song name into. It has its own window, opened over
// whatever she is doing by a key or by asking out loud, because the settings
// window is somewhere she went on purpose and this is not.
const musicBox = new MusicBox(engineURL, () => language)

let window: BrowserWindow | null = null
let tray: Tray | null = null
let quitting = false

/** The window follows the language the engine speaks, so both agree. */
let language = fallbackLanguage
let strings: Catalog = catalog(fallbackLanguage)

const t = (key: string, values?: Record<string, string | number>): string =>
  translate(strings, key, values)

/**
 * One window at a time, but never at the cost of no window at all.
 *
 * The lock is worth having: a second launch should raise the window that is
 * already open rather than adding another. But Electron leaves the lock behind
 * when it is killed rather than closed, and a stale one made the next launch
 * exit silently -- she double-clicks the icon and nothing happens, with
 * nothing anywhere to say why. That is the worse failure by a long way, so a
 * lock we cannot take is a warning and we carry on.
 *
 * Carrying on is safe: the engine is a separate process and a second window
 * attaches to the one already running rather than starting its own, so the
 * worst case is two windows onto the same engine.
 */
async function acquireSingleInstanceLock(): Promise<boolean> {
  for (let attempt = 0; attempt < 3; attempt++) {
    if (app.requestSingleInstanceLock()) {
      return true
    }
    // A window that has just closed can hold the lock for a moment longer.
    await new Promise((resolve) => setTimeout(resolve, 600))
  }
  return false
}

{
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
  if (!(await acquireSingleInstanceLock())) {
    console.warn(
      'MikkiLens could not take the single-instance lock. ' +
        'Opening anyway: a stale lock must not leave you without a window.',
    )
  }
  await app.whenReady()

  createWindow()
  createTray()
  Menu.setApplicationMenu(buildMenu())

  if (development) {
    watchRenderer(
      () => window,
      () => {
        // The menus and the tray live here, not in the page, so a reload does
        // not touch them; they are rebuilt from the strings just reread.
        strings = catalog(language)
        Menu.setApplicationMenu(buildMenu())
        applyTrayMenu()
      },
    )
  }

  updates.start()

  const status = await daemon.ensureRunning()
  if (status.reachable) {
    await adoptEngineLanguage()
  }
  // After the language, so the box opens in the one she reads, and after the
  // engine is up, so the key it registers is the one her config asks for.
  await musicBox.start()
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
    width: 1180,
    height: 820,
    minWidth: 620,
    minHeight: 460,
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

/**
 * Silence the chat being read aloud, or give it back.
 *
 * The key is how she uses this. The menu item is for whoever is helping her,
 * who would rather press something than learn a keyboard shortcut on somebody
 * else's machine -- and it is a toggle for the same reason the key is: there is
 * no way to see which way it is set, and the engine says which it went.
 */
function toggleMute(): void {
  void fetch(`${engineURL}/api/mute`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  }).catch(() => {})
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
      { label: t('menu.findSong'), click: () => musicBox.open() },
      { label: t('menu.toggleMute'), click: toggleMute },
      { type: 'separator' },
      {
        label: updates.status().ready ? t('menu.installUpdate') : t('menu.noUpdate'),
        enabled: updates.status().ready,
        click: () => void updates.install(),
      },
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
        // No accelerators on these two: both already have a global key of
        // their own, taken from her config, and a second one that only works
        // while this window has focus would be a different key for the same
        // thing depending on where she is.
        { label: t('menu.findSong'), click: () => musicBox.open() },
        { label: t('menu.toggleMute'), click: toggleMute },
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
          // Shown either way. "Up to date" is information; a menu that only
          // grows an item when something is wrong makes her hunt for it.
          label: updates.status().ready
            ? t('menu.installUpdate')
            : t('menu.noUpdate'),
          enabled: updates.status().ready,
          click: () => void updates.install(),
        },
        { type: 'separator' },
        {
          label: t('menu.openLog'),
          click: () => {
            void shell.openPath(logFile())
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

ipcMain.handle('update:status', () => updates.status())

ipcMain.handle('update:check', () => updates.checkNow())

ipcMain.handle('app:version', () => app.getVersion())

ipcMain.handle('update:install', async () => {
  await updates.install()
  return updates.status()
})

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
    const contents = readFileSync(logFile(), 'utf8')
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
  updates.stop()
  musicBox.stop()
  daemon.stop()
})

app.on('window-all-closed', () => {
  // Deliberately does not quit: the tray keeps the engine and the app alive,
  // which is what "close the window mid-stream" has to mean.
})
