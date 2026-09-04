import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/**
 * The window's own strings.
 *
 * These are separate from the engine's locales on purpose. The engine's files
 * hold every sentence MikkiLens *speaks*, and she edits them to change how it
 * talks to her; mixing menu labels and button captions into that file would
 * make it much harder to read for the one job it exists to do.
 *
 * The main process owns the catalogue and hands it to the page over the
 * preload bridge, so there is one copy on disk and the renderer never needs
 * filesystem access to read it.
 */

export const fallbackLanguage = 'en'

export type Catalog = Record<string, string>

const cache = new Map<string, Catalog>()

/**
 * Forget what has been read, so the next look goes back to disk.
 *
 * Only the dev watcher calls this. Without it, editing a locale file reloads
 * the page and changes nothing, because the catalogue it is handed is the one
 * cached at startup -- which looks like the edit not having worked.
 */
export function clearCatalogCache(): void {
  cache.clear()
}

function localesDirectory(): string {
  return join(__dirname, '..', 'locales')
}

/** Which languages the window can be shown in. */
export function availableLanguages(): string[] {
  try {
    return readdirSync(localesDirectory())
      .filter((name) => name.endsWith('.json'))
      .map((name) => name.replace(/\.json$/, ''))
      .sort()
  } catch {
    return [fallbackLanguage]
  }
}

function readCatalog(language: string): Catalog | null {
  if (cache.has(language)) {
    return cache.get(language) ?? null
  }
  try {
    const raw = readFileSync(join(localesDirectory(), `${language}.json`), 'utf8')
    const parsed = JSON.parse(raw) as Catalog
    cache.set(language, parsed)
    return parsed
  } catch {
    return null
  }
}

/**
 * The catalogue for one language, with English merged in behind it.
 *
 * A key that a translation has not covered yet falls back to English rather
 * than to nothing: a blank menu item is far worse than one in the wrong
 * language, and it is much harder to notice.
 */
export function catalog(language: string): Catalog {
  const fallback = readCatalog(fallbackLanguage) ?? {}
  if (language === fallbackLanguage) {
    return { ...fallback }
  }
  return { ...fallback, ...(readCatalog(language) ?? {}) }
}

/** Fill {placeholders} from values, leaving anything unknown visible. */
export function format(template: string, values: Record<string, string | number> = {}): string {
  return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in values ? String(values[name]) : whole,
  )
}

/** Look one string up, for the menus and the tray. */
export function translate(
  strings: Catalog,
  key: string,
  values?: Record<string, string | number>,
): string {
  const template = strings[key]
  if (template === undefined) {
    // Loud rather than blank, so a missing key is obvious and fixable.
    return `[${key}]`
  }
  return format(template, values)
}
