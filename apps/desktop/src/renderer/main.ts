import { applyTranslations, setCatalog, t } from './i18n.js'
import type {
  AppConfig,
  CommandsPayload,
  CommandSpec,
  DeviceInfo,
  DeviceList,
  EngineStatus,
  Health,
  LogPayload,
  Snapshot,
  VoiceInfo,
  MatcherStatus,
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
  ['stt_backend', 'label.sttBackend'],
  ['wake_model', 'label.wakeModel'],
  ['hotkey', 'label.hotkey'],
  ['command_count', 'label.commandCount'],
]

function displayValue(key: string, value: unknown): string {
  if (typeof value === 'boolean') {
    return value ? t('value.yes') : t('value.no')
  }
  if (value === null || value === undefined || value === '') {
    return key === 'wake_model' || key === 'hotkey' ? t('value.disabled') : t('value.none')
  }
  return String(value)
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
        rate: element<HTMLInputElement>('rate').value,
        chat_rate: element<HTMLInputElement>('chat-rate').value,
        earcon_volume: numberFrom('earcon-volume', settings?.speech.earcon_volume ?? 0.25),
      },
      audio: { input_device: input ? input.value : '' },
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

element<HTMLInputElement>('earcon-volume').addEventListener('input', (event) => {
  element('earcon-volume-out').textContent = (event.target as HTMLInputElement).value
})

