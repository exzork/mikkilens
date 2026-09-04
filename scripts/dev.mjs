import { spawn, spawnSync } from 'node:child_process'
import { existsSync, watch } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * One command that watches both halves of MikkiLens.
 *
 * The two halves reload differently and that is the whole reason this script
 * exists. The engine is a process: a Go change means rebuild it and start it
 * again, which air does. The window is three things -- a main process, a
 * preload and a page -- and only the last of them can be reloaded in place.
 * So a page change refreshes the window and keeps it where it was, while a
 * main-process change restarts Electron, because there is no way to swap that
 * code out from underneath a running app.
 *
 * The engine belongs to air here, not to Electron. Normally the window starts
 * an engine when it cannot find one; in dev that would be a second engine
 * fighting the first for the microphone, the hotkey and the port. Hence
 * --dev, which makes the window attach and never spawn.
 *
 * Usage:
 *   npm run dev
 *   npm run dev -- --silent     the engine stays mute, for a quiet room
 */

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..')
const desktop = join(root, 'apps', 'desktop')

const engineURL = process.env.MIKKILENS_API ?? 'http://127.0.0.1:8760'
const silent = process.argv.includes('--silent')

const windows = process.platform === 'win32'
const devBinary = 'mikkilensd-dev.exe'

/** Everything spawned, so Ctrl+C can take all of it down. */
const children = new Set()
let shuttingDown = false

// -- output -------------------------------------------------------------------

const colours = {
  go: '\u001b[36m', // cyan
  tsc: '\u001b[35m', // magenta
  app: '\u001b[32m', // green
  dev: '\u001b[33m', // yellow
}
const reset = '\u001b[0m'

function say(tag, message) {
  const colour = colours[tag] ?? ''
  for (const line of String(message).replace(/\s+$/, '').split('\n')) {
    process.stdout.write(`${colour}[${tag}]${reset} ${line}\n`)
  }
}

/** Spawn a long-running process and label everything it prints. */
function run(tag, command, args, options = {}) {
  const child = spawn(command, args, {
    cwd: root,
    stdio: ['ignore', 'pipe', 'pipe'],
    ...options,
    env: { ...process.env, ...(options.env ?? {}) },
  })
  children.add(child)

  child.stdout?.on('data', (chunk) => say(tag, chunk))
  child.stderr?.on('data', (chunk) => say(tag, chunk))
  child.on('error', (error) => say(tag, `could not start: ${error.message}`))
  child.on('exit', (code) => {
    children.delete(child)
    if (!shuttingDown && code !== 0 && code !== null) {
      say(tag, `exited with code ${code}`)
    }
  })
  return child
}

// -- air ----------------------------------------------------------------------

/**
 * Find air, installing it if it is not there yet.
 *
 * Checking GOPATH/bin as well as PATH is worth the few lines: `go install`
 * puts tools there and a shell that has not been reopened since will not see
 * them, which otherwise looks exactly like the install having failed.
 */
function resolveAir() {
  const candidates = ['air']

  const gopath = spawnSync('go', ['env', 'GOPATH'], { encoding: 'utf8' })
  if (gopath.status === 0) {
    const bin = join(gopath.stdout.trim(), 'bin', windows ? 'air.exe' : 'air')
    if (existsSync(bin)) {
      candidates.unshift(bin)
    }
  }

  for (const candidate of candidates) {
    if (spawnSync(candidate, ['-v'], { stdio: 'ignore' }).status === 0) {
      return candidate
    }
  }

  say('dev', 'air is not installed. Fetching it once with go install...')
  const install = spawnSync('go', ['install', 'github.com/air-verse/air@latest'], {
    stdio: 'inherit',
  })
  if (install.status !== 0) {
    say('dev', 'could not install air. Install it by hand and run this again:')
    say('dev', '    go install github.com/air-verse/air@latest')
    process.exit(1)
  }
  return resolveAir()
}

// -- the window ---------------------------------------------------------------

const require = createRequire(join(desktop, 'package.json'))
let electron = null
let restartTimer = null

/**
 * Set while a restart is deliberately killing Electron.
 *
 * Without it the exit that a restart causes is indistinguishable from the one
 * quitting the window causes, and the first rebuild of the main process ends
 * the whole session instead of reloading it.
 */
let restarting = false

