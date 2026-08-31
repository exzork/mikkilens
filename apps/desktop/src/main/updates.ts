import { app } from 'electron'
import { autoUpdater } from 'electron-updater'
import type { Daemon } from './daemon.js'

/**
 * Updating must never be the thing that ends her stream.
 *
 * The engine is inside this app, so an update replaces the thing currently
 * listening to her microphone and holding her broadcast open. That makes the
 * usual "download it and restart when convenient" behaviour dangerous here:
 * "convenient" is decided by a timer, and the timer does not know she is live
 * in front of an audience.
 *
 * So the parts are separated by how much they can cost:
 *
 *   checking    free, and silent
 *   downloading free -- it writes to a cache and touches nothing that runs
 *   installing  expensive, and never happens on its own
 *
 * Only the last one stops the engine, and only when she asks for it and is not
 * streaming. Everything is said out loud, because an update that announced
 * itself only in a window would be invisible to the person it interrupts.
 */

/** How long after startup to look, so a launch is never slowed by the network. */
const firstCheckDelayMs = 30_000

/** How often to look after that. Twice a day is plenty for a desktop app. */
const checkIntervalMs = 12 * 60 * 60 * 1000

export interface UpdateStatus {
  /** A newer version has been downloaded and is waiting to be installed. */
  readonly ready: boolean
  /** The version that is waiting, if any. */
  readonly version: string
  /** Why installing is not possible right now, if it is not. */
  readonly blocked: string
}

export class Updates {
  private ready = false
  private version = ''
  private timer: NodeJS.Timeout | null = null

  /**
   * @param engineURL the running engine, which owns the voice and knows
   *   whether she is live
   * @param daemon    stopped before an installer replaces its executable
   * @param t         the window's translator, so she is told in her language
   * @param onChange  called when an update becomes available to install
   */
  constructor(
    private readonly engineURL: string,
    private readonly daemon: Daemon,
    private readonly t: (key: string, values?: Record<string, string | number>) => string,
    private readonly onChange: () => void,
  ) {}

  /**
   * A portable copy cannot replace itself.
   *
   * It runs from a temporary folder that is thrown away on exit, so there is
   * nothing on disk for an installer to update. She is still told a new
   * version exists -- silence would be worse -- but the offer is to go and
   * fetch it rather than to install it here.
   */
  static portable(): boolean {
    return Boolean(process.env.PORTABLE_EXECUTABLE_DIR)
  }

  status(): UpdateStatus {
    return { ready: this.ready, version: this.version, blocked: '' }
  }

  start(): void {
    if (!app.isPackaged) {
      // Running from source: the version in package.json is not a release, and
      // checking would only ever report that it is behind itself.
      return
    }

    autoUpdater.autoDownload = true
    // Nothing is ever replaced without being asked. electron-updater would
    // otherwise run the installer after the app exits, which is exactly the
    // moment nobody is watching -- and the engine may still be running her
    // stream, started separately, with this window merely closed.
    autoUpdater.autoInstallOnAppQuit = false

    autoUpdater.on('update-downloaded', (info: { version: string }) => {
      this.ready = true
      this.version = info.version
      this.onChange()
      void this.announce()
    })

    autoUpdater.on('error', (error: Error) => {
      // A failed check is not worth interrupting her for. It is logged, and
      // the next one is in twelve hours.
      console.warn('update check failed', error.message)
    })

    this.timer = setTimeout(() => {
      void this.check()
      this.timer = setInterval(() => void this.check(), checkIntervalMs)
    }, firstCheckDelayMs)
  }

  stop(): void {
    if (this.timer) {
      clearTimeout(this.timer)
      clearInterval(this.timer)
      this.timer = null
    }
  }

  private async check(): Promise<void> {
    try {
      await autoUpdater.checkForUpdates()
    } catch (error) {
      console.warn('update check failed', error)
    }
  }

  /**
   * Install it, unless that would cut her off mid-broadcast.
   *
   * Refusing is spoken rather than silent: a menu item that does nothing when
   * pressed is indistinguishable from a broken one.
   */
  async install(): Promise<void> {
    if (!this.ready) {
      return
    }
    if (await this.streaming()) {
      await this.say(this.t('update.notWhileLive'))
      return
    }
    if (Updates.portable()) {
      await this.say(this.t('update.portable', { version: this.version }))
      return
    }

    await this.say(this.t('update.installing', { version: this.version }))

    // The installer has to replace mikkilensd.exe, and Windows will not let it
    // while that file is running. Stopping the engine first turns a failed
    // install into an ordinary one.
    this.daemon.stop()
    setTimeout(() => autoUpdater.quitAndInstall(false, true), 1500)
  }

  /** Whether she is live right now, according to the engine. */
  private async streaming(): Promise<boolean> {
    try {
      const response = await fetch(`${this.engineURL}/api/state`, {
        signal: AbortSignal.timeout(2000),
      })
      if (!response.ok) {
        return false
      }
      const state = (await response.json()) as { streaming?: boolean }
      return state.streaming === true
    } catch {
      // An engine that cannot be reached is not streaming through this app,
      // and refusing to update because a status check failed would leave her
      // stuck on an old version with nothing to do about it.
      return false
    }
  }

  private async announce(): Promise<void> {
    const key = Updates.portable() ? 'update.readyPortable' : 'update.ready'
    await this.say(this.t(key, { version: this.version }))
  }

  /**
   * Speak through the engine rather than through the window.
   *
   * The engine owns the voice, the chosen output device and the queue that
   * keeps two things from being said at once. Announcing an update over the
   * top of "the stream has started" would be worse than not announcing it.
   */
  private async say(text: string): Promise<void> {
    try {
      await fetch(`${this.engineURL}/api/speak`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ text }),
        signal: AbortSignal.timeout(3000),
      })
    } catch {
      // The engine may not be running. The menu item still says it, and there
      // is nothing here worth failing over.
    }
  }
}
