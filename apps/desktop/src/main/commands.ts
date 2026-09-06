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
 * have is left exactly as it is, phrasing and all. Nothing is ever rewritten,
 * so an edit of hers cannot be lost by this, and the new commands land in the
 * same file she already edits rather than somewhere she cannot see them.
 *
 * Additive in one direction only was not enough. A command the application has
 * since dropped stays in her file for ever, and it does not sit there quietly:
 * its slots are no longer slots the application knows, so every start reads out
 * "phrase uses unknown slot" for each one -- a file problem, announced by ear,
 * that she cannot fix by ear. Removing scenes and sources in 0.10 turned that
 * into eight of them.
 *
 * So a retired command is commented out rather than deleted. It stops being a
 * command, which is the point; the phrases she wrote for it are still there to
 * read, which is the courtesy; and doing it in the file she already edits means
 * the change is somewhere she can see rather than somewhere she cannot.
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
 * Comment out any section of `mine` that `shipped` no longer defines.
 *
 * Not deleted. She may have spent an evening on those phrases, and a file that
 * quietly loses text is a worse thing to own than one with a few dead lines in
 * it -- which is also why the note above them says what happened rather than
 * leaving her to work it out from a diff she cannot see.
 */
export function retired(
  mine: string,
  shipped: string,
  note: string,
): { text: string; retired: string[] } | null {
  const known = new Set(commandIds(shipped))
  const gone = sections(mine).filter((section) => !known.has(section.id))
  if (gone.length === 0) {
    return null
  }

  const newline = String.fromCharCode(10)

  let text = mine
  for (const section of gone) {
    const commented = section.text
      .split(newline)
      .map((line) => (line.startsWith('#') ? line : `# ${line}`))
      .join(newline)
    text = text.replace(section.text, note + newline + commented)
  }
  return { text, retired: gone.map((section) => section.id) }
}

/**
 * Bring one command file up to date on disk.
 *
 * Failure is never fatal. A command file that could not be read or written
 * leaves her with exactly what she had a moment ago, which is a working
 * application missing some newer commands -- worth a line in the log, and
 * nothing worth interrupting a stream over.
 */
export function syncCommands(
  mine: string,
  shipped: string,
  notes: { added: string; retired: string },
): { added: string[]; retired: string[] } {
  const nothing = { added: [], retired: [] }
  try {
    const theirs = readFileSync(shipped, 'utf8')
    let text = readFileSync(mine, 'utf8')
    const result = { added: [] as string[], retired: [] as string[] }

    // Retiring first, so a command that was removed and later brought back
    // arrives as a new section rather than being commented out again.
    const out = retired(text, theirs, notes.retired)
    if (out !== null) {
      text = out.text
      result.retired = out.retired
    }
    const inbound = merged(text, theirs, notes.added)
    if (inbound !== null) {
      text = inbound.text
      result.added = inbound.added
    }

    if (result.added.length === 0 && result.retired.length === 0) {
      return nothing
    }
    writeFileSync(mine, text, 'utf8')
    return result
  } catch (error) {
    console.warn(`could not bring ${mine} up to date`, error)
    return nothing
  }
}
