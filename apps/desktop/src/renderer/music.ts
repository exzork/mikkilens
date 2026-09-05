import { setCatalog, t } from './i18n.js'

/**
 * The typing box.
 *
 * One text field, and a list of what came back. The engine reads the results
 * out loud one at a time and starts the one she picks, so nothing on this page
 * has to be seen for the feature to work -- it is here so that whoever is
 * helping her can see the same five songs she is hearing, and so a screen
 * reader has something to move through.
 *
 * Which is also why the results are real buttons in a real list rather than
 * rows that respond to a click. Arrow keys reach them, Enter presses them, and
 * a screen reader announces them as "one of five" without being told to.
 */

interface Song {
  title: string
  artist: string
  album: string
  duration: string
  minutes: number
  seconds: number
  video_id: string
}

const bridge = window.mikkilens

const query = document.getElementById('query') as HTMLInputElement
const status = document.getElementById('status') as HTMLParagraphElement
const results = document.getElementById('results') as HTMLOListElement
const hint = document.getElementById('hint') as HTMLParagraphElement
const prompt = document.getElementById('prompt') as HTMLLabelElement
const resultsLabel = document.getElementById('resultsLabel') as HTMLElement

let engine = ''
let searching = false

void start()

async function start(): Promise<void> {
  const wanted = new URLSearchParams(window.location.search).get('language') ?? undefined
  const locale = await bridge.loadLocale(wanted)
  setCatalog(locale.language, locale.strings)

  prompt.textContent = t('music.prompt')
  resultsLabel.textContent = t('music.resultsLabel')
  hint.textContent = t('music.hint')
  document.title = t('music.title')

  engine = await bridge.engineURL()
  await loadLastResults()
  focusQuery()
}

// -- opening again ------------------------------------------------------------

// Reopened rather than reopened-and-recreated, so the second press lands on a
// field that is already there. It has to be put back to how it started, or she
// types a new song over the last one's results.
bridge.onMusicBoxReopened(() => void reset())

async function reset(): Promise<void> {
  query.value = ''
  status.textContent = ''
  await loadLastResults()
  focusQuery()
}

function focusQuery(): void {
  query.focus()
  query.select()
}

/**
 * Fill in whatever is still pickable.
 *
 * A box opened a minute after a search should still offer those five, because
 * "play number two" still plays number two -- the engine remembers them for
 * exactly this reason, and an empty list here would say otherwise.
 */
async function loadLastResults(): Promise<void> {
  try {
    const response = await fetch(`${engine}/api/music/songs`)
    const body = (await response.json()) as { songs?: Song[] }
    render(body.songs ?? [])
  } catch {
    render([])
  }
}

// -- searching ----------------------------------------------------------------

query.addEventListener('keydown', (event) => {
  if (event.key === 'Enter') {
    event.preventDefault()
    void search()
  }
})

async function search(): Promise<void> {
  const wanted = query.value.trim()
  if (wanted === '' || searching) {
    return
  }

  searching = true
  status.textContent = t('music.searching', { query: wanted })
  render([])

  try {
    const response = await fetch(`${engine}/api/music/search`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query: wanted }),
    })
    const body = (await response.json()) as { songs?: Song[]; detail?: string }

    if (!response.ok) {
      // Already said out loud by the engine, in her language. This is for
      // whoever is watching the screen.
      status.textContent = t('music.failed', { reason: body.detail ?? '' })
      return
    }
    const songs = body.songs ?? []
    render(songs)
    status.textContent =
      songs.length === 0 ? t('music.nothingFound', { query: wanted }) : t('music.found', { count: songs.length })
    if (songs.length > 0) {
      // Focus moves to the list, which is what makes the number keys safe: a
      // digit in the text field belongs to the song name.
      ;(results.querySelector('button') as HTMLButtonElement | null)?.focus()
    }
  } catch (error) {
    status.textContent = t('music.failed', { reason: String(error) })
  } finally {
    searching = false
  }
}

// -- what came back -----------------------------------------------------------

function render(songs: Song[]): void {
  results.replaceChildren()

  songs.forEach((song, index) => {
    const number = index + 1

    const button = document.createElement('button')
    button.type = 'button'
    button.addEventListener('click', () => void play(number))

    const label = document.createElement('span')
    label.className = 'number'
    label.textContent = `${number}.`
    label.setAttribute('aria-hidden', 'true')

    const detail = document.createElement('span')
    detail.className = 'song'

    const title = document.createElement('span')
    title.textContent = song.title

    const byline = document.createElement('span')
    byline.className = 'byline'
    byline.textContent = [song.artist, song.album, song.duration].filter((part) => part).join(' — ')

    detail.append(title, byline)
    button.append(label, detail)
    // The number is in the accessible name too, because the number is how she
    // picks one -- out loud, or on the keyboard, and both use the one she heard.
    button.setAttribute(
      'aria-label',
      t('music.resultLabel', { number, title: song.title, artist: song.artist }),
    )

    const item = document.createElement('li')
    item.append(button)
    results.append(item)
  })
}

async function play(number: number): Promise<void> {
  try {
    const response = await fetch(`${engine}/api/music/play`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ number }),
    })
    if (!response.ok) {
      const body = (await response.json()) as { detail?: string }
      status.textContent = t('music.failed', { reason: body.detail ?? '' })
      return
    }
    // The engine says which song out loud. Closing on the way out is what makes
    // this feel like one gesture rather than a window to tidy up after.
    void bridge.closeMusicBox()
  } catch (error) {
    status.textContent = t('music.failed', { reason: String(error) })
  }
}

// -- the keyboard -------------------------------------------------------------

document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') {
    event.preventDefault()
    void bridge.closeMusicBox()
    return
  }

  // Back to the field to search for something else, from anywhere on the page.
  if (event.key === 'Backspace' && document.activeElement !== query) {
    event.preventDefault()
    focusQuery()
    return
  }

  // One to five plays that result -- but never while she is typing, where a
  // digit is part of a song name rather than a choice between five of them.
  if (document.activeElement === query || event.ctrlKey || event.altKey || event.metaKey) {
    return
  }
  const number = Number(event.key)
  if (Number.isInteger(number) && number >= 1 && number <= results.children.length) {
    event.preventDefault()
    void play(number)
  }
})
