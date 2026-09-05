import { spawnSync } from 'node:child_process'
import { createWriteStream } from 'node:fs'
import { copyFile, mkdir, mkdtemp, readdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { fileURLToPath } from 'node:url'

/**
 * Fetch what the wake word needs, so the installer can carry it.
 *
 * The engine can download these itself on first run, and still does when they
 * are missing. That is the wrong moment to depend on a network: the wake word
 * is how she starts talking to MikkiLens without touching anything, so a
 * machine that installed fine but happened to be offline once comes up with
 * "onnxruntime.dll was not found" and no voice -- the whole product, waiting
 * on a download she has no way to see or retry.
 *
 * So they are fetched here instead, at build time, and packaged into the
 * installer. Eighteen megabytes next to a 100 MB Electron app is not a size
 * anyone will notice, and it is the difference between a wake word that works
 * out of the box and one that works if the network cooperates.
 *
 * The speech models are deliberately NOT here: those are hundreds of megabytes
 * and which one is right depends on her machine, so they stay a first-run
 * download. See scripts/fetch-whisper.mjs.
 *
 * Usage:
 *   node scripts/fetch-wake.mjs            fetch anything missing
 *   node scripts/fetch-wake.mjs --force    fetch it again anyway
 */

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..')
const models = join(root, 'data', 'models')

const force = process.argv.slice(2).includes('--force')

/**
 * Pinned to the same versions the engine downloads.
 *
 * These must not drift from packages/audio/assets/assets.go. The runtime in
 * particular is tied to the Go binding, not chosen freely: onnxruntime_go
 * v1.35.0 is built against ORT_API_VERSION 29, so it needs runtime 1.29. An
 * older one loads and then refuses with "Error setting ORT API base", which
 * looks like a broken wake word rather than a version to bump.
 */
const onnxRuntime = '1.29.0'
const wakeWordRelease = 'v0.5.1'

/** The two stages every wake word shares, whatever the word is. */
const sharedModels = ['melspectrogram.onnx', 'embedding_model.onnx']

/**
 * Which runtime version the files in data/models came from.
 *
 * The libraries carry a version resource, but reading it means platform
 * specific work for something a one-line file answers just as well.
 */
const stampPath = join(models, '.onnxruntime-version')

async function stampedVersion() {
  try {
    return (await readFile(stampPath, 'utf8')).trim()
  } catch {
    return null
  }
}

/** What is kept out of the runtime archive: 65 MB of headers around two files. */
const runtimeLibraries = ['onnxruntime.dll', 'onnxruntime_providers_shared.dll']

function say(message) {
  process.stdout.write(`${message}\n`)
}

async function exists(path) {
  try {
    await stat(path)
    return true
  } catch {
    return false
  }
}

/** Download to a temporary name and rename on success, so a dropped connection
 * leaves nothing that later loads as a corrupt model. */
async function download(url, destination, label) {
  await mkdir(dirname(destination), { recursive: true })
  const temporary = `${destination}.part`

  say(`  downloading ${label}…`)
  const response = await fetch(url, {
    headers: { 'User-Agent': 'mikkilens-fetch-wake' },
    redirect: 'follow',
  })
  if (!response.ok || !response.body) {
    throw new Error(`${label}: ${response.status} ${response.statusText}`)
  }

  await pipeline(Readable.fromWeb(response.body), createWriteStream(temporary))
  await rename(temporary, destination)

  const { size } = await stat(destination)
  say(`  ${label} — ${(size / 1024 / 1024).toFixed(1)} MB`)
}

/** Unpack with whatever the platform has; Node ships no unzip. */
function unpack(archive, destination) {
  const command =
    process.platform === 'win32'
      ? {
          file: 'powershell',
          args: [
            '-NoProfile',
            '-Command',
            `Expand-Archive -LiteralPath '${archive}' -DestinationPath '${destination}' -Force`,
          ],
        }
      : { file: 'unzip', args: ['-o', archive, '-d', destination] }

  const result = spawnSync(command.file, command.args, { stdio: 'inherit' })
  if (result.status !== 0) {
    throw new Error(`could not unpack ${archive}`)
  }
}

/** Find one file anywhere under a directory, by name. */
async function findWithin(directory, name) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const candidate = join(directory, entry.name)
    if (entry.isDirectory()) {
      const found = await findWithin(candidate, name)
      if (found) {
        return found
      }
    } else if (entry.name.toLowerCase() === name.toLowerCase()) {
      return candidate
    }
  }
  return null
}

/**
 * The ONNX runtime, flattened out of the release archive into data/models.
 *
 * Only fetched on Windows. The Linux and macOS builds of the runtime are not
 * what this installer ships, and guessing one would put a library on disk that
 * the engine would load and fail on.
 */
async function fetchRuntime() {
  if (process.platform !== 'win32') {
    say('  not Windows: put libonnxruntime.so in data/models yourself')
    return
  }

  // What is on disk is checked against the version that put it there, not
  // merely for being present. A runtime left over from an older pin is the one
  // failure worth catching here: it loads, refuses at startup with "Error
  // setting ORT API base", and looks like a broken wake word rather than a
  // stale file. Presence alone would keep it forever.
  const present = await Promise.all(runtimeLibraries.map((name) => exists(join(models, name))))
  if (!force && present.every(Boolean) && (await stampedVersion()) === onnxRuntime) {
    say(`  the ONNX runtime ${onnxRuntime} is already here`)
    return
  }

  const url =
    `https://github.com/microsoft/onnxruntime/releases/download/v${onnxRuntime}/` +
    `onnxruntime-win-x64-${onnxRuntime}.zip`

  const scratch = await mkdtemp(join(tmpdir(), 'mikkilens-onnx-'))
  try {
    const archive = join(scratch, 'onnxruntime.zip')
    await download(url, archive, `onnxruntime ${onnxRuntime}`)
    unpack(archive, scratch)

    for (const name of runtimeLibraries) {
      const found = await findWithin(scratch, name)
      if (!found) {
        throw new Error(`${name} was not in the onnxruntime archive`)
      }
      await mkdir(models, { recursive: true })
      await copyFile(found, join(models, name))
      say(`  ${name}`)
    }
    await writeFile(stampPath, onnxRuntime, 'utf8')
  } finally {
    await rm(scratch, { recursive: true, force: true })
  }
}

/**
 * The spectrogram and embedding stages.
 *
 * The wake word itself is not fetched: MikkiLens embeds its own in the engine
 * executable and writes it out on start, so there is nothing here that can go
 * stale against the name in config.toml.
 */
async function fetchSharedModels() {
  for (const name of sharedModels) {
    const target = join(models, name)
    if (!force && (await exists(target))) {
      say(`  ${name} is already here`)
      continue
    }
    const url =
      `https://github.com/dscripka/openWakeWord/releases/download/${wakeWordRelease}/${name}`
    await download(url, target, name)
  }
}

async function main() {
  say('Wake word files:')
  await fetchRuntime()
  await fetchSharedModels()
  say('Done. These are packaged into the installer by `npm run package`.')
}

main().catch((error) => {
  process.stderr.write(`${error.message}\n`)
  process.exit(1)
})
