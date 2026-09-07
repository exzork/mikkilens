import { strict as assert } from 'node:assert'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * Checks that every string the window asks for actually exists.
 *
 * i18n.ts renders a key it cannot find as `[install.stage.voice]` -- loud on
 * purpose, so a missing string is never a blank label. Loud is only useful if
 * somebody sees it before she does, and the person who sees this one is
 * whoever is helping set the machine up, mid-download, on the first run.
 *
 * That is exactly how it went wrong. A download stage added to the engine has
 * a name in the engine's own locale files, which the Go side takes from a
 * switch of literal keys so a missing one is visible. The window builds the
 * same name by interpolation -- `install.stage.${stage}` -- which no test could
 * see, so 0.11.0 shipped a first-run download that displayed the raw key.
 *
 *     node scripts/test-strings.mjs
 */

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..', '..', '..')

const locales = {}
for (const file of readdirSync(join(here, '..', 'src', 'locales'))) {
  if (file.endsWith('.json')) {
    locales[file.replace(/\.json$/, '')] = JSON.parse(
      readFileSync(join(here, '..', 'src', 'locales', file), 'utf8'),
    )
  }
}
const languages = Object.keys(locales)
assert.ok(languages.length >= 2, 'expected at least two locale files')

// -- the interpolated ones -----------------------------------------------------
//
// Read out of the engine rather than listed here, because a list here is a
// second place to remember and the whole failure was somebody not remembering
// a second place.

const stages = new Set()
const assets = join(root, 'packages', 'audio', 'assets')
for (const file of readdirSync(assets)) {
  if (!file.endsWith('.go') || file.endsWith('_test.go')) continue
  const source = readFileSync(join(assets, file), 'utf8')
  for (const [, name] of source.matchAll(/Stage\w*\s+Stage\s*=\s*"([^"]+)"/g)) {
    stages.add(name)
  }
}
assert.ok(stages.size >= 6, `only found ${stages.size} download stages in the engine`)

for (const stage of stages) {
  for (const language of languages) {
    assert.ok(
      `install.stage.${stage}` in locales[language],
      `${language}.json has no name for the "${stage}" download, so the window ` +
        `shows [install.stage.${stage}] while it runs`,
    )
  }
}

// -- the voices ----------------------------------------------------------------
//
// Read out of the engine for the same reason as the stages. The dropdown falls
// back to the bare filename for a voice it has no description of, which is
// right for one somebody built themselves and wrong for one that ships: "F3"
// on its own says nothing about what she is choosing between.

const voices = []
const supertonic = readFileSync(
  join(root, 'packages', 'audio', 'tts', 'supertonic', 'supertonic.go'),
  'utf8',
)
const presets = supertonic.match(/PresetVoices\s*=\s*\[\]string\{([^}]*)\}/)
assert.ok(presets, 'could not find PresetVoices in the engine')
for (const [, name] of presets[1].matchAll(/"([^"]+)"/g)) {
  voices.push(name)
}
assert.equal(voices.length, 10, `expected ten preset voices, found ${voices.length}`)

for (const voice of voices) {
  for (const language of languages) {
    assert.ok(
      `voice.${voice}` in locales[language],
      `${language}.json has no description for the "${voice}" voice, so the ` +
        `dropdown offers it as a bare filename`,
    )
  }
}

// -- the static ones -----------------------------------------------------------

const markup = readFileSync(join(here, '..', 'src', 'renderer', 'index.html'), 'utf8')
const attributes = /data-i18n(?:-aria-label|-title|-placeholder)?="([^"]+)"/g
for (const [, key] of markup.matchAll(attributes)) {
  for (const language of languages) {
    assert.ok(key in locales[language], `${language}.json has no "${key}", used in index.html`)
  }
}

// -- and both halves of every pair ---------------------------------------------
//
// A string added to one language and not the other is the same fault arriving
// by a different route: it reads correctly for whoever added it and shows a
// raw key to everybody else.

for (const language of languages) {
  for (const other of languages) {
    if (language === other) continue
    for (const key of Object.keys(locales[language])) {
      assert.ok(key in locales[other], `${other}.json is missing "${key}", which ${language}.json has`)
    }
  }
}

console.log(
  `strings: all checks passed (${languages.length} languages, ` +
    `${stages.size} download stages, ${voices.length} voices)`,
)
