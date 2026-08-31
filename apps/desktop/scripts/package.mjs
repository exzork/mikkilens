import { spawnSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

/**
 * Build the installer and the portable executable.
 *
 * Two things have to be arranged before electron-builder will run in this
 * repository on Windows; both are explained where they are done.
 */

const here = dirname(fileURLToPath(import.meta.url))
const require = createRequire(import.meta.url)

/**
 * The signing tools electron-builder fetches for itself.
 *
 * Pinned because the version is baked into electron-builder's own downloader
 * and is not readable from here. If it ever asks for a different one it will
 * download that itself and fail the same way, which is the signal to bump this
 * line.
 */
const signToolsVersion = '2.6.0'

await ensureSignTools()
run()

/**
 * Unpack the signing tools ourselves, leaving the macOS half of them out.
 *
 * The archive carries tools for all three platforms, and two files in the
 * macOS part are symbolic links. Creating a symlink on Windows needs a
 * privilege an ordinary account does not have unless Developer Mode is on, so
 * electron-builder's own unpack fails and the build stops before it has
 * written anything -- over two files a Windows build has no use for.
 *
 * Unpacking it here without that directory leaves everything else identical.
 * electron-builder finds the folder already in its cache and gets on with the
 * build.
 */
async function ensureSignTools() {
  const cache = join(
    process.env.LOCALAPPDATA ?? join(process.env.USERPROFILE ?? '', 'AppData', 'Local'),
    'electron-builder',
    'Cache',
    'winCodeSign',
  )
  const target = join(cache, `winCodeSign-${signToolsVersion}`)
  if (existsSync(join(target, 'windows-10'))) {
    return
  }

  const url =
    'https://github.com/electron-userland/electron-builder-binaries/releases/' +
    `download/winCodeSign-${signToolsVersion}/winCodeSign-${signToolsVersion}.7z`
  console.log(`fetching the signing tools: ${url}`)

  const response = await fetch(url)
  if (!response.ok) {
    throw new Error(`could not download the signing tools: HTTP ${response.status}`)
  }
  const archive = join(tmpdir(), `winCodeSign-${signToolsVersion}.7z`)
  writeFileSync(archive, Buffer.from(await response.arrayBuffer()))

  mkdirSync(target, { recursive: true })
  const { path7za } = require('7zip-bin')
  const unpacked = spawnSync(
    path7za,
    ['x', '-bd', '-y', '-xr!darwin', archive, `-o${target}`],
    { stdio: 'inherit' },
  )
  if (unpacked.status !== 0) {
    throw new Error('could not unpack the signing tools')
  }
}

/**
 * Run electron-builder with the Electron version spelled out for it.
 *
 * electron-builder works out which Electron to package by looking for it in
 * the app's own node_modules. In a workspace npm hoists it to the repository
 * root instead, and electron-builder then stops with "cannot compute electron
 * version" -- which reads like a broken install rather than a layout it does
 * not recognise.
 *
 * Reading the version off the module that is actually installed keeps the one
 * declaration in package.json: upgrading Electron there is still the only
 * thing anyone has to change.
 */
function run() {
  const { version } = JSON.parse(readFileSync(require.resolve('electron/package.json'), 'utf8'))
  const builder = require.resolve('electron-builder/out/cli/cli.js')
  const result = spawnSync(
    process.execPath,
    [builder, '--win', `-c.electronVersion=${version}`, ...process.argv.slice(2)],
    { stdio: 'inherit', cwd: join(here, '..') },
  )

  if (result.error) {
    console.error('could not run electron-builder:', result.error.message)
    process.exit(1)
  }
  process.exit(result.status ?? 1)
}
