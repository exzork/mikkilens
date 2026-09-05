import { strict as assert } from 'node:assert'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

/**
 * Checks on the one piece of this application that writes to a file she owns.
 *
 * Her command file is where her voice control actually lives, and merging into
 * it is the only thing here that edits something she may have spent an evening
 * tuning. A mistake would not throw -- it would quietly replace her phrasing
 * with the shipped one, and she would find out mid-stream when a command she
 * had fixed stopped answering. So the rules are pinned rather than assumed.
 *
 *     node scripts/test-commands.mjs
 */

const here = dirname(fileURLToPath(import.meta.url))
// pathToFileURL, because a Windows absolute path is not a URL the ESM loader
// will take: it reads "D:" as a protocol.
const { merged, sections, commandIds } = await import(
  pathToFileURL(join(here, '..', 'out', 'main', 'commands.js')).href
)

const NOTE = '# added by an update'

const hers = `[commands.mute_mic]
phrases = ["matiin mic dong", "bisukan mik"]

[commands.status]
phrases = ["status"]
`

const shipped = `# Why this one is worded the way it is.
[commands.mute_mic]
phrases = ["matikan mikrofon"]

[commands.status]
phrases = ["status"]

# The clock, asked out loud.
[commands.current_time]
phrases = ["jam berapa sekarang"]
`

const result = merged(hers, shipped, NOTE)
assert.ok(result, 'a command she does not have must be added')
assert.deepEqual(result.added, ['current_time'], 'only the missing one is added')

// The whole point: her file is hers.
assert.ok(result.text.includes('matiin mic dong'), 'her phrasing must survive')
assert.ok(!result.text.includes('"matikan mikrofon"'), 'the shipped phrasing must not replace hers')
assert.ok(result.text.startsWith(hers.replace(/\s*$/, '')), 'her file must be left byte for byte')

// The comment above a command explains why it is worded that way; arriving
// without it would make the new entries the only unexplained ones in the file.
assert.ok(result.text.includes('The clock, asked out loud'), 'the comment comes with it')

// Running on every start, so doing nothing when there is nothing to do matters.
assert.equal(merged(result.text, shipped, NOTE), null, 'a second run must change nothing')
assert.equal(merged(shipped, shipped, NOTE), null, 'an identical file must not be rewritten')
assert.equal(merged(shipped, hers, NOTE), null, 'a file ahead of the shipped one is left alone')

// The dividers the real files use must not be dragged along with a section.
const withDivider = `[commands.a]
phrases = ["a"]

# ----------------------------------------------------------------- system ----

[commands.b]
phrases = ["b"]
`
assert.deepEqual(commandIds(withDivider), ['a', 'b'], 'both sections are found')
assert.ok(!sections(withDivider)[1].text.includes('-----'), 'a divider stays where it is')

console.log('commands: all checks passed')
