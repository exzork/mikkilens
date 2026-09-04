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

export class Daemon {
  private child: ChildProcess | null = null
  private owned = false
  private detail = ''

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
   */
  async ensureRunning(): Promise<DaemonStatus> {
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

    try {
      this.child = spawn(executable, ['run'], {
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
    this.owned = true

    // Node reports some launch failures asynchronously rather than by
    // throwing, so this path has to be covered too.
    this.child.on('error', (error) => {
      this.detail = `Could not start the engine: ${error.message}`
      this.child = null
    })

    // The engine logs to data/mikkilens.log as well; mirroring it here is what
    // makes a failed start visible when the window is all she has open.
    this.child.stdout?.on('data', (chunk: Buffer) => {
      process.stdout.write(`[engine] ${chunk}`)
    })
    this.child.stderr?.on('data', (chunk: Buffer) => {
      process.stderr.write(`[engine] ${chunk}`)
    })
    this.child.on('exit', (code) => {
      this.detail = `the engine stopped (exit code ${code ?? 'unknown'})`
      this.child = null
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
  stop(): void {
    if (!this.child || !this.owned) {
      return
    }
    this.child.kill()
    this.child = null
  }

  private status(reachable: boolean): DaemonStatus {
    return { reachable, owned: this.owned, url: this.url, detail: this.detail }
  }
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
