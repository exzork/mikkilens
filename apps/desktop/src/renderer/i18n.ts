/**
 * Translation for the page.
 *
 * The catalogue comes from the main process, which owns the files. This module
 * only decides what to do with it, and it follows the same rule the engine
 * does: a missing string is loud, never blank. A button with no label is
 * silent when the page is read aloud, which is the exact failure this app
 * exists to avoid.
 */

export type Catalog = Record<string, string>

let strings: Catalog = {}
let current = 'en'

/** The language the page is currently showing. */
export function language(): string {
  return current
}

/** Install a catalogue and update the document language for screen readers. */
export function setCatalog(languageCode: string, catalog: Catalog): void {
  current = languageCode
  strings = catalog

  // The document language decides how a screen reader pronounces the page, so
  // this is not cosmetic: Indonesian read with an English voice is unusable.
  document.documentElement.lang = strings['meta.htmlLang'] ?? languageCode
  document.title = t('app.title')
}

/** Look one string up, filling {placeholders}. */
export function t(key: string, values?: Record<string, string | number>): string {
  const template = strings[key]
  if (template === undefined) {
    return `[${key}]`
  }
  return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
    values && name in values ? String(values[name]) : whole,
  )
}

/**
 * Fill every translatable slot in a piece of the document.
 *
 * Attributes matter as much as text here: aria-label is what a screen reader
 * reads for a control whose visible label is an icon or a short word, so it
 * gets translated alongside everything else.
 */
export function applyTranslations(root: ParentNode = document): void {
  root.querySelectorAll<HTMLElement>('[data-i18n]').forEach((element) => {
    const key = element.dataset.i18n
    if (key) {
      element.textContent = t(key)
    }
  })

  const attributes: Array<[string, string]> = [
    ['data-i18n-aria-label', 'aria-label'],
    ['data-i18n-title', 'title'],
    ['data-i18n-placeholder', 'placeholder'],
  ]
  for (const [dataAttribute, target] of attributes) {
    root.querySelectorAll<HTMLElement>(`[${dataAttribute}]`).forEach((element) => {
      const key = element.getAttribute(dataAttribute)
      if (key) {
        element.setAttribute(target, t(key))
      }
    })
  }
}
