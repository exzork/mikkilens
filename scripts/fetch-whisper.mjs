import { spawnSync } from 'node:child_process'
import { createWriteStream } from 'node:fs'
import { mkdir, readdir, rename, rm, stat } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { fileURLToPath } from 'node:url'

/**
 * Fetch the speech recognition that actually runs on her machine.
 *
 * Two things decide how well MikkiLens hears her, and neither of them is code
 * in this repository. The model decides accuracy: `base` mishears enough of a
 * short Indonesian command to be the difference between a command that works
 * and one she has to say twice. The build decides speed: the same whisper.cpp
 * executable compiled for CUDA answers in a fifth of a second where the
 * processor-only one takes seconds, and seconds of silence after speaking are
 * long enough that she starts again -- and then the command arrives twice.
 *
 * Neither is shipped with MikkiLens. The model is hundreds of megabytes and
 * the CUDA build is a gigabyte of somebody else's binaries, and which build is
 * right depends on the card in the machine. So they are fetched here, into
 * data/models, where the engine already knows to look. Nothing is rebuilt and
 * nothing is installed system-wide; if this script is never run, MikkiLens
 * falls back to whatever is already there.
 *
 * Usage:
 *   node scripts/fetch-whisper.mjs                 the CUDA build and small
 *   node scripts/fetch-whisper.mjs --cpu           no graphics card here
 *   node scripts/fetch-whisper.mjs --model medium  a different model
 *   node scripts/fetch-whisper.mjs --build-only    leave the models alone
 */

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..')
const models = join(root, 'data', 'models')

const args = process.argv.slice(2)
const wantsCPU = args.includes('--cpu')
const buildOnly = args.includes('--build-only')
const modelOnly = args.includes('--model-only')
const modelSize = valueOf('--model') ?? 'small'

/** The releases these files come from. Pinned to the projects, not versions. */
const whisperReleases = 'https://api.github.com/repos/ggml-org/whisper.cpp/releases'
const modelHost = 'https://huggingface.co/ggerganov/whisper.cpp/resolve/main'

function valueOf(flag) {
  const index = args.indexOf(flag)
  return index >= 0 ? args[index + 1] : undefined
}

function say(message) {
  process.stdout.write(`${message}\n`)
}

function fail(message) {
  process.stderr.write(`${message}\n`)
  process.exit(1)
}

async function exists(path) {
  try {
    await stat(path)
    return true
  } catch {
    return false
  }
}

/**
 * Download to a temporary name and rename on success.
 *
 * A half-written ggml-small.bin is worse than no model at all: it is found by
 * the engine, loaded, and fails at the moment she speaks.
 */
async function download(url, destination, label) {
  await mkdir(dirname(destination), { recursive: true })
  const temporary = `${destination}.part`

  say(`  downloading ${label}…`)
  const response = await fetch(url, {
    headers: { 'User-Agent': 'mikkilens-fetch-whisper' },
    redirect: 'follow',
  })
  if (!response.ok || !response.body) {
    throw new Error(`${label}: ${response.status} ${response.statusText}`)
  }

  await pipeline(Readable.fromWeb(response.body), createWriteStream(temporary))
  await rename(temporary, destination)

  const { size } = await stat(destination)
  say(`  ${label} — ${(size / 1024 / 1024).toFixed(0)} MB`)
}

/**
 * Ask GitHub for the recent releases.
 *
 * Unauthenticated calls are rate limited per address, and a shared address
 * runs out of them long before you do. A token lifts that; the `gh` CLI is
 * asked for one when it is signed in here, because a machine that builds this
 * project usually already has it.
 */
async function releases() {
  const headers = {
    'User-Agent': 'mikkilens-fetch-whisper',
    Accept: 'application/vnd.github+json',
  }
  const token = process.env.GITHUB_TOKEN ?? process.env.GH_TOKEN ?? githubCLIToken()
  if (token) {
    headers.Authorization = `Bearer ${token}`
  }

  const response = await fetch(`${whisperReleases}?per_page=20`, { headers })
  if (response.status === 403 || response.status === 429) {
    throw new Error(
      'GitHub is rate limiting this machine. Sign in with `gh auth login`, or set ' +
        'GITHUB_TOKEN, and run this again.',
    )
  }
  if (!response.ok) {
    throw new Error(`could not ask GitHub for the whisper.cpp releases: ${response.status}`)
  }
  return response.json()
}

