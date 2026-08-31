import { app } from 'electron'
import { copyFileSync, existsSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'

/**
 * Where MikkiLens keeps the things she owns: config.toml, her command files
 * and data/.
 *
 * The engine works this out for itself when it is run from a terminal inside
 * the repository, by walking up for a marker file. That does not survive being
 * packaged: the binary then sits in an installation folder that is read-only
 * on a per-machine install, and inside a temporary extraction folder for the
 * portable build -- a folder that is thrown away and made again on every
 * launch, which would lose her settings every time she starts.
 *
 * So the app decides, and tells the engine over MIKKILENS_HOME. Both processes
 * then agree on one directory, whoever started first.
 */

/** Files the engine expects beside config.toml, seeded on a fresh install. */
const seeded = ['commands.en.toml', 'commands.id.toml', 'config.example.toml']

/** Markers that identify the repository when running from source. */
const sourceMarkers = ['config.toml', 'config.example.toml', 'go.mod']

/**
 * Markers that identify an installation she has already set up.
 *
 * Narrower than the ones above on purpose: a packaged executable can be
 * anywhere, and go.mod would make it adopt any Go project it happened to be
 * unzipped inside.
 */
const installMarkers = ['config.toml', 'commands.en.toml', 'commands.id.toml']

let resolved: string | null = null

/**
 * The directory config.toml and data/ live in.
 *
 * MIKKILENS_HOME wins, always: it is how a copy is pointed somewhere else
 * entirely. From source, the repository root is used, so the packaged app and
 * `npm run dev` do not quietly read two different configurations.
 *
 * Packaged, an executable sitting inside an installation uses that
 * installation. This matters more than it looks: the speech model and
 * onnxruntime.dll are several gigabytes and are hers to supply, so they are
 * never inside the app. An executable dropped next to an installation that
 * already has them has to find them, or it starts an engine that cannot hear
 * anything -- which is the whole product, failing silently, on a machine where
 * everything it needed was right there.
 *
 * Failing that it is %APPDATA%\MikkiLens, which survives reinstalling and
 * updating.
 */
export function homeDirectory(): string {
  if (resolved) {
    return resolved
  }
  resolved = chooseHome()
  mkdirSync(join(resolved, 'data'), { recursive: true })
  return resolved
}

function chooseHome(): string {
  const override = process.env.MIKKILENS_HOME?.trim()
  if (override) {
    return override
  }
  if (!app.isPackaged) {
    return walkUpForMarker(app.getAppPath(), sourceMarkers) ?? defaultHome()
  }

  // The portable build unpacks itself into a temporary folder, so where the
  // running process lives says nothing about where she put the executable.
  // electron-builder passes the real location in PORTABLE_EXECUTABLE_DIR.
  const placed = process.env.PORTABLE_EXECUTABLE_DIR ?? dirname(process.execPath)
  return walkUpForMarker(placed, installMarkers) ?? defaultHome()
}

function defaultHome(): string {
  return join(app.getPath('appData'), 'MikkiLens')
}

function walkUpForMarker(start: string, markers: readonly string[]): string | null {
  let directory = start
  for (;;) {
    if (markers.some((marker) => existsSync(join(directory, marker)))) {
      return directory
    }
    const parent = dirname(directory)
    if (parent === directory) {
      return null
    }
    directory = parent
  }
}

/**
 * Put the files a fresh installation needs into the home directory.
 *
 * Only ever fills in what is missing. Her command files are hers to edit, and
 * an update that overwrote the phrases she had changed would take her voice
 * control away without saying so.
 */
export function seedHome(home: string): void {
  if (!app.isPackaged) {
    return // running from the repository: the files are already there
  }
  for (const name of seeded) {
    const destination = join(home, name)
    if (existsSync(destination)) {
      continue
    }
    const source = join(process.resourcesPath, name)
    if (!existsSync(source)) {
      continue
    }
    try {
      copyFileSync(source, destination)
    } catch (error) {
      // A missing command file leaves her with the built-in phrases rather
      // than with nothing, so this is worth a line in the log and no more.
      console.warn(`could not seed ${name}`, error)
    }
  }
}

/** The engine's log, wherever the home directory turned out to be. */
export function logFile(): string {
  return join(homeDirectory(), 'data', 'mikkilens.log')
}