function startElectron() {
  const binary = require('electron')
  restarting = false
  electron = run('app', binary, ['.', '--dev'], {
    cwd: desktop,
    env: { MIKKILENS_API: engineURL, MIKKILENS_DEV: '1' },
  })
  electron.on('exit', () => {
    electron = null
    // Quitting the window from its own menu ends the session, rather than
    // leaving watchers running against nothing.
    if (!shuttingDown && !restarting) {
      say('dev', 'the window was closed; stopping.')
      shutdown(0)
    }
  })
}

/**
 * Restart Electron, debounced.
 *
 * tsc writes a file per module, so one save lands as a burst of change events.
 * Restarting on each of them would restart the app several times over and
 * sometimes against a half-written build.
 */
function restartElectron(why) {
  if (restartTimer !== null) {
    clearTimeout(restartTimer)
  }
  restartTimer = setTimeout(() => {
    restartTimer = null
    say('dev', `${why} changed; restarting the window`)
    if (electron) {
      restarting = true
      electron.once('exit', () => startElectron())
      killTree(electron)
      return
    }
    startElectron()
  }, 400)
}

// -- waiting ------------------------------------------------------------------

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

async function engineIsUp() {
  try {
    const response = await fetch(`${engineURL}/api/health`, {
      signal: AbortSignal.timeout(1000),
    })
    return response.ok
  } catch {
    return false
  }
}

/** Wait for a condition, or give up and say so rather than hanging forever. */
async function waitFor(what, ready, timeoutMs) {
  const started = Date.now()
  while (Date.now() - started < timeoutMs) {
    if (await ready()) {
      return true
    }
    await delay(250)
  }
  say('dev', `gave up waiting for ${what}; carrying on anyway`)
  return false
}

// -- the installed app --------------------------------------------------------

/**
 * Whether a packaged MikkiLens is running.
 *
 * Checked by process name because that is what tells the two apart: the
 * installed window is MikkiLens.exe, and the one this script starts is
 * electron.exe running out of node_modules. So this never mistakes a previous
 * dev window for the real thing, or the other way round.
 */
function installedAppIsRunning() {
  if (!windows) {
    return false
  }
  const found = spawnSync('tasklist', ['/FI', 'IMAGENAME eq MikkiLens.exe', '/NH'], {
    encoding: 'utf8',
  })
  return found.status === 0 && /MikkiLens\.exe/i.test(found.stdout ?? '')
}

/**
 * Refuse to run alongside the installed app.
 *
 * Two MikkiLens windows and two engines is not a smaller problem than it
 * sounds: they fight over the microphone, the global hotkey and the port, and
 * the one that loses looks broken rather than absent. Worse, the window you
 * are looking at may be the installed one -- so the changes you are making
 * appear to do nothing, which is the single most confusing way for a dev
 * server to fail.
 */
async function refuseIfAlreadyRunning() {
  const window = installedAppIsRunning()
  const engine = await engineIsUp()
  if (!window && !engine) {
    return
  }

  say('dev', 'MikkiLens is already running on this machine:')
  if (window) {
    say('dev', '  - the installed app has a window open')
  }
  if (engine) {
    say('dev', `  - an engine is answering on ${engineURL}`)
  }
  say('dev', 'Quit it first (right-click the tray icon, Quit), then run this again.')
  say('dev', 'Otherwise there would be two windows and two engines competing for')
  say('dev', 'the microphone, the hotkey and the port -- and the window you were')
  say('dev', 'looking at might not be the one you are editing.')
  process.exit(1)
}

// -- shutdown -----------------------------------------------------------------

/**
 * Kill a child and everything below it.
 *
 * The engine is air's child, so it is this script's grandchild, and on Windows
 * killing a process does not take its descendants with it. Left behind, the
 * engine holds the microphone, the global hotkey and the port -- and the next
 * run would quietly attach to that stale engine and look like nothing was
 * reloading.
 */
function killTree(child) {
  if (!child.pid) {
    return
  }
  if (windows) {
    spawnSync('taskkill', ['/PID', String(child.pid), '/F', '/T'], { stdio: 'ignore' })
    return
  }
  child.kill()
}

/**
 * Sweep up whatever a previous run left behind.
 *
 * Ctrl+C is handled and cleans up properly. Closing the terminal, or anything
 * else that kills this script outright, is not: the handler never runs and
 * air, the engine and the window carry on without a parent. The engine matters
 * most -- it holds the microphone, the global hotkey and the port -- but a
 * stray window is the one you actually notice, because the next run puts a
 * second one beside it and only one of them is the code you are editing.
 *
 * Everything is matched on its command line containing this checkout, so a
 * second clone, an air running for another project, and an installed
 * MikkiLens are all left alone.
 */