function githubCLIToken() {
  const result = spawnSync('gh', ['auth', 'token'], { encoding: 'utf8', shell: true })
  const token = (result.stdout ?? '').trim()
  return result.status === 0 && token ? token : undefined
}

/** The CUDA toolkit a name like whisper-cublas-12.4.0-bin-x64.zip was built with. */
function toolkitVersion(name) {
  const found = /cublas-(\d+)\.(\d+)/i.exec(name)
  return found ? Number(found[1]) * 1000 + Number(found[2]) : 0
}

/**
 * Pick the release asset to use.
 *
 * The CUDA builds are published under a name carrying the toolkit version, so
 * they are matched by shape rather than by name, and the newest toolkit wins:
 * the older one is built for cards that stopped being new years ago, and on a
 * recent card it either refuses to load or falls back to the processor
 * halfway through -- which looks like recognition being mysteriously slow.
 *
 * The newest release does not always publish Windows binaries at all, hence
 * walking back through the releases rather than asking only for the latest.
 */
async function findBuild() {
  const wanted = wantsCPU ? /^whisper-bin-x64\.zip$/i : /cublas.*bin-x64\.zip$/i

  for (const release of await releases()) {
    const matches = (release.assets ?? []).filter((asset) => wanted.test(asset.name))
    if (matches.length === 0) {
      continue
    }
    matches.sort((left, right) => toolkitVersion(right.name) - toolkitVersion(left.name))
    return {
      name: matches[0].name,
      url: matches[0].browser_download_url,
      release: release.tag_name,
    }
  }
  throw new Error(
    wantsCPU
      ? 'no processor build was published in the recent whisper.cpp releases'
      : 'no CUDA build was published in the recent whisper.cpp releases; ' +
        'run again with --cpu, or download one by hand into data/models/whisper',
  )
}

/**
 * Unpack with whatever the platform has.
 *
 * Node ships no unzip, and adding a dependency to this repository for one
 * archive a year is a worse trade than shelling out to the tool every one of
 * these machines already has.
 */
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

/**
 * Move the binaries up to where the engine looks.
 *
 * The Windows archives put everything under a Release/ directory. The engine
 * looks in data/models/whisper, not in a folder underneath it, and a build it
 * cannot see is a build that was never downloaded as far as she is concerned.
 */
async function flatten(destination, executables) {
  for (const name of executables) {
    if (await exists(join(destination, name))) {
      return
    }
  }

  for (const entry of await readdir(destination, { withFileTypes: true })) {
    if (!entry.isDirectory()) {
      continue
    }
    const inside = join(destination, entry.name)
    const holds = await Promise.all(executables.map((name) => exists(join(inside, name))))
    if (!holds.some(Boolean)) {
      continue
    }
    for (const file of await readdir(inside)) {
      await rename(join(inside, file), join(destination, file))
    }
    await rm(inside, { recursive: true, force: true })
    return
  }
}

async function fetchBuild() {
  const build = await findBuild()
  const destination = join(models, 'whisper')
  const archive = join(destination, build.name)

  say(`whisper.cpp ${build.release}: ${build.name}`)
  await download(build.url, archive, build.name)
  unpack(archive, destination)
  await rm(archive, { force: true })
  await flatten(destination, ['whisper-server.exe', 'whisper-cli.exe', 'main.exe'])

  if (!(await exists(join(destination, 'whisper-server.exe')))) {
    say('  warning: this archive has no whisper-server.exe in it. MikkiLens will')
    say('  fall back to the one-shot CLI, which reloads the model per command.')
  }

  say(`  unpacked into ${destination}`)
  if (!wantsCPU) {
    say('  MikkiLens prefers this build over a processor-only one on its own.')
  }
}

async function fetchModel() {
  const name = `ggml-${modelSize}.bin`
  const destination = join(models, name)

  if (await exists(destination)) {
    say(`${name} is already in data/models`)
    return
  }
  say(`model ${modelSize}:`)
  await download(`${modelHost}/${name}`, destination, name)
}

try {
  if (!modelOnly) {
    await fetchBuild()
  }
  if (!buildOnly) {
    await fetchModel()
  }
  say('')
  say(`Done. Set stt.model_size = "${modelSize}" in config.toml, or choose it in`)
  say('the settings app under Audio, and restart MikkiLens.')
} catch (error) {
  fail(`Could not fetch speech recognition: ${error.message}`)
}
