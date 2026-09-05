import { app, BrowserWindow, globalShortcut, ipcMain } from 'electron'
import { join } from 'node:path'

/**
 * The box she types a song name into.
 *
 * Everything else in MikkiLens is spoken, and this is the one thing that is
 * not. Song and artist names are what speech recognition is worst at -- they
 * are not sentences in any language, and a misheard one does not fail loudly:
 * it finds five wrong songs, with no way to tell whether she was misheard or
 * the search was. She types perfectly without looking, so for this one input
 * the keyboard is the accurate instrument and the microphone is the compromise.
 *
 * The window is only ever a text field. What comes back is read aloud by the
 * engine, one result at a time, and the number she picks can be pressed here or
 * said out loud -- the same numbers either way. So this can be closed, ignored,
 * or never looked at, and the feature still works end to end.
 *
 * It lives in the main process rather than as a page in the settings window
 * because it has to appear over whatever she is doing -- OBS, a game, a browser
 * -- on a key press, and because the settings window is somewhere she went on
 * purpose. Yanking that to the front mid-stream would be a different thing
 * happening than the one she asked for.
 */

/** How long to wait before trying the engine again after it refused to talk. */
const retryDelay = 3000

/**
 * Turn the combination as config.toml writes it into an Electron accelerator.
 *
 * Two spellings for one thing, which is worth avoiding where it can be avoided
 * and cannot be here: the engine's own keys are registered through Windows and
 * have always been written `<ctrl>+<alt>+<f>`, and Electron takes
 * `Control+Alt+F`. Config keeps the one spelling she already knows, and this is
 * where it is translated -- once, in a function with tests, rather than in a
 * second field she has to be told about.
 *
 * Returns null for anything it cannot read, so an unregistrable key is a
 * warning in the log rather than a crash on startup.
 */
export function toAccelerator(combination: string): string | null {
  const named: Record<string, string> = {
    ctrl: 'Control',
    control: 'Control',
    alt: 'Alt',
    shift: 'Shift',
    cmd: 'Super',
    win: 'Super',
    super: 'Super',
    space: 'Space',
    enter: 'Return',
    return: 'Return',
    tab: 'Tab',
    esc: 'Escape',
    escape: 'Escape',
    backspace: 'Backspace',
    delete: 'Delete',
    insert: 'Insert',
    home: 'Home',
    end: 'End',
    page_up: 'PageUp',
    page_down: 'PageDown',
    up: 'Up',
    down: 'Down',
    left: 'Left',
    right: 'Right',
    print_screen: 'PrintScreen',
  }

  const parts: string[] = []
  for (const raw of combination.split('+')) {
    const name = raw.trim().toLowerCase().replace(/^</, '').replace(/>$/, '')
    if (name === '') {
      continue
    }
    const known = named[name]
    if (known !== undefined) {
      parts.push(known)
      continue
    }
    if (/^f([1-9]|1[0-9]|2[0-4])$/.test(name)) {
      parts.push(name.toUpperCase())
      continue
    }
    if (/^[a-z0-9]$/.test(name)) {
      parts.push(name.toUpperCase())
      continue
    }
    // A key from the engine's own vocabulary that Electron has no name for --
    // the numeric keypad, the left and right variants of a modifier. Better to
    // register nothing and say so than to register something else.
    return null
  }
  return parts.length > 0 ? parts.join('+') : null
}

/** What the engine answers a waiting window with. */
interface Prompt {
  count?: number
  open?: boolean
}

export class MusicBox {
  private window: BrowserWindow | null = null
  private accelerator: string | null = null
  private stopped = false
  private since = 0

  constructor(
    private readonly engineURL: string,
    private readonly language: () => string,
  ) {}

  /**
   * Register the key and start listening for the engine asking for the box.
   *
   * Both paths in, because both are worth having: the key is instant and works
   * when the microphone is busy, and the spoken command is what she reaches for
   * when her hands are elsewhere. They open the same window.
   */
  async start(): Promise<void> {
    ipcMain.handle('music:close', () => {
      this.hide()
      return true
    })

    await this.registerKey()
    void this.waitForEngine()
  }

