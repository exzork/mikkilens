import { strict as assert } from 'node:assert'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

/**
 * Checks on the one place two spellings of a key have to agree.
 *
 * The engine registers its keys through Windows and has always written them
 * `<ctrl>+<alt>+<f>`; Electron takes `Control+Alt+F`. Config keeps the spelling
 * she already knows, and this translates it -- so a mistake here is a key that
 * silently does nothing, which is indistinguishable from a broken keyboard.
 *
 *     node scripts/test-keys.mjs
 */

const here = dirname(fileURLToPath(import.meta.url))
// pathToFileURL, because a Windows absolute path is not a URL the ESM loader
// will take: it reads "D:" as a protocol.
const { toAccelerator } = await import(
  pathToFileURL(join(here, '..', 'out', 'main', 'music.js')).href
)

// The default, and the shape every key in her config file is written in.
assert.equal(toAccelerator('<ctrl>+<alt>+<f>'), 'Control+Alt+F')
assert.equal(toAccelerator('<ctrl>+<alt>+<m>'), 'Control+Alt+M')
assert.equal(toAccelerator('<ctrl>+<alt>+<space>'), 'Control+Alt+Space')

// Angle brackets are how the file marks a named key, and a letter needs none.
// Both spellings reach here, because both are things she may have typed.
assert.equal(toAccelerator('ctrl+alt+f'), 'Control+Alt+F')
assert.equal(toAccelerator(' <Ctrl> + <Alt> + <F> '), 'Control+Alt+F')

// Function keys and digits, which is where a Stream Deck or a macro key lands.
assert.equal(toAccelerator('<f13>'), 'F13')
assert.equal(toAccelerator('<ctrl>+<shift>+<f9>'), 'Control+Shift+F9')
assert.equal(toAccelerator('<alt>+7'), 'Alt+7')

// Named keys the engine knows and Electron spells differently.
assert.equal(toAccelerator('<ctrl>+<page_up>'), 'Control+PageUp')
assert.equal(toAccelerator('<ctrl>+<enter>'), 'Control+Return')
assert.equal(toAccelerator('<win>+<up>'), 'Super+Up')

// Anything it cannot read has to come back as nothing, so the caller logs a
// warning rather than registering some other key. The numeric keypad and the
// left and right halves of a modifier are real entries in the engine's own
// vocabulary that Electron has no name for -- registering "Control" for
// "ctrl_l" would take a key she never asked for.
assert.equal(toAccelerator('<num_5>'), null)
assert.equal(toAccelerator('<ctrl_l>+<alt>+<f>'), null)
assert.equal(toAccelerator('<not_a_key>'), null)
assert.equal(toAccelerator(''), null)
assert.equal(toAccelerator('   '), null)

console.log('keys: all checks passed')