element<HTMLInputElement>('wake-threshold').addEventListener('input', (event) => {
  element('wake-threshold-out').textContent = (event.target as HTMLInputElement).value
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

element('save-vision').addEventListener('click', async () => {
  await saveConfig(
    {
      vision: {
        base_url: element<HTMLInputElement>('vision-url').value,
        model: element<HTMLInputElement>('vision-model').value,
      },
    },
    t('vision.saved'),
  )

  const keyField = element<HTMLInputElement>('vision-key')
  if (keyField.value && settings) {
    try {
      await api('/secret', {
        method: 'PUT',
        body: JSON.stringify({ name: settings.vision.api_key_env, value: keyField.value }),
      })
      keyField.value = ''
      announce(t('vision.keySaved'))
    } catch (error) {
      alarm(reason(error))
    }
  }
})

element('test-vision').addEventListener('click', async () => {
  const result = element('vision-result')
  result.textContent = t('vision.testing')

  try {
    const answer = await api<{ ok: boolean; model?: string; answer?: string; error?: string }>(
      '/test/vision',
      { method: 'POST' },
    )
    const text = answer.ok
      ? t('vision.connected', { model: answer.model ?? '', answer: answer.answer ?? '-' })
      : t('vision.failed', { reason: answer.error ?? '' })
    result.textContent = text
    announce(text)
  } catch (error) {
    const text = t('vision.failed', { reason: reason(error) })
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
        model: element<HTMLInputElement>('wake-model').value,
        threshold: numberFrom('wake-threshold', settings?.wake.threshold ?? 0.6),
      },
      hotkey: { combination: element<HTMLInputElement>('hotkey').value },
    },
    t('language.saved'),
  )

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

// -- understanding commands ---------------------------------------------------

// Polled only while something is downloading. A three gigabyte file takes long
// enough that the page must keep saying so, but polling a finished download
// forever would be waste.
let matcherPoll: number | undefined

function gigabytes(bytes: number): string {
  return (bytes / 1e9).toFixed(2)
}

async function refreshMatcher(): Promise<void> {
  const status = await api<MatcherStatus>('/matcher/status')

  const select = element<HTMLSelectElement>('matcher-model')
  if (select.options.length === 0) {
    for (const model of status.models ?? []) {
      const option = document.createElement('option')
      option.value = model.name
      option.textContent = t('matcher.modelOption', {
        name: model.name,
        size: gigabytes(model.bytes),
        summary: model.summary,
      })
      select.append(option)
    }
  }

  const progress = element<HTMLProgressElement>('matcher-progress')
  const result = element('matcher-result')

  let text: string
  if (status.external_base_url) {
    text = t('matcher.external', { model: status.external_model ?? '' })
  } else if (status.downloading) {
    const done = status.progress?.percent ?? 0
    text = t('matcher.downloading', {
      percent: done,
      speed: ((status.progress?.bytes_per_second ?? 0) / 1e6).toFixed(1),
    })
    progress.hidden = false
    progress.value = done
  } else if (status.ready) {
    text = status.vision_is_local
      ? t('matcher.readyWithVision', { model: status.installed_model })
      : t('matcher.ready', { model: status.installed_model })
    progress.hidden = true
  } else if (status.loading) {
    text = t('matcher.loading')
    progress.hidden = true
  } else if (status.installed_model) {
    text = t('matcher.installedNotRunning', { model: status.installed_model })
    progress.hidden = true
  } else {
    text = t('matcher.notInstalled')
    progress.hidden = true
  }

  element('matcher-state').textContent = text
  if (status.progress?.stage === 'error') {
    result.textContent = t('matcher.failed', { reason: status.progress.detail })
  }

  // Keep polling only while there is something to watch.
  if (status.downloading && matcherPoll === undefined) {
    matcherPoll = window.setInterval(() => void refreshMatcher(), 1000)
  } else if (!status.downloading && matcherPoll !== undefined) {
    window.clearInterval(matcherPoll)
    matcherPoll = undefined
  }
}

element('download-matcher').addEventListener('click', async () => {
  const model = element<HTMLSelectElement>('matcher-model').value
  try {
    await api('/matcher/download', {
      method: 'POST',
      body: JSON.stringify({ model }),
    })
    announce(t('matcher.started'))
  } catch (error) {
    alarm(reason(error))
  }
  await refreshMatcher()
})

element('cancel-matcher').addEventListener('click', async () => {
  await api('/matcher/cancel', { method: 'POST' })
  announce(t('matcher.cancelled'))
  await refreshMatcher()
})

// -- youtube ------------------------------------------------------------------

async function refreshYouTube(): Promise<YouTubeStatus> {
  const status = await api<YouTubeStatus>('/youtube/status')

  let text: string
  if (!status.enabled) {
    text = t('youtube.disabled')
  } else if (status.connected) {
    text = t('youtube.connectedAs', {
      channel: status.channel ? t('youtube.asChannel', { channel: status.channel }) : '',
      percent: status.quota_percent ?? 0,
      transport: status.chat_transport
        ? t('youtube.viaTransport', { transport: status.chat_transport })
        : '',
    })
  } else if (status.access === 'public') {
    // Reading works and writing does not, which is worth saying plainly
    // rather than showing the same "not connected" as having nothing at all.
    text = t('youtube.connectedWithKey', { percent: status.quota_percent ?? 0 })
  } else if (status.has_api_key) {
    text = t('youtube.keyNeedsChannel')
  } else if (!status.has_client_secret) {
    text = t('youtube.noClientSecret')
  } else {
    text = t('youtube.notConnected')
  }

  element('youtube-state').textContent = text
  element<HTMLInputElement>('youtube-channel').value = status.channel_id ?? ''
  element<HTMLInputElement>('youtube-video').value = status.video_id ?? ''
  return status
}

// Saving the key is separate from connecting the account: it is the cheaper
// half of YouTube, and the one most people will actually get working.
element('save-youtube-key').addEventListener('click', async () => {
  const result = element('youtube-key-result')

  await saveConfig(
    {
      youtube: {
        channel_id: element<HTMLInputElement>('youtube-channel').value.trim(),
        video_id: element<HTMLInputElement>('youtube-video').value.trim(),
      },
    },
    t('youtube.keySaved'),
  )

  const keyField = element<HTMLInputElement>('youtube-key')
  if (keyField.value) {
    try {
      await api('/secret', {
        method: 'PUT',
        body: JSON.stringify({
          name: settings?.youtube.api_key_env || 'MIKKILENS_YOUTUBE_KEY',
          value: keyField.value,
        }),
      })
      keyField.value = ''
    } catch (error) {
      result.textContent = reason(error)
      alarm(reason(error))
      return
    }
  }

  result.textContent = t('youtube.keySaved')
  await refreshYouTube()
})

element('connect-youtube').addEventListener('click', async () => {
  element('youtube-result').textContent = t('youtube.opening')
  announce(t('youtube.openingAnnounce'))

  try {
    const result = await api<{ ok: boolean; channel?: string; error?: string }>(
      '/youtube/connect',
      { method: 'POST' },
    )
    const text = result.ok
      ? t('youtube.done', {
          channel: result.channel ? t('youtube.asChannel', { channel: result.channel }) : '',
        })
      : t('obs.failed', { reason: result.error ?? '' })
    element('youtube-result').textContent = text
    announce(text)
  } catch (error) {
    element('youtube-result').textContent = reason(error)
    alarm(reason(error))
  }
  await refreshYouTube()
})

element('disconnect-youtube').addEventListener('click', async () => {
  await api('/youtube/disconnect', { method: 'POST' })
  announce(t('youtube.disconnected'))
  await refreshYouTube()
})

element<HTMLInputElement>('startup').addEventListener('change', async (event) => {
  const enabled = (event.target as HTMLInputElement).checked
  try {
    const result = await api<{ enabled: boolean }>('/startup', {
      method: 'PUT',
      body: JSON.stringify({ enabled }),
    })
    await bridge.loginItem(result.enabled)
    announce(result.enabled ? t('startup.on') : t('startup.off'))
  } catch (error) {
    alarm(reason(error))
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

  element<HTMLInputElement>('rate').value = settings.speech.rate
  element<HTMLInputElement>('chat-rate').value = settings.speech.chat_rate
  element<HTMLInputElement>('earcon-volume').value = String(settings.speech.earcon_volume)
  element('earcon-volume-out').textContent = String(settings.speech.earcon_volume)

  element<HTMLInputElement>('obs-host').value = settings.obs.host
  element<HTMLInputElement>('obs-port').value = String(settings.obs.port)
  element<HTMLInputElement>('obs-password').value = settings.obs.password
  element<HTMLInputElement>('obs-mic').value = settings.obs.mic_source

  element<HTMLInputElement>('vision-url').value = settings.vision.base_url
  element<HTMLInputElement>('vision-model').value = settings.vision.model

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

  element<HTMLInputElement>('wake-model').value = settings.wake.model
  element<HTMLInputElement>('wake-threshold').value = String(settings.wake.threshold)
  element('wake-threshold-out').textContent = String(settings.wake.threshold)
  element<HTMLInputElement>('hotkey').value = settings.hotkey.combination

  element<HTMLInputElement>('startup').checked = (
    await api<{ enabled: boolean }>('/startup')
  ).enabled

  await refreshYouTube()
  await refreshMatcher()
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