  stop(): void {
    this.stopped = true
    if (this.accelerator) {
      globalShortcut.unregister(this.accelerator)
      this.accelerator = null
    }
    this.window?.destroy()
    this.window = null
  }

  /**
   * Take the combination from the engine's config, so every key MikkiLens
   * answers to is written in one file.
   *
   * A failure here is never fatal. No key means the spoken command still opens
   * the box, which is a worse morning than she was promised and not a reason to
   * be without a settings window.
   */
  private async registerKey(): Promise<void> {
    let combination = '<ctrl>+<alt>+<f>'
    let enabled = true
    try {
      const response = await fetch(`${this.engineURL}/api/config`)
      const settings = (await response.json()) as {
        music?: { enabled?: boolean; combination?: string }
      }
      enabled = settings.music?.enabled ?? true
      combination = settings.music?.combination ?? combination
    } catch (error) {
      console.warn('could not read the music key from the engine; using the default', error)
    }
    if (!enabled) {
      return
    }

    const accelerator = toAccelerator(combination)
    if (accelerator === null) {
      console.warn(`could not understand the music key ${combination}`)
      return
    }
    if (!globalShortcut.register(accelerator, () => this.open())) {
      // Almost always another application already holding it.
      console.warn(`could not register the music key ${accelerator}`)
      return
    }
    this.accelerator = accelerator
  }

  /**
   * Sit on the engine's long poll, so a spoken "play a song" opens the box.
   *
   * A long poll rather than a socket: this is the only message the main process
   * ever needs pushed to it, and a WebSocket client for one message is a
   * dependency to carry and a reconnect loop to get wrong.
   *
   * The count is what makes reconnecting safe. The engine answers with which
   * request it is up to; a request that arrived while this was away is answered
   * immediately rather than lost, and a count that has gone backwards means the
   * engine restarted, so this follows it back rather than waiting for a number
   * that will never come again.
   */
  private async waitForEngine(): Promise<void> {
    while (!this.stopped) {
      try {
        const response = await fetch(`${this.engineURL}/api/music/prompt?since=${this.since}`)
        const prompt = (await response.json()) as Prompt
        const count = prompt.count ?? 0
        if (count < this.since) {
          this.since = count
          continue
        }
        this.since = count
        if (prompt.open === true) {
          this.open()
        }
      } catch {
        // The engine is restarting, or was never there. Neither is worth a line
        // in the log every three seconds for the rest of the session.
        await new Promise((resolve) => setTimeout(resolve, retryDelay))
      }
    }
  }

  /** Show the box, focused and ready to be typed into. */
  open(): void {
    if (this.window === null || this.window.isDestroyed()) {
      this.window = this.create()
      return
    }
    this.window.webContents.send('music:reset')
    this.reveal(this.window)
  }

  private hide(): void {
    this.window?.hide()
  }

  private create(): BrowserWindow {
    const window = new BrowserWindow({
      width: 560,
      height: 460,
      minWidth: 380,
      minHeight: 260,
      show: false,
      // Over whatever she is doing, because that is the point: this is opened
      // mid-stream, from a game or from OBS, and a box that arrives behind them
      // is a box she has to go and find.
      alwaysOnTop: true,
      minimizable: false,
      maximizable: false,
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

    window.once('ready-to-show', () => this.reveal(window))
    window.on('close', (event) => {
      // Closing hides it. Keeping the window means the next press opens onto a
      // field that is already there rather than onto a second of white.
      if (!window.isDestroyed()) {
        event.preventDefault()
        window.hide()
      }
    })

    void window.loadFile(join(__dirname, '..', 'renderer', 'music.html'), {
      query: { language: this.language() },
    })
    return window
  }

  /**
   * Bring it to the front and give it the keyboard.
   *
   * show() alone is not enough on Windows when the press came from a global
   * shortcut: the window appears and the keystrokes carry on going to whatever
   * had focus, which means the song name is typed into her game.
   */
  private reveal(window: BrowserWindow): void {
    if (window.isMinimized()) {
      window.restore()
    }
    app.focus({ steal: true })
    window.show()
    window.moveTop()
    window.focus()
  }
}
