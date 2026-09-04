import { applyTranslations, setCatalog, t } from './i18n.js'
import type {
  AppConfig,
  ChannelInfo,
  ChannelsPayload,
  CommandsPayload,
  CommandSpec,
  DeviceInfo,
  DeviceList,
  EngineStatus,
  Health,
  LogPayload,
  OBSProfiles,
  Snapshot,
  VoiceInfo,
  WakeStatus,
  YouTubeStatus,
} from './types.js'

/**
 * The settings page.
 *
 * Every action reports its outcome into an ARIA live region, so a screen
 * reader announces it without the user going looking. That is the same rule
 * the voice side follows: if something happened and nothing said so, it may as
 * well not have happened.
 */

const bridge = window.mikkilens

let engineURL = 'http://127.0.0.1:8760'
let settings: AppConfig | null = null
let commands: Record<string, CommandSpec> = {}
let commandOrder: string[] = []
let snapshot: Snapshot = {}

// -- small helpers ------------------------------------------------------------

function element<T extends HTMLElement>(id: string): T {
  const found = document.getElementById(id)
  if (!found) {
    throw new Error(`the page is missing #${id}`)
  }
  return found as T
}

function announce(message: string): void {
  element('announcer').textContent = message
}

function alarm(message: string): void {
  element('alert').textContent = message
}

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`${engineURL}/api${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  })
  if (!response.ok) {
    let detail = response.statusText
    try {
      const body = (await response.json()) as { detail?: string }
      detail = body.detail ?? detail
    } catch {
      // The status line is all we have; that is still worth reporting.
    }
    throw new Error(detail)
  }
  if (response.status === 204) {
    return undefined as T
  }
  return (await response.json()) as T
}

function reason(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/**
 * Read a number out of a field, falling back to what is already configured.
 *
 * A blank or half-typed field must never be saved as zero. Zero earcon volume
 * is a MikkiLens that has quietly stopped making its tones; zero for the OBS
 * port is one that can no longer reach OBS. Both look like the app breaking
 * for no reason, and neither is anything she asked for.
 */
function numberFrom(id: string, fallback: number): number {
  const parsed = Number.parseFloat(element<HTMLInputElement>(id).value)
  return Number.isFinite(parsed) ? parsed : fallback
}

/**
 * Sliders.
 *
 * Everything the engine calls a rate or a volume is a percentage, and typing
 * "+15%" into a box is both a spelling test and something she cannot check by
 * looking. A slider with the number beside it is one gesture, and it cannot be
 * saved as nonsense.
 *
 * Two kinds share the treatment: a signed percentage the engine wants as a
 * string ("+15%"), and a plain fraction it wants as a number (0.25), shown as
 * a percentage because that is what it means.
 */
function showSliderValue(slider: HTMLInputElement): void {
  const output = document.getElementById(`${slider.id}-out`)
  if (!output) {
    return
  }
  const value = Number(slider.value)
  output.textContent =
    slider.dataset.unit === 'fraction'
      ? `${Math.round(value * 100)}%`
      : `${value > 0 ? '+' : ''}${value}%`
}

for (const slider of document.querySelectorAll<HTMLInputElement>('input[type="range"]')) {
  slider.addEventListener('input', () => showSliderValue(slider))
}

/**
 * Put a configured percentage on its slider.
 *
 * A value from the config file outside the slider's range widens the slider
 * rather than being clamped: opening the page and pressing Save must never
 * quietly change a setting she never touched.
 */
function showPercent(id: string, configured: string): void {
  const slider = element<HTMLInputElement>(id)
  const parsed = Number.parseFloat(configured)
  const value = Number.isFinite(parsed) ? parsed : 0

  if (value < Number(slider.min)) {
    slider.min = String(value)
  }
  if (value > Number(slider.max)) {
    slider.max = String(value)
  }
  slider.value = String(value)
  showSliderValue(slider)
}

/** A slider's position, in the form the engine reads: "+15%", "-20%", "+0%". */
function percentFrom(id: string): string {
  const value = Number(element<HTMLInputElement>(id).value)
  return `${value >= 0 ? '+' : ''}${value}%`
}

function showFraction(id: string, value: number): void {
  const slider = element<HTMLInputElement>(id)
  slider.value = String(value)
  showSliderValue(slider)
}

// -- tabs ---------------------------------------------------------------------

const tabs = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="tab"]'))

function selectTab(tab: HTMLButtonElement): void {
  for (const other of tabs) {
    const selected = other === tab
    other.setAttribute('aria-selected', String(selected))
    other.tabIndex = selected ? 0 : -1

    const panelId = other.getAttribute('aria-controls')
    if (panelId) {
      element(panelId).hidden = !selected
    }
  }
  tab.focus()

  // The live meters belong to one panel, so they run only while it is open.
  updateLivePolling()
}

for (const tab of tabs) {
  tab.addEventListener('click', () => selectTab(tab))
  // Arrow-key navigation is what the tab pattern promises a screen reader.
  tab.addEventListener('keydown', (event) => {
    const index = tabs.indexOf(tab)
    let next: HTMLButtonElement | undefined
    if (event.key === 'ArrowRight') next = tabs[(index + 1) % tabs.length]
    if (event.key === 'ArrowLeft') next = tabs[(index - 1 + tabs.length) % tabs.length]
    if (event.key === 'Home') next = tabs[0]
    if (event.key === 'End') next = tabs[tabs.length - 1]
    if (next) {
      event.preventDefault()
      selectTab(next)
    }
  })
}

// -- high contrast ------------------------------------------------------------

const contrastButton = element<HTMLButtonElement>('contrast')
contrastButton.addEventListener('click', () => {
  const on = document.documentElement.getAttribute('data-contrast') !== 'high'
  document.documentElement.setAttribute('data-contrast', on ? 'high' : 'normal')
  contrastButton.setAttribute('aria-pressed', String(on))
  try {
    localStorage.setItem('mikkilens-contrast', on ? 'high' : 'normal')
  } catch {
    // Remembering the choice is a convenience; losing it is not a failure.
  }
})

try {
  if (localStorage.getItem('mikkilens-contrast') === 'high') {
    contrastButton.click()
  }
} catch {
  // As above.
}

// -- status -------------------------------------------------------------------

const statusLabels: Array<[keyof Snapshot & string, string]> = [
  ['streaming', 'label.streaming'],
  ['current_scene', 'label.currentScene'],
  ['mic_muted', 'label.micMuted'],
  ['viewer_count', 'label.viewerCount'],
  ['chat_reading', 'label.chatReading'],
  ['chat_backlog', 'label.chatBacklog'],
  ['last_transcript', 'label.lastTranscript'],
  ['last_command', 'label.lastCommand'],
  ['installing', 'label.installing'],
  ['stt_backend', 'label.sttBackend'],
  ['wake_model', 'label.wakeModel'],
  ['hotkey', 'label.hotkey'],
  ['command_count', 'label.commandCount'],
]

function displayValue(key: string, value: unknown): string {
  if (key === 'installing') {
    return describeDownload(value as Snapshot['installing'])
  }
  if (typeof value === 'boolean') {
    return value ? t('value.yes') : t('value.no')
  }
  if (value === null || value === undefined || value === '') {
    return key === 'wake_model' || key === 'hotkey' ? t('value.disabled') : t('value.none')
  }
  return String(value)
}

/**
 * The first-run download, as one line.
 *
 * The engine says all of this aloud as it happens, which is the channel that
 * matters. This row is for the person helping her set the machine up, who is
 * looking at the screen and wants to know whether half a gigabyte is actually
 * moving or has quietly stalled -- so it carries the speed, not just a
 * percentage that could sit still for a minute and look identical either way.
 */
function describeDownload(progress: Snapshot['installing']): string {
  if (!progress || progress.done) {
    return t('value.none')
  }
  if (progress.failed) {
    return t('install.failed', { reason: progress.failed })
  }

  const what = t(`install.stage.${progress.stage}`)
  if (progress.total <= 0) {
    return what
  }
  return t('install.progress', {
    what,
    percent: String(progress.percent),
    speed: megabytesPerSecond(progress.bytes_per_second),
  })
}

function megabytesPerSecond(speed: number): string {
  return speed > 0 ? `${(speed / (1024 * 1024)).toFixed(1)} MB/s` : '—'
}

function renderStatus(): void {
  const grid = element('status-grid')
  grid.replaceChildren()

  for (const [key, labelKey] of statusLabels) {
    // A div around each pair is valid inside a dl, and it is what lets the
    // readings flow into as many columns as the window has room for.
    const pair = document.createElement('div')

    const term = document.createElement('dt')
    term.textContent = t(labelKey)

    const value = document.createElement('dd')
    const text = displayValue(key, snapshot[key])
    value.textContent = text
    value.title = text // the whole reading, when the column has clipped it

    pair.append(term, value)
    grid.append(pair)
  }

  showRecognitionBackend()

  const health = element('health-list')
  health.replaceChildren()
  for (const key of ['obs', 'youtube', 'chat', 'vision'] as const) {
    const value = (snapshot[key] ?? 'unknown') as Health
    const row = document.createElement('li')

    const name = document.createElement('span')
    name.textContent = key.toUpperCase()

    // The badge carries a word as well as a colour, so it still reads
    // correctly to a screen reader and in high contrast.
    const badge = document.createElement('span')
    badge.className = `badge ${value}`
    badge.textContent = t(`health.${value}`)

    row.append(name, badge)
    health.append(row)
  }

  element('headline').textContent = snapshot.streaming
    ? t('status.live', { scene: snapshot.current_scene || '?' })
    : t('status.notLive')
}

element('listen-now').addEventListener('click', async () => {
  try {
    await api('/listen', { method: 'POST' })
    announce(t('status.listening'))
  } catch (error) {
    alarm(reason(error))
  }
})

// Stopping a live stream is not undoable, so it asks once. The confirmation
// lives in the button itself rather than a dialog: a dialog moves focus, and
// where focus went is the hardest thing to work out without seeing it.
let stopArmed = false

element('go-live').addEventListener('click', async () => {
  try {
    const result = await api<{ streaming: boolean; unchanged: boolean }>('/obs/stream', {
      method: 'POST',
      body: JSON.stringify({ active: true }),
    })
    announce(result.unchanged ? t('status.alreadyLive') : t('status.wentLive'))
  } catch (error) {
    alarm(reason(error))
  }
})

element('stop-stream').addEventListener('click', async () => {
  const button = element<HTMLButtonElement>('stop-stream')

  if (!stopArmed) {
    stopArmed = true
    button.textContent = t('status.stopStreamConfirm')
    announce(t('status.stopStreamConfirm'))
    // Disarms itself, so a press left forgotten cannot end a stream later.
    window.setTimeout(() => {
      stopArmed = false
      button.textContent = t('status.stopStream')
    }, 8000)
    return
  }

  stopArmed = false
  button.textContent = t('status.stopStream')
  try {
    const result = await api<{ streaming: boolean; unchanged: boolean }>('/obs/stream', {
      method: 'POST',
      body: JSON.stringify({ active: false }),
    })
    announce(result.unchanged ? t('status.notLive') : t('status.stoppedStream'))
  } catch (error) {
    alarm(reason(error))
  }
})

element('restart-engine').addEventListener('click', async () => {
  const result = await bridge.restartEngine()
  if (result.reachable) {
    announce(t('status.engineRestarted'))
    showEngineBanner(null)
    await boot()
    return
  }
  alarm(t('status.engineRestartFailed', { detail: result.detail }))
})

function showEngineBanner(message: string | null): void {
  const banner = element('engine-banner')
  if (message === null) {
    banner.hidden = true
    banner.textContent = ''
    return
  }
  banner.hidden = false
  banner.textContent = message
}

// -- live updates -------------------------------------------------------------

let socket: WebSocket | null = null

function connectSocket(): void {
  socket = new WebSocket(`${engineURL.replace(/^http/, 'ws')}/ws`)

  socket.onmessage = (event) => {
    const message = JSON.parse(String(event.data)) as {
      type: 'snapshot' | 'delta'
      data: Snapshot
    }
    // Merged, never replaced: a snapshot from an older engine might not carry
    // every field, and blanking the page on reconnect would lose exactly the
    // readings she opened it to see.
    snapshot = { ...snapshot, ...message.data }
    renderStatus()

    if (message.type === 'delta' && 'listening' in message.data) {
      announce(message.data.listening ? t('status.listening') : t('status.doneListening'))
    }
  }

  socket.onclose = () => {
    element('headline').textContent = t('status.engineReconnecting')
    setTimeout(connectSocket, 2000)
  }
}

// -- devices ------------------------------------------------------------------

function renderDevices(
  container: HTMLElement,
  devices: DeviceInfo[],
  kind: 'output' | 'input',
  selectedName: string,
): void {
  container.replaceChildren()

  for (const device of devices) {
    const row = document.createElement('div')
    row.className = 'device-row'

    const radio = document.createElement('input')
    radio.type = 'radio'
    radio.name = `${kind}-device`
    radio.id = `${kind}-${device.index}`
    radio.value = device.name
    radio.checked = selectedName ? device.name === selectedName : device.is_default

    const label = document.createElement('label')
    label.setAttribute('for', radio.id)
    label.textContent = device.label

    row.append(radio, label)

    if (kind === 'output') {
      // Pressing Test on each device in turn is how she finds her headphones
      // without being able to read the list.
      const test = document.createElement('button')
      test.type = 'button'
      test.textContent = t('audio.test')
      test.setAttribute('aria-label', t('audio.testOn', { name: device.name }))
      test.addEventListener('click', async () => {
        try {
          await api('/devices/test', {
            method: 'POST',
            body: JSON.stringify({ index: device.index }),
          })
          announce(t('audio.playingOn', { name: device.name }))
        } catch (error) {
          alarm(reason(error))
        }
      })
      row.append(test)
    }
    container.append(row)
  }
}

// -- commands -----------------------------------------------------------------

function renderCommands(payload: CommandsPayload): void {
  // Everything here is defended against a missing list. This page is how she
  // diagnoses a MikkiLens that is misbehaving, so it has to survive a server
  // that is misbehaving too.
  commands = payload.commands ?? {}
  const order = payload.order ?? []
  commandOrder = order.length > 0 ? order : Object.keys(commands)

  const problems = payload.warnings ?? []
  const warnings = element('command-warnings')
  warnings.hidden = problems.length === 0
  warnings.textContent = problems.join(' · ')

  const list = element('command-list')
  list.replaceChildren()

  for (const id of commandOrder) {
    const spec = commands[id]
    if (!spec) {
      continue
    }

    const details = document.createElement('details')
    details.className = 'command'

    const summary = document.createElement('summary')
    summary.textContent = id

    const meta = document.createElement('span')
    meta.className = 'meta'
    meta.textContent = t('commands.meta', {
      count: spec.phrases.length,
      confirm: spec.confirm ? t('commands.metaConfirm') : '',
      handled: spec.handled ? '' : t('commands.metaUnhandled'),
    })
    summary.append(meta)

    const hint = document.createElement('p')
    hint.className = 'hint'
    hint.textContent = t('commands.onePerLine')

    const area = document.createElement('textarea')
    area.id = `phrases-${id}`
    area.value = spec.phrases.join('\n')
    area.setAttribute('aria-label', t('commands.phrasesLabel', { command: id }))
    area.addEventListener('change', () => {
      spec.phrases = area.value
        .split('\n')
        .map((line) => line.trim())
        .filter(Boolean)
    })

    const confirmWrap = document.createElement('p')
    const confirmBox = document.createElement('input')
    confirmBox.type = 'checkbox'
    confirmBox.id = `confirm-${id}`
    confirmBox.checked = spec.confirm
    confirmBox.addEventListener('change', () => {
      spec.confirm = confirmBox.checked
    })
    const confirmLabel = document.createElement('label')
    confirmLabel.setAttribute('for', confirmBox.id)
    confirmLabel.textContent = t('commands.askFirst')
    confirmWrap.append(confirmBox, confirmLabel)

    details.append(summary, hint, area, confirmWrap)
    list.append(details)
  }
}

element('save-commands').addEventListener('click', async () => {
  try {
    const result = await api<{ count: number; warnings: string[] }>('/commands', {
      method: 'PUT',
      body: JSON.stringify({ commands }),
    })
    announce(t('commands.saved', { count: result.count }))
    if (result.warnings.length > 0) {
      alarm(t('commands.warning', { warnings: result.warnings.join(' · ') }))
    }
    await loadCommands()
  } catch (error) {
    alarm(t('commands.saveFailed', { reason: reason(error) }))
  }
})

element('reload-commands').addEventListener('click', async () => {
  try {
    const result = await api<{ count: number }>('/commands/reload', { method: 'POST' })
    announce(t('commands.reloaded', { count: result.count }))
    await loadCommands()
  } catch (error) {
    alarm(reason(error))
  }
})

async function loadCommands(): Promise<void> {
  renderCommands(await api<CommandsPayload>('/commands'))
}

// -- saving settings ----------------------------------------------------------

async function saveConfig(patch: Record<string, unknown>, message: string): Promise<void> {
  try {
    const result = await api<{ config: AppConfig }>('/config', {
      method: 'PUT',
      body: JSON.stringify(patch),
    })
    settings = result.config
    announce(message)
  } catch (error) {
    alarm(t('common.saveFailed', { reason: reason(error) }))
  }
}

element('save-audio').addEventListener('click', () => {
  const output = document.querySelector<HTMLInputElement>('input[name="output-device"]:checked')
  const input = document.querySelector<HTMLInputElement>('input[name="input-device"]:checked')

  void saveConfig(
    {
      speech: {
        output_device: output ? output.value : '',
        voice: element<HTMLSelectElement>('voice').value,
        rate: percentFrom('rate'),
        volume: percentFrom('volume'),
        chat_rate: percentFrom('chat-rate'),
        chat_volume: percentFrom('chat-volume'),
        earcon_volume: numberFrom('earcon-volume', settings?.speech.earcon_volume ?? 0.25),
      },
      audio: { input_device: input ? input.value : '' },
      stt: {
        model_size: element<HTMLSelectElement>('stt-model-size').value,
        device: element<HTMLSelectElement>('stt-device').value,
      },
    },
    t('audio.saved'),
  )
})

element('preview-voice').addEventListener('click', async () => {
  try {
    await api('/speak', {
      method: 'POST',
      body: JSON.stringify({
        text: t('audio.sampleText'),
        voice: element<HTMLSelectElement>('voice').value,
      }),
    })
    announce(t('audio.playingSample'))
  } catch (error) {
    alarm(reason(error))
  }
})

element('save-obs').addEventListener('click', () => {
  void saveConfig(
    {
      obs: {
        host: element<HTMLInputElement>('obs-host').value,
        port: numberFrom('obs-port', settings?.obs.port ?? 4455),
        password: element<HTMLInputElement>('obs-password').value,
        mic_source: element<HTMLInputElement>('obs-mic').value,
      },
    },
    t('obs.saved'),
  )
})


element('save-donations').addEventListener('click', () => {
  void saveConfig(
    {
      tako: {
        enabled: element<HTMLInputElement>('tako-enabled').checked,
        link: element<HTMLInputElement>('tako-link').value.trim(),
        read_aloud: element<HTMLInputElement>('tako-read').checked,
      },
      trakteer: {
        enabled: element<HTMLInputElement>('trakteer-enabled').checked,
        link: element<HTMLInputElement>('trakteer-link').value.trim(),
        read_aloud: element<HTMLInputElement>('trakteer-read').checked,
      },
      chat: {
        max_gift_recipients: numberFrom(
          'max-gift-recipients',
          settings?.chat.max_gift_recipients ?? 5,
        ),
      },
    },
    t('donations.saved'),
  )
})

element('test-obs').addEventListener('click', async () => {
  const result = element('obs-result')
  result.textContent = t('obs.testing')

  try {
    const answer = await api<{
      ok: boolean
      scenes?: string[]
      current_scene?: string
      error?: string
    }>('/test/obs', { method: 'POST' })

    const text = answer.ok
      ? t('obs.connected', {
          scenes: (answer.scenes ?? []).join(', '),
          current: answer.current_scene ?? '?',
        })
      : t('obs.failed', { reason: answer.error ?? '' })
    result.textContent = text
    announce(text)
  } catch (error) {
    const text = t('obs.failed', { reason: reason(error) })
    result.textContent = text
    alarm(text)
  }
})

// One endpoint, saved in one place. The matcher switch lives here rather than
// in a section of its own, because what it decides is about this endpoint: it
// is the only thing that sends her speech to it.
element('save-model').addEventListener('click', async () => {
  await saveConfig(
    {
      model: {
        base_url: element<HTMLInputElement>('model-url').value.trim(),
        model: element<HTMLInputElement>('model-name').value.trim(),
      },
      matcher: { enabled: element<HTMLInputElement>('matcher-enabled').checked },
    },
    t('model.saved'),
  )

  const keyField = element<HTMLInputElement>('model-key')
  if (keyField.value && settings) {
    try {
      await api('/secret', {
        method: 'PUT',
        body: JSON.stringify({ name: settings.model.api_key_env, value: keyField.value }),
      })
      keyField.value = ''
      announce(t('model.keySaved'))
    } catch (error) {
      alarm(reason(error))
    }
  }
})

element('test-model').addEventListener('click', async () => {
  const result = element('model-result')
  result.textContent = t('model.testing')

  try {
    const answer = await api<{ ok: boolean; model?: string; answer?: string; error?: string }>(
      '/test/model',
      { method: 'POST' },
    )
    const text = answer.ok
      ? t('model.connected', { model: answer.model ?? '', answer: answer.answer ?? '-' })
      : t('model.failed', { reason: answer.error ?? '' })
    result.textContent = text
    announce(text)
  } catch (error) {
    const text = t('model.failed', { reason: reason(error) })
    result.textContent = text
    alarm(text)
  }
})

element('save-language').addEventListener('click', async () => {
  const wanted = element<HTMLSelectElement>('lang-output').value

  await saveConfig(
    {
      language: { output: wanted, stt: element<HTMLSelectElement>('lang-stt').value },
      wake: {
        enabled: element<HTMLInputElement>('wake-enabled').checked,
        model: element<HTMLSelectElement>('wake-model').value,
        threshold: numberFrom('wake-threshold', settings?.wake.threshold ?? 0.6),
      },
      hotkey: { combination: element<HTMLInputElement>('hotkey').value },
    },
    t('language.saved'),
  )

  // The engine takes both of these up immediately, so whether they actually
  // took is knowable now -- and a hotkey another application already owns is
  // the one failure she cannot see from the field she just typed into.
  await refreshWake()
  await showHotkeyProblem()

  // The window follows the language MikkiLens speaks, menus and tray included.
  await applyLanguage(wanted)
})

// -- language -----------------------------------------------------------------

/** Load a catalogue, retranslate the page, and re-render what it built. */
async function applyLanguage(wanted: string): Promise<void> {
  const loaded = await bridge.loadLocale(wanted)
  setCatalog(loaded.language, loaded.strings)
  applyTranslations()
  await bridge.applyLocale(loaded.language)

  // Anything built from script carries translated text of its own, so it has
  // to be rebuilt rather than just re-labelled.
  renderStatus()
  fillSpokenLanguageChoices()
  fillRecognitionChoices()
  if (wakeStatus) {
    fillWakeWords(wakeStatus)
    explainWake(wakeStatus)
  }
  if (Object.keys(commands).length > 0) {
    renderCommands({
      language: loaded.language,
      path: '',
      order: commandOrder,
      warnings: [],
      commands,
    })
  }
}

/** The "language you speak" list, which is written in the current language. */
function fillSpokenLanguageChoices(): void {
  const select = element<HTMLSelectElement>('lang-stt')
  const chosen = settings?.language.stt ?? select.value

  select.replaceChildren()
  for (const [value, label] of [
    ['id', 'Indonesia'],
    ['en', 'English'],
    ['auto', t('language.auto')],
  ] as const) {
    const option = document.createElement('option')
    option.value = value
    option.textContent = label
    select.append(option)
  }
  select.value = chosen
}

// -- speech recognition -------------------------------------------------------

/**
 * Which model hears her, and what runs it.
 *
 * These two live in the config file and nowhere else until now, which made
 * them unchangeable for the person this application is for. They matter more
 * than anything else on this page: the model decides whether a command is
 * heard correctly, and the device decides whether the answer arrives in a
 * fifth of a second or in three -- and three seconds of silence is long
 * enough that she says it again.
 */

/** The models MikkiLens knows to look for in data/models, smallest first. */
const recognitionModels = ['tiny', 'base', 'small', 'medium', 'large-v3-turbo', 'large-v3']

/** Where recognition can run. "auto" is the one that survives a new machine. */
const recognitionDevices: Array<[string, string]> = [
  ['auto', 'audio.sttAuto'],
  ['cuda', 'audio.sttCard'],
  ['cpu', 'audio.sttProcessor'],
]

function fillRecognitionChoices(): void {
  const sizes = element<HTMLSelectElement>('stt-model-size')
  const chosen = settings?.stt?.model_size ?? 'small'
  const names = [...recognitionModels]
  if (chosen && !names.includes(chosen)) {
    names.push(chosen) // a size set by hand is still hers to keep
  }

  sizes.replaceChildren()
  for (const name of names) {
    const option = document.createElement('option')
    option.value = name
    option.textContent = name
    sizes.append(option)
  }
  sizes.value = chosen

  const devices = element<HTMLSelectElement>('stt-device')
  const device = settings?.stt?.device ?? 'auto'
  devices.replaceChildren()
  for (const [value, labelKey] of recognitionDevices) {
    const option = document.createElement('option')
    option.value = value
    option.textContent = t(labelKey)
    devices.append(option)
  }
  devices.value = recognitionDevices.some(([value]) => value === device) ? device : 'auto'
}

/**
 * What is actually running, in the engine's own words.
 *
 * "whisper.cpp small on the processor" answers the two questions this panel
 * asks -- did the model change take, and is it on the card -- without her
 * having to time a command to find out.
 */
function showRecognitionBackend(): void {
  const backend = snapshot.stt_backend
  element('stt-running').textContent = backend
    ? t('audio.sttRunning', { backend: String(backend) })
    : ''
}

// -- getting her attention ----------------------------------------------------

/**
 * The wake word and the hotkey.
 *
 * Both fail the same way when they fail: nothing happens. A wake word whose
 * model is not installed, a threshold set so high that nothing reaches it, a
 * hotkey another application already owns -- from where she is sitting these
 * are all "it stopped listening to me". So this panel refuses to be a pair of
 * text boxes: the wake word is chosen from what is actually installed, the key
 * is captured by pressing it, and two live bars show what the microphone and
 * the detector are hearing right now.
 */

let wakeStatus: WakeStatus | null = null
let liveTimer: number | undefined
let liveNote = ''

async function refreshWake(): Promise<void> {
  try {
    const status = await api<WakeStatus>('/wake')
    wakeStatus = status
    fillWakeWords(status)
    explainWake(status)
  } catch (error) {
    element('wake-model-note').textContent = reason(error)
  }
}

/**
 * Offer the wake words that are installed, and nothing else.
 *
 * A name that is configured but missing is still listed, marked as missing:
 * opening this page and pressing Save must never quietly swap her wake word
 * for a different one.
 */
function fillWakeWords(status: WakeStatus): void {
  const select = element<HTMLSelectElement>('wake-model')
  const chosen = settings?.wake.model ?? status.model
  const names = [...status.installed]
  if (chosen && !names.includes(chosen)) {
    names.unshift(chosen)
  }

  select.replaceChildren()
  for (const name of names) {
    const option = document.createElement('option')
    option.value = name
    option.textContent = status.installed.includes(name)
      ? name
      : t('language.wakeMissingOption', { model: name })
    select.append(option)
  }
  if (names.length === 0) {
    const option = document.createElement('option')
    option.value = ''
    option.textContent = t('language.wakeNoneInstalled')
    select.append(option)
  }
  select.value = chosen
  select.disabled = names.length === 0
}

/** Say why the wake word is not running, when it is not. */
function explainWake(status: WakeStatus): void {
  const chosen = element<HTMLSelectElement>('wake-model').value
  let note = ''

  if (status.runtime_error) {
    note = t('language.wakeRuntimeMissing', { reason: status.runtime_error })
  } else if (status.installed.length === 0) {
    note = t('language.wakeNoModels')
  } else if (chosen && !status.installed.includes(chosen)) {
    note = t('language.wakeNotInstalled', { model: chosen })
  } else if (status.error) {
    note = t('language.wakeFailed', { reason: status.error })
  }
  element('wake-model-note').textContent = note
}

/** A microphone level, on the decibel scale a meter is readable on. */
function meterFraction(rms: number): number {
  if (rms <= 0) {
    return 0
  }
  const decibels = 20 * Math.log10(rms)
  return Math.max(0, Math.min(1, (decibels + 60) / 60))
}

function showLevel(fill: string, out: string, fraction: number): void {
  const percent = Math.round(Math.max(0, Math.min(1, fraction)) * 100)
  element(fill).style.width = `${percent}%`
  element(out).textContent = `${percent}%`
}

/**
 * One sentence for what the two bars are doing, changed only when it changes.
 *
 * The bars are hidden from a screen reader because a number that moves four
 * times a second is noise, not information. This is the same reading in words,
 * and it only speaks when the situation is genuinely different.
 */
function describeLive(status: WakeStatus, level: number): string {
  if (status.mic_error) {
    return t('language.micFailed', { reason: status.mic_error })
  }
  if (status.mic_running === false) {
    return t('language.micClosed')
  }
  if (level < 0.05) {
    return t('language.micSilent')
  }
  if (!status.enabled) {
    return t('language.wakeSwitchedOff')
  }
  if (status.error || !status.loaded) {
    return t('language.wakeNotRunning')
  }
  if (status.paused) {
    return t('language.wakeBusy')
  }
  if ((status.score ?? 0) >= status.threshold) {
    return t('language.wakeHeardIt')
  }
  return t('language.micHearing')
}

async function pollLive(): Promise<void> {
  let status: WakeStatus
  try {
    status = await api<WakeStatus>('/wake')
  } catch {
    return // the engine banner already says when it cannot be reached
  }
  wakeStatus = status

  const level = meterFraction(status.mic_level ?? 0)
  showLevel('mic-level-fill', 'mic-level-out', level)
  showLevel('wake-score-fill', 'wake-score-out', status.score ?? 0)

  // The mark is where the threshold sits, so "it nearly triggered" is
  // something she can see rather than work out from two numbers.
  const threshold = numberFrom('wake-threshold', status.threshold)
  element('wake-threshold-mark').style.left = `${Math.round(threshold * 100)}%`

  const note = describeLive(status, level)
  if (note !== liveNote) {
    liveNote = note
    element('wake-live-note').textContent = note
  }
}

/**
 * The meters run only while that panel is open.
 *
 * They are a diagnosis tool, not a display: polling the engine four times a
 * second from a window sitting on the Status tab would be work nobody asked
 * for, on the machine that is also encoding video.
 */
function updateLivePolling(): void {
  const wanted = !element('panel-language').hidden && !document.hidden

  if (wanted && liveTimer === undefined) {
    void pollLive()
    liveTimer = window.setInterval(() => void pollLive(), 250)
  } else if (!wanted && liveTimer !== undefined) {
    window.clearInterval(liveTimer)
    liveTimer = undefined
    liveNote = ''
  }
}

document.addEventListener('visibilitychange', updateLivePolling)

element<HTMLSelectElement>('wake-model').addEventListener('change', () => {
  if (wakeStatus) {
    explainWake(wakeStatus)
  }
})

// -- the hotkey, captured by pressing it --------------------------------------

/**
 * The names the engine's key table uses, keyed by the browser's code for the
 * physical key.
 *
 * The code rather than the character on purpose: the engine registers a
 * position with Windows, not a letter, so a keyboard laid out differently
 * still records the key she actually pressed.
 */
const namedKeys: Record<string, string> = {
  Space: 'space',
  Enter: 'enter',
  NumpadEnter: 'enter',
  Tab: 'tab',
  Backspace: 'backspace',
  Delete: 'delete',
  Insert: 'insert',
  Home: 'home',
  End: 'end',
  PageUp: 'page_up',
  PageDown: 'page_down',
  ArrowUp: 'up',
  ArrowDown: 'down',
  ArrowLeft: 'left',
  ArrowRight: 'right',
  CapsLock: 'caps_lock',
  NumLock: 'num_lock',
  ScrollLock: 'scroll_lock',
  PrintScreen: 'print_screen',
  Pause: 'pause',
  ContextMenu: 'menu',
  NumpadAdd: 'num_add',
  NumpadSubtract: 'num_subtract',
  NumpadMultiply: 'num_multiply',
  NumpadDivide: 'num_divide',
  NumpadDecimal: 'num_decimal',
}

const modifierCodes = new Set([
  'ControlLeft',
  'ControlRight',
  'AltLeft',
  'AltRight',
  'ShiftLeft',
  'ShiftRight',
  'MetaLeft',
  'MetaRight',
])

/** The engine's name for one physical key, or null if it has no name there. */
function keyName(code: string): string | null {
  if (/^Key[A-Z]$/.test(code)) {
    return code.slice(3).toLowerCase()
  }
  if (/^Digit[0-9]$/.test(code)) {
    return code.slice(5)
  }
  if (/^F([1-9]|1[0-9]|2[0-4])$/.test(code)) {
    return code.toLowerCase()
  }
  if (/^Numpad[0-9]$/.test(code)) {
    return `num_${code.slice(6)}`
  }
  return namedKeys[code] ?? null
}

/** Letters and digits are written bare; everything else in angle brackets. */
function written(name: string): string {
  return /^[a-z0-9]$/.test(name) ? name : `<${name}>`
}

function modifiersHeld(event: KeyboardEvent): string[] {
  const held: string[] = []
  if (event.ctrlKey) held.push('<ctrl>')
  if (event.altKey) held.push('<alt>')
  if (event.shiftKey) held.push('<shift>')
  if (event.metaKey) held.push('<win>')
  return held
}

const hotkeyField = element<HTMLInputElement>('hotkey')
const hotkeyButton = element<HTMLButtonElement>('hotkey-record')

/** What the field held before capture started; null when not capturing. */
let hotkeyBefore: string | null = null

function hotkeyNote(text: string): void {
  element('hotkey-note').textContent = text
}

function startHotkeyCapture(): void {
  if (hotkeyBefore !== null) {
    cancelHotkeyCapture()
    return
  }

  hotkeyBefore = hotkeyField.value
  hotkeyField.value = ''
  hotkeyField.placeholder = t('language.hotkeyPrompt')
  hotkeyField.classList.add('capturing')
  hotkeyButton.textContent = t('common.cancel')
  hotkeyNote(t('language.hotkeyPrompt'))
  announce(t('language.hotkeyPrompt'))
  hotkeyField.focus()

  // Capture phase, so the keys land here rather than in the page: a
  // combination the window itself would act on has to be recordable too.
  window.addEventListener('keydown', onHotkeyDown, true)
  window.addEventListener('keyup', onHotkeyUp, true)
  hotkeyField.addEventListener('blur', cancelHotkeyCapture)
}

function endHotkeyCapture(): void {
  window.removeEventListener('keydown', onHotkeyDown, true)
  window.removeEventListener('keyup', onHotkeyUp, true)
  hotkeyField.removeEventListener('blur', cancelHotkeyCapture)
  hotkeyField.classList.remove('capturing')
  hotkeyField.placeholder = ''
  hotkeyButton.textContent = t('language.hotkeyRecord')
  hotkeyBefore = null
}

function cancelHotkeyCapture(): void {
  if (hotkeyBefore === null) {
    return
  }
  hotkeyField.value = hotkeyBefore
  endHotkeyCapture()
  hotkeyNote(t('language.hotkeyUnchanged'))
  announce(t('language.hotkeyUnchanged'))
}

function finishHotkeyCapture(combination: string): void {
  hotkeyField.value = combination
  endHotkeyCapture()

  const said = t('language.hotkeyCaptured', { combination })
  hotkeyNote(said)
  announce(said)
}

function onHotkeyDown(event: KeyboardEvent): void {
  event.preventDefault()
  event.stopPropagation()

  if (event.code === 'Escape') {
    cancelHotkeyCapture()
    return
  }
  if (modifierCodes.has(event.code)) {
    // Show what is held, so a combination still on its way down looks like
    // progress rather than a field that has stopped responding.
    hotkeyField.value = modifiersHeld(event).join('+')
    return
  }

  const name = keyName(event.code)
  if (name === null) {
    hotkeyNote(t('language.hotkeyUnusableKey', { key: event.key }))
    return
  }

  const modifiers = modifiersHeld(event)
  // A bare key is registered globally, which would take it away from every
  // other application on the machine. Function keys are the exception: that
  // is what a Stream Deck or a foot pedal usually sends.
  if (modifiers.length === 0 && !/^f\d+$/.test(name)) {
    hotkeyField.value = ''
    hotkeyNote(t('language.hotkeyNeedsModifier'))
    return
  }

  finishHotkeyCapture([...modifiers, written(name)].join('+'))
}

function onHotkeyUp(event: KeyboardEvent): void {
  event.preventDefault()
  if (hotkeyBefore !== null && modifiersHeld(event).length === 0) {
    hotkeyField.value = '' // nothing is held any more; start the guess again
  }
}

hotkeyButton.addEventListener('click', startHotkeyCapture)
hotkeyField.addEventListener('click', () => {
  if (hotkeyBefore === null) {
    startHotkeyCapture()
  }
})

/**
 * Report a hotkey the engine could not take.
 *
 * RegisterHotKey refuses a combination another application already holds, and
 * that refusal is invisible from the field she typed it into: the setting
 * saves, and the key simply does nothing.
 */
async function showHotkeyProblem(): Promise<void> {
  try {
    const state = await api<Snapshot>('/state')
    snapshot = state
    renderStatus()

    const problem = typeof state.hotkey_error === 'string' ? state.hotkey_error : ''
    hotkeyNote(problem ? t('language.hotkeyRefused', { reason: problem }) : '')
    if (problem) {
      alarm(t('language.hotkeyRefused', { reason: problem }))
    }
  } catch {
    // The banner covers an unreachable engine; nothing useful to add here.
  }
}

// -- understanding commands ---------------------------------------------------

// -- youtube ------------------------------------------------------------------

// Two buttons and a sentence. Everything the sentence has to distinguish is
// something she can act on: connected, never connected, or connected being
// impossible because there is no OAuth client on the machine to do it with.
async function refreshYouTube(): Promise<YouTubeStatus> {
  const status = await api<YouTubeStatus>('/youtube/status')

  let text: string
  if (status.connected) {
    text = t('youtube.connectedAs', {
      channel: status.channel ? t('youtube.asChannel', { channel: status.channel }) : '',
      percent: status.quota_percent ?? 0,
      transport: status.chat_transport
        ? t('youtube.viaTransport', { transport: status.chat_transport })
        : '',
    })
  } else if (status.has_client === false) {
    text = t('youtube.noClient')
  } else if (!status.enabled) {
    text = t('youtube.disconnected')
  } else {
    text = t('youtube.notConnected')
  }

  element('youtube-state').textContent = text

  // Offering a button that cannot work is worse than offering none: she cannot
  // see it fail, only hear nothing happen.
  element<HTMLButtonElement>('connect-youtube').disabled =
    status.connected || status.has_client === false
  element<HTMLButtonElement>('disconnect-youtube').disabled = !status.connected
  return status
}

// Connecting waits on a browser page she has to read and agree to, so it can
// take minutes. The button says so and stays disabled meanwhile, because a
// second press would start a second consent flow behind the first.
element('connect-youtube').addEventListener('click', async () => {
  const button = element<HTMLButtonElement>('connect-youtube')
  const result = element('youtube-result')

  button.disabled = true
  result.textContent = t('youtube.connecting')
  announce(t('youtube.connecting'))

  try {
    await api('/youtube/connect', { method: 'POST' })
    result.textContent = t('youtube.connected')
    announce(t('youtube.connected'))
  } catch (error) {
    const text = t('youtube.connectFailed', { reason: reason(error) })
    result.textContent = text
    alarm(text)
  } finally {
    button.disabled = false
    await refreshYouTube()
  }
})

element('disconnect-youtube').addEventListener('click', async () => {
  const result = element('youtube-result')
  try {
    await api('/youtube/disconnect', { method: 'POST' })
    result.textContent = t('youtube.disconnected')
    announce(t('youtube.disconnected'))
  } catch (error) {
    result.textContent = reason(error)
    alarm(reason(error))
  }
  await refreshYouTube()
})


// -- channels -----------------------------------------------------------------

/**
 * One row per channel: what she calls it, which OBS profile streams to it, and
 * a button to switch.
 *
 * The pairing is the part that cannot be done by voice, which is why it is on
 * this page at all. "Call this one music" is a fine thing to say out loud;
 * "the YouTube channel whose id is UC-then-twenty-two-characters belongs to the
 * OBS profile called Music" is not. Made once here by whoever is helping, and
 * after that switching is a sentence she says.
 *
 * The profile is a dropdown of what OBS actually has rather than a text box,
 * because a profile name typed with the wrong capitalisation is a switch that
 * appears to work and silently changes nothing.
 */
let channels: ChannelInfo[] = []
let obsProfiles: OBSProfiles = { connected: false }

async function refreshChannels(): Promise<void> {
  const state = element('channels-state')
  try {
    const [payload, profiles] = await Promise.all([
      api<ChannelsPayload>('/youtube/channels'),
      api<OBSProfiles>('/obs/profiles'),
    ])
    channels = payload.channels ?? []
    obsProfiles = profiles
  } catch (error) {
    state.textContent = t('common.loadFailed', { reason: reason(error) })
    return
  }

  if (channels.length === 0) {
    state.textContent = t('channels.none')
  } else {
    const active = channels.find((channel) => channel.active)
    state.textContent = active
      ? t('channels.onChannel', { channel: channelName(active) })
      : t('channels.countOnly', { count: channels.length })
  }
  if (!obsProfiles.connected) {
    state.textContent += ` ${t('channels.obsOffline')}`
  }

  renderChannels()
}

function channelName(channel: ChannelInfo): string {
  return channel.name || channel.channel_title || channel.obs_profile || channel.channel_id
}

function renderChannels(): void {
  const list = element('channel-list')
  list.replaceChildren()

  channels.forEach((channel, index) => {
    const group = document.createElement('fieldset')
    group.className = 'channel'

    const caption = document.createElement('legend')
    // The legend names the row for a screen reader, and every control inside
    // is labelled with it, so tabbing into the middle of the list still says
    // which channel is being changed.
    caption.textContent = channel.active
      ? t('channels.activeRow', { channel: channelName(channel) })
      : channelName(channel)
    group.append(caption)

    group.append(
      textRow(`channel-name-${index}`, t('channels.name'), channel.name, (value) => {
        channel.name = value
      }),
    )
    group.append(
      selectRow(
        `channel-profile-${index}`,
        t('channels.profile'),
        obsProfiles.profiles ?? [],
        channel.obs_profile,
        (value) => {
          channel.obs_profile = value
        },
      ),
    )
    group.append(
      selectRow(
        `channel-scenes-${index}`,
        t('channels.sceneCollection'),
        obsProfiles.scene_collections ?? [],
        channel.obs_scene_collection,
        (value) => {
          channel.obs_scene_collection = value
        },
      ),
    )

    const status = document.createElement('p')
    status.className = 'hint'
    status.textContent = channel.connected
      ? t('channels.signedIn', { channel: channel.channel_title || channel.channel_id })
      : t('channels.needsSignIn')
    group.append(status)

    const buttons = document.createElement('p')
    buttons.className = 'buttons'

    const switchTo = document.createElement('button')
    switchTo.type = 'button'
    switchTo.textContent = t('channels.switchTo')
    switchTo.setAttribute('aria-label', t('channels.switchToNamed', {
      channel: channelName(channel),
    }))
    // Already there, or no sign-in to switch to: a button that cannot do
    // anything is worse than none, because she cannot see it fail.
    switchTo.disabled = channel.active || !channel.connected
    switchTo.addEventListener('click', () => switchToChannel(channel))
    buttons.append(switchTo)

    group.append(buttons)
    list.append(group)
  })
}

function textRow(
  id: string,
  labelText: string,
  value: string,
  onChange: (value: string) => void,
): HTMLElement {
  const row = document.createElement('p')

  const label = document.createElement('label')
  label.setAttribute('for', id)
  label.textContent = labelText

  const input = document.createElement('input')
  input.type = 'text'
  input.id = id
  input.value = value ?? ''
  input.addEventListener('input', () => onChange(input.value))

  row.append(label, input)
  return row
}

/**
 * A dropdown of what OBS has, with a blank first entry.
 *
 * Blank matters: leaving the scene collection unset is the right answer when
 * both channels share one set of scenes and only the stream key differs, and
 * there has to be a way to say so. A saved value OBS no longer has is kept as
 * its own option rather than silently dropped -- OBS may simply be closed, and
 * quietly unbinding a channel because OBS was not running would be a settings
 * page that loses her work.
 */
function selectRow(
  id: string,
  labelText: string,
  options: string[],
  value: string,
  onChange: (value: string) => void,
): HTMLElement {
  const row = document.createElement('p')

  const label = document.createElement('label')
  label.setAttribute('for', id)
  label.textContent = labelText

  const select = document.createElement('select')
  select.id = id

  const blank = document.createElement('option')
  blank.value = ''
  blank.textContent = t('value.none')
  select.append(blank)

  const all = value && !options.includes(value) ? [...options, value] : options
  for (const name of all) {
    const option = document.createElement('option')
    option.value = name
    option.textContent = name
    select.append(option)
  }
  select.value = value ?? ''
  select.addEventListener('change', () => onChange(select.value))

  row.append(label, select)
  return row
}

async function switchToChannel(channel: ChannelInfo): Promise<void> {
  const result = element('channels-result')
  const name = channelName(channel)

  result.textContent = t('channels.switching', { channel: name })
  announce(t('channels.switching', { channel: name }))

  try {
    // The engine says what happened out loud -- including "you are live, stop
    // the stream first" -- so this waits for it to finish and then re-reads
    // the state rather than claiming a result of its own.
    await api('/youtube/switch', {
      method: 'POST',
      body: JSON.stringify({ channel: channel.channel_id || name }),
    })
  } catch (error) {
    result.textContent = t('channels.switchFailed', { reason: reason(error) })
    alarm(t('channels.switchFailed', { reason: reason(error) }))
  }
  await refreshChannels()
  await refreshYouTube()
}

element('save-channels').addEventListener('click', async () => {
  const result = element('channels-result')
  try {
    await api('/youtube/channels', {
      method: 'PUT',
      body: JSON.stringify({ channels }),
    })
    result.textContent = t('channels.saved')
    announce(t('channels.saved'))
  } catch (error) {
    result.textContent = t('common.saveFailed', { reason: reason(error) })
    alarm(t('common.saveFailed', { reason: reason(error) }))
  }
  await refreshChannels()
})

// Connecting another channel is the same browser consent as the first, and
// takes just as long, so the button behaves the same way: disabled while it is
// waiting, because a second press would start a second consent behind the
// first.
element('connect-channel').addEventListener('click', async () => {
  const button = element<HTMLButtonElement>('connect-channel')
  const result = element('channels-result')

  button.disabled = true
  result.textContent = t('channels.connecting')
  announce(t('channels.connecting'))

  try {
    await api('/youtube/connect-channel', { method: 'POST' })
    result.textContent = t('channels.connected')
    announce(t('channels.connected'))
  } catch (error) {
    const text = t('youtube.connectFailed', { reason: reason(error) })
    result.textContent = text
    alarm(text)
  } finally {
    button.disabled = false
    await refreshChannels()
    await refreshYouTube()
  }
})

// -- log ----------------------------------------------------------------------

async function loadLog(): Promise<void> {
  const log = await api<LogPayload>('/log')

  element('last-heard').textContent = log.last_transcript
    ? t('log.heardAs', {
        text: log.last_transcript,
        command: log.last_command || t('log.notRecognised'),
      })
    : t('log.nothingYet')

  const body = element('log-rows')
  body.replaceChildren()
  for (const entry of log.spoken ?? []) {
    const row = document.createElement('tr')

    const kind = document.createElement('td')
    kind.textContent = entry.priority

    const text = document.createElement('td')
    text.textContent = entry.text + (entry.completed ? '' : t('log.cutOff'))

    row.append(kind, text)
    body.append(row)
  }
}

element('refresh-log').addEventListener('click', () => {
  void loadLog()
})

element('show-engine-log').addEventListener('click', async () => {
  const view = element('engine-log')
  view.textContent = await bridge.readLogTail(300)
  view.hidden = false
  view.focus()
})

// -- boot ---------------------------------------------------------------------

async function boot(): Promise<void> {
  engineURL = await bridge.engineURL()
  settings = await api<AppConfig>('/config')

  await applyLanguage(settings.language.output)

  const devices = await api<DeviceList>('/devices')
  renderDevices(element('output-devices'), devices.output ?? [], 'output', settings.speech.output_device)
  renderDevices(element('input-devices'), devices.input ?? [], 'input', settings.audio.input_device)

  const voices = await api<VoiceInfo[]>(
    `/voices?language=${encodeURIComponent(settings.language.output)}`,
  )
  const voiceSelect = element<HTMLSelectElement>('voice')
  voiceSelect.replaceChildren()

  const available = voices ?? []
  const options =
    available.length > 0 ? available : [{ name: settings.speech.voice, gender: '', locale: '' }]
  for (const voice of options) {
    const option = document.createElement('option')
    option.value = voice.name
    option.textContent = voice.gender ? `${voice.name} (${voice.gender})` : voice.name
    voiceSelect.append(option)
  }
  if (settings.speech.voice) {
    voiceSelect.value = settings.speech.voice
  }

  showPercent('rate', settings.speech.rate)
  showPercent('volume', settings.speech.volume)
  showPercent('chat-rate', settings.speech.chat_rate)
  showPercent('chat-volume', settings.speech.chat_volume)
  showFraction('earcon-volume', settings.speech.earcon_volume)

  fillRecognitionChoices()

  element<HTMLInputElement>('obs-host').value = settings.obs.host
  element<HTMLInputElement>('obs-port').value = String(settings.obs.port)
  element<HTMLInputElement>('obs-password').value = settings.obs.password
  element<HTMLInputElement>('obs-mic').value = settings.obs.mic_source

  element<HTMLInputElement>('tako-enabled').checked = settings.tako?.enabled ?? false
  element<HTMLInputElement>('tako-link').value = settings.tako?.link ?? ''
  element<HTMLInputElement>('tako-read').checked = settings.tako?.read_aloud ?? false
  element<HTMLInputElement>('trakteer-enabled').checked = settings.trakteer?.enabled ?? false
  element<HTMLInputElement>('trakteer-link').value = settings.trakteer?.link ?? ''
  element<HTMLInputElement>('trakteer-read').checked = settings.trakteer?.read_aloud ?? false
  element<HTMLInputElement>('max-gift-recipients').value = String(
    settings.chat?.max_gift_recipients ?? 5,
  )

  element<HTMLInputElement>('model-url').value = settings.model.base_url
  element<HTMLInputElement>('model-name').value = settings.model.model
  element<HTMLInputElement>('matcher-enabled').checked = settings.matcher.enabled

  const languageSelect = element<HTMLSelectElement>('lang-output')
  languageSelect.replaceChildren()
  for (const code of settings._languages ?? ['id', 'en']) {
    const option = document.createElement('option')
    option.value = code
    option.textContent = code === 'id' ? 'Bahasa Indonesia' : code === 'en' ? 'English' : code
    languageSelect.append(option)
  }
  languageSelect.value = settings.language.output
  fillSpokenLanguageChoices()

  element<HTMLInputElement>('wake-enabled').checked = settings.wake.enabled
  showFraction('wake-threshold', settings.wake.threshold)
  element<HTMLInputElement>('hotkey').value = settings.hotkey.combination
  await refreshWake()

  await refreshYouTube()
  await refreshChannels()
  await loadCommands()
  await loadLog()

  snapshot = await api<Snapshot>('/state')
  renderStatus()

  if (!socket) {
    connectSocket()
  }
}

bridge.onEngineStatus((status: EngineStatus) => {
  if (!status.reachable) {
    showEngineBanner(t('status.engineUnreachable', { detail: status.detail ?? '' }))
  }
})

// English is installed before the first request, so a failure to reach the
// engine is still reported in words rather than as an empty window.
void (async () => {
  const initial = await bridge.loadLocale()
  setCatalog(initial.language, initial.strings)
  applyTranslations()

  try {
    await boot()
    showEngineBanner(null)
  } catch (error) {
    showEngineBanner(t('common.loadFailed', { reason: reason(error) }))
    alarm(t('common.loadFailed', { reason: reason(error) }))
  }
})()
