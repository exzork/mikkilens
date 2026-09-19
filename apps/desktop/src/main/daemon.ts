import { spawn, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { app } from 'electron'

/**
 * The window is a view onto the engine, not the engine itself.
 *
 * The daemon is a separate process on purpose: closing the window, or the
 * renderer crashing, must never stop her being able to control her stream by
 * voice. It also means the engine can already be running -- started at login,
 * or from a terminal -- and this app simply attaches to it.
 */

export interface DaemonStatus {
  /** Whether the engine is answering right now. */
  readonly reachable: boolean
  /** True when this app started the engine, rather than finding one running. */
  readonly owned: boolean
  readonly url: string
  readonly detail: string
}

/** How long to wait for a freshly spawned engine to answer. */
const startupTimeoutMs = 20_000
const pollIntervalMs = 250
const stopTimeoutMs = 10_000

export class Daemon {
  private child: ChildProcess | null = null
  private owned = false
  private detail = ''
  private starting: Promise<DaemonStatus> | null = null

  /**
   * @param url  where the engine's local API answers
   * @param home the directory holding config.toml and data/, handed to the
   *   engine so a packaged app and a hand-run engine cannot disagree about it
   * @param attachOnly never start an engine, only attach to one. This is what
   *   dev mode is: air owns the engine there and rebuilds it on every Go
   *   change, and a second one spawned from here would fight it for the
   *   microphone, the global hotkey and the port -- while looking, from the
   *   window, exactly like everything working.
   */
  constructor(
    readonly url: string,
    readonly home: string,
    readonly attachOnly = false,
  ) {}

  /** Where the engine binary lives, packaged or in the repository. */
  static executablePath(): string | null {
    const name = process.platform === 'win32' ? 'mikkilensd.exe' : 'mikkilensd'
    const candidates = [
      join(process.resourcesPath ?? '', name),
      join(app.getAppPath(), '..', '..', '..', 'dist', name),
      join(app.getAppPath(), '..', '..', 'dist', name),
      join(process.cwd(), 'dist', name),
    ]
    return candidates.find((candidate) => existsSync(candidate)) ?? null
  }

  /** Whether an engine is answering on the API. */
  async reachable(): Promise<boolean> {
    try {
      const response = await fetch(`${this.url}/api/health`, {
        signal: AbortSignal.timeout(2000),
      })
      return response.ok
    } catch {
      return false
    }
  }

  /**
   * Attach to a running engine, or start one.
   *
   * Attaching first is what makes "run at login, open the window later" work,
   * and it stops a second window from starting a second engine that would
   * fight the first one for the microphone.
   *
   * Callers that overlap -- startup still waiting on the engine while the page
   * asks for a restart -- share one attempt. Each running its own would see
   * no engine yet, and each would start one.
   */
  ensureRunning(): Promise<DaemonStatus> {
    if (!this.starting) {
      this.starting = this.attachOrStart().finally(() => {
        this.starting = null
      })
    }
    return this.starting
  }

  private async attachOrStart(): Promise<DaemonStatus> {
    if (await this.reachable()) {
      this.detail = 'attached to the engine already running'
      return this.status(true)
    }

    if (this.attachOnly) {
      this.detail =
        'the engine is not answering. In dev it is run by air: check the [go] ' +
        'output, or save a Go file to build it again.'
      return this.status(false)
    }

    const executable = Daemon.executablePath()
    if (!executable) {
      this.detail =
        'Could not find the MikkiLens engine. Build it with "npm run build:daemon".'
      return this.status(false)
    }

    let child: ChildProcess
    try {
      child = spawn(executable, ['run'], {
        // The engine is started in her home directory, not next to the
        // binary: packaged, that binary sits in an installation folder it
        // cannot write to, and MIKKILENS_HOME is what stops it looking for
        // config.toml there.
        cwd: this.home,
        env: { ...process.env, MIKKILENS_HOME: this.home },
        stdio: ['ignore', 'pipe', 'pipe'],
        windowsHide: true,
      })
    } catch (error) {
      // A refusal to launch is reported, not thrown: the window still opens,
      // and the banner tells her what happened. Throwing here would leave a
      // blank window and nothing to read.
      this.detail = `Could not start the engine: ${String(error)}`
      return this.status(false)
    }
    this.child = child
    this.owned = true

    // Each handler forgets only its own process. A restart kills one engine
    // and starts the next before the first has finished exiting, and when the
    // old one's exit arrived it used to clear the new one -- so the second
    // restart found nothing to stop, attached to the engine still running,
    // and reported a restart that never happened.
    const forget = (): void => {
      if (this.child === child) {
        this.child = null
      }
    }

    // Node reports some launch failures asynchronously rather than by
    // throwing, so this path has to be covered too.
    child.on('error', (error) => {
      this.detail = `Could not start the engine: ${error.message}`
      forget()
    })

    // The engine logs to data/mikkilens.log as well; mirroring it here is what
    // makes a failed start visible when the window is all she has open.
    child.stdout?.on('data', (chunk: Buffer) => {
      process.stdout.write(`[engine] ${chunk}`)
    })
    child.stderr?.on('data', (chunk: Buffer) => {
      process.stderr.write(`[engine] ${chunk}`)
    })
    child.on('exit', (code) => {
      if (this.child === child) {
        this.detail = `the engine stopped (exit code ${code ?? 'unknown'})`
      }
      forget()
    })

    const started = Date.now()
    while (Date.now() - started < startupTimeoutMs) {
      if (await this.reachable()) {
        this.detail = 'started the engine'
        return this.status(true)
      }
      if (!this.child) {
        return this.status(false)
      }
      await delay(pollIntervalMs)
    }

    this.detail = 'the engine did not answer in time'
    return this.status(false)
  }

  /**
   * Stop the engine, but only if this app started it.
   *
   * An engine she started herself keeps running when the window closes, which
   * is the whole point: the voice control is the product, the window is not.
   */
  stop(): Promise<void> {
    const child = this.child
    if (!child || !this.owned) {
      return Promise.resolve()
    }
    this.child = null
    if (child.exitCode !== null || child.signalCode !== null) {
      return Promise.resolve()
    }
    const exited = new Promise<void>((resolve) => {
      child.once('exit', () => resolve())
      // A process that will not go is not worth waiting on for ever; the
      // restart then finds the port still taken and says so.
      setTimeout(resolve, stopTimeoutMs)
    })
    child.kill()
    return exited
  }

  /**
   * Stop the engine and start it again.
   *
   * Waits for the old one to be gone first. Starting the next while the last
   * still held the port made it refuse to run -- "an engine is already
   * running" -- and the window attached to the dying one instead.
   *
   * An engine this window did not start is not its to stop: dev mode's, or one
   * started from run.bat. That is reported rather than called a restart.
   */
  async restart(): Promise<DaemonStatus> {
    if (!this.child && (await this.reachable())) {
      this.detail =
        'this engine was not started by the MikkiLens window, so it cannot ' +
        'restart it. Close it where it was started, then press this again.'
      return this.status(false)
    }
    await this.stop()
    return this.ensureRunning()
  }

  private status(reachable: boolean): DaemonStatus {
    return { reachable, owned: this.owned, url: this.url, detail: this.detail }
  }
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
