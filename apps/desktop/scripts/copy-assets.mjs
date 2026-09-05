import { cp, mkdir } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * TypeScript compiles the code; everything else has to be carried across by
 * hand. Keeping this a plain script rather than a bundler is deliberate: the
 * page is a few hundred lines of DOM code, and a build chain would be more to
 * install, more to break, and nothing to show for it.
 */

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..')

const assets = [
  ['src/renderer/index.html', 'out/renderer/index.html'],
  ['src/renderer/music.html', 'out/renderer/music.html'],
  ['src/renderer/style.css', 'out/renderer/style.css'],
  ['src/locales', 'out/locales'],
]

for (const [from, to] of assets) {
  const destination = join(root, to)
  await mkdir(dirname(destination), { recursive: true })
  await cp(join(root, from), destination, { recursive: true })
  console.log(`copied ${from} -> ${to}`)
}
