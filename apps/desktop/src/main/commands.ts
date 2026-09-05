import { readFileSync, writeFileSync } from 'node:fs'

/**
 * Getting new commands into a command file she already owns.
 *
 * The command files are hers: she edits the phrases, and an update that
 * overwrote them would take away voice control she had tuned, without saying
 * so. So seeding only ever fills in what is missing, and never touches a file
 * that is already there.
 *
 * Which is right, and which quietly meant that every command added after her
 * first install never arrived. A file seeded in August had twenty-three
 * commands in it; four releases later the application knew twenty-seven, and
 * the four it had learned since -- switching channel, asking which channel,
 * listing them, asking the time -- did nothing at all when spoken. Nothing
 * reported it, because from the engine's point of view she simply never asked
 * for a command that existed.
 *
 * So: additive. A section she does not have is appended; a section she does
 * have is left exactly as it is, phrasing and all. Nothing is ever rewritten
 * or removed, so an edit of hers cannot be lost by this, and the new commands
 * land in the same file she already edits rather than somewhere she cannot
 * see them.
 */

/** One command as it is written in the file, with the comments that explain it. */
interface Section {
  id: string
  text: string
}

/** The ids a command file defines. */
export function commandIds(toml: string): string[] {
  return [...toml.matchAll(/^\[commands\.([A-Za-z0-9_]+)\]/gm)].map((found) => found[1] ?? '')
}

/**
 * Split a command file into its sections.
 *
 * Comment lines directly above a section come with it: they are what explains
 * why the phrases are the way they are, and arriving without them would make
 * the new entries the only unexplained ones in the file.
 */
export function sections(toml: string): Section[] {
  const lines = toml.split(/\r?\n/)
  const found: Section[] = []

  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index] ?? ''
    const header = /^\[commands\.([A-Za-z0-9_]+)\]/.exec(line)
    if (!header) {
      continue
    }

    // Walk back over the comment lines that introduce it, stopping at a blank
    // line, at another section, or at one of the file's own dividers.
    let from = index
    while (from > 0) {
      const above = lines[from - 1] ?? ''
      if (!above.startsWith('#') || /^#\s*-{4,}/.test(above)) {
        break
      }
      from -= 1
    }

    let to = index + 1
    while (to < lines.length && !/^\[commands\./.test(lines[to] ?? '') && !/^#\s*-{4,}/.test(lines[to] ?? '')) {
      to += 1
    }
    // Trailing blank lines belong to the gap, not to the section.
    while (to > index + 1 && (lines[to - 1] ?? '').trim() === '') {
      to -= 1
    }

    found.push({ id: header[1] ?? '', text: lines.slice(from, to).join('\n') })
  }
  return found
}

/**
 * Add to `mine` any command `shipped` defines and it does not.
 *
 * Returns the new text and which ids were added, or null when there is nothing
 * to do -- so the common case does not rewrite the file at all.
 */
export function merged(mine: string, shipped: string, note: string): { text: string; added: string[] } | null {
  const have = new Set(commandIds(mine))
  const missing = sections(shipped).filter((section) => !have.has(section.id))
  if (missing.length === 0) {
    return null
  }

  const body = missing.map((section) => section.text).join('\n\n')
  const text = `${mine.replace(/\s*$/, '')}\n\n${note}\n\n${body}\n`
  return { text, added: missing.map((section) => section.id) }
}

/**
 * Bring one command file up to date on disk.
 *
 * Failure is never fatal. A command file that could not be read or written
 * leaves her with exactly what she had a moment ago, which is a working
 * application missing some newer commands -- worth a line in the log, and
 * nothing worth interrupting a stream over.
 */
export function addNewCommands(mine: string, shipped: string, note: string): string[] {
  try {
    const current = readFileSync(mine, 'utf8')
    const theirs = readFileSync(shipped, 'utf8')
    const result = merged(current, theirs, note)
    if (result === null) {
      return []
    }
    writeFileSync(mine, result.text, 'utf8')
    return result.added
  } catch (error) {
    console.warn(`could not add new commands to ${mine}`, error)
    return []
  }
}