function sweepPreviousRun() {
  if (!windows) {
    spawnSync('pkill', ['-f', devBinary], { stdio: 'ignore' })
    return
  }

  // Engines first and by name as well, in case one was started from a copy of
  // this checkout at a different path.
  spawnSync('taskkill', ['/IM', devBinary, '/F', '/T'], { stdio: 'ignore' })

  const script = [
    "$root = '" + root.replace(/'/g, "''") + "'",
    "Get-CimInstance Win32_Process |",
    "  Where-Object { $_.Name -in @('air.exe','electron.exe','" + devBinary + "') } |",
    '  Where-Object { $_.CommandLine -and $_.CommandLine.Contains($root) } |',
    '  ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }',
  ].join('\n')

  spawnSync('powershell', ['-NoProfile', '-Command', script], { stdio: 'ignore' })
}

function shutdown(code) {
  if (shuttingDown) {
    return
  }
  shuttingDown = true
  say('dev', 'stopping')

  for (const child of children) {
    killTree(child)
  }
  sweepPreviousRun()
  process.exit(code)
}

process.on('SIGINT', () => shutdown(0))
process.on('SIGTERM', () => shutdown(0))

// -- go -----------------------------------------------------------------------

async function main() {
  // Before anything else, and before the check below: a previous run killed
  // rather than closed leaves an engine that would answer the health check,
  // and a window that would sit beside the new one.
  sweepPreviousRun()

  await refuseIfAlreadyRunning()

  const air = resolveAir()
  say('dev', `engine: ${air} (dist/${devBinary}), window: electron`)
  if (silent) {
    say('dev', 'the engine will not make a sound this session')
  }

  run('go', air, ['-c', '.air.toml'], {
    env: silent ? { MIKKILENS_SILENT: '1' } : {},
  })

  // Both tsc passes rewrite everything they own on their first compile, even
  // when nothing changed. Starting Electron before that lands would have it
  // restarted by its own build a second later, so the first pass is waited
  // for rather than raced.
  let compiled = 0
  const compiling = new Promise((resolve) => {
    const watchTsc = (chunk) => {
      if (/Watching for file changes/.test(String(chunk))) {
        compiled += 1
        if (compiled === 2) {
          resolve(true)
        }
      }
    }
    for (const project of ['tsconfig.node.json', 'tsconfig.web.json']) {
      const child = run('tsc', 'npx', ['tsc', '-p', project, '--watch', '--preserveWatchOutput'], {
        cwd: desktop,
        shell: windows,
      })
      child.stdout?.on('data', watchTsc)
    }
  })

  // The page's HTML, stylesheet and locale files are copied rather than
  // compiled, so tsc never sees them and something has to.
  const copyAssets = () => {
    const done = spawnSync(process.execPath, [join(desktop, 'scripts', 'copy-assets.mjs')], {
      cwd: desktop,
      encoding: 'utf8',
    })
    if (done.status !== 0) {
      say('dev', `could not copy the page assets: ${done.stderr ?? ''}`)
    }
  }
  copyAssets()

  let copyTimer = null
  for (const directory of [join(desktop, 'src', 'renderer'), join(desktop, 'src', 'locales')]) {
    watch(directory, { recursive: true }, (_event, name) => {
      if (!name || /\.ts$/.test(String(name))) {
        return // tsc owns the TypeScript
      }
      clearTimeout(copyTimer)
      copyTimer = setTimeout(copyAssets, 150)
    })
  }

  await Promise.race([compiling, delay(60_000)])
  if (compiled < 2) {
    say('dev', 'the window is still compiling; opening it anyway')
  }
  await waitFor(
    'the first build of the window',
    async () => existsSync(join(desktop, 'out', 'main', 'index.js')),
    30_000,
  )
  await waitFor('the engine', engineIsUp, 90_000)

  startElectron()

  // Only the main process and the preload need a restart. The page reloads
  // itself: the main process watches out/renderer in dev and refreshes the
  // window in place, which keeps the tab you were on and the scroll position.
  for (const part of ['main', 'preload']) {
    const directory = join(desktop, 'out', part)
    if (!existsSync(directory)) {
      continue
    }
    watch(directory, { recursive: true }, (_event, name) => {
      if (name && /\.map$/.test(String(name))) {
        return
      }
      restartElectron(part)
    })
  }

  say('dev', 'watching. Ctrl+C stops everything.')
}

main().catch((error) => {
  say('dev', String(error))
  shutdown(1)
})
