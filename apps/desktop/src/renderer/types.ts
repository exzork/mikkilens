/** The shapes the engine's API sends back. */

export interface EngineStatus {
  reachable: boolean
  owned?: boolean
  url: string
  detail?: string
}

/** The bridge the preload exposes. Nothing else reaches out of the page. */
export interface MikkiLensBridge {
  engineURL(): Promise<string>
  engineStatus(): Promise<{ reachable: boolean; url: string }>
  restartEngine(): Promise<{ reachable: boolean; detail: string }>
  loadLocale(language?: string): Promise<{ language: string; strings: Record<string, string> }>
  availableLocales(): Promise<string[]>
  applyLocale(language: string): Promise<string>
  openExternal(url: string): Promise<boolean>
  readLogTail(lines: number): Promise<string>
  loginItem(enabled?: boolean): Promise<boolean>
  version(): Promise<string>
  checkForUpdate(): Promise<UpdateCheck>
  onEngineStatus(listener: (status: EngineStatus) => void): void
  closeMusicBox(): Promise<boolean>
  onMusicBoxReopened(listener: () => void): void
}

/** What a check she asked for turned up. */
export interface UpdateCheck {
  outcome: 'current' | 'downloading' | 'ready' | 'failed' | 'unsupported'
  version: string
  detail?: string
}

declare global {
  interface Window {
    mikkilens: MikkiLensBridge
  }
}

export type Health = 'unknown' | 'connected' | 'disconnected' | 'error'

export interface Snapshot {
  obs?: Health
  youtube?: Health
  chat?: Health
  vision?: Health
  streaming?: boolean
  current_scene?: string
  scenes?: string[]
  mic_muted?: boolean
  broadcast_title?: string
  viewer_count?: number
  chat_reading?: string
  chat_backlog?: number
  /** The mute key, which is not the reader: muted chat is still being collected. */
  chat_muted?: boolean
  /** The song coming out of the speakers, empty for none. */
  now_playing?: string
  listening?: boolean
  last_transcript?: string
  last_command?: string
  stt_backend?: string
  /** The first-run download, absent once there is nothing left to fetch. */
  installing?: {
    stage: 'engine' | 'model' | 'wake' | 'gpu'
    downloaded: number
    total: number
    percent: number
    bytes_per_second: number
    done: boolean
    failed?: string
  } | null
  stt_loaded?: boolean
  wake_model?: string | null
  wake_score?: number
  wake_error?: string
  hotkey?: string | null
  hotkey_error?: string
  command_count?: number
  unhandled?: string[]
  mic_frames?: number
  mic_error?: string
  [key: string]: unknown
}

/**
 * Everything the Language panel needs to make a wake word work: what is
 * installed, what is running, and what the microphone and the detector are
 * hearing at this moment.
 */
export interface WakeStatus {
  enabled: boolean
  model: string
  threshold: number
  cooldown_s: number
  installed: string[]
  /** Why the wake word is not running, when it is not. */
  error?: string
  /** The ONNX runtime is missing, so no wake word can run at all. */
  runtime_error?: string
  loaded?: boolean
  running_model?: string
  /** True while a command is being recorded, when it deliberately stops. */
  paused?: boolean
  score?: number
  mic_level?: number
  mic_frames?: number
  mic_error?: string
  mic_running?: boolean
}

export interface DeviceInfo {
  index: number
  name: string
  label: string
  host_api: string
  is_default: boolean
  kind: string
}

export interface DeviceList {
  output: DeviceInfo[]
  input: DeviceInfo[]
  host_api?: string
  error?: string
}

export interface VoiceInfo {
  name: string
  gender: string
  locale: string
}

export interface CommandSpec {
  phrases: string[]
  confirm: boolean
  confirm_prompt: string
  handled: boolean
}

export interface CommandsPayload {
  language: string
  path: string
  order: string[]
  warnings: string[]
  commands: Record<string, CommandSpec>
}

export interface SpokenEntry {
  text: string
  priority: string
  completed: boolean
  at: number
}

export interface LogPayload {
  spoken: SpokenEntry[]
  last_transcript: string
  last_command: string
  command_warnings: string[]
}

export interface AppConfig {
  language: { output: string; stt: string; chat_tts: string }
  speech: {
    voice: string
    rate: string
    volume: string
    chat_voice: string
    chat_rate: string
    chat_volume: string
    donation_voice: string
    donation_rate: string
    donation_volume: string
    output_device: string
    earcon_volume: number
    confirm_timeout_s: number
  }
  audio: { input_device: string; [key: string]: unknown }
  stt: { backend: string; model_size: string; device: string; [key: string]: unknown }
  wake: { enabled: boolean; model: string; threshold: number; cooldown_s: number }
  hotkey: { enabled: boolean; combination: string; push_to_talk: boolean }
  /** The key that silences the chat being read aloud, and gives it back. */
  mute: { enabled: boolean; combination: string }
  /** The key that opens the box she types a song name into. */
  music: { enabled: boolean; combination: string; [key: string]: unknown }
  obs: { host: string; port: number; password: string; mic_source: string; [key: string]: unknown }
  /** The one OpenAI-compatible endpoint, used for text and for images alike. */
  model: { base_url: string; model: string; api_key_env: string; [key: string]: unknown }
  vision: { max_edge: number; monitors: string; [key: string]: unknown }
  matcher: { enabled: boolean }
  youtube: { enabled: boolean; [key: string]: unknown }
  chat: { max_gift_recipients: number; [key: string]: unknown }
  /** Donations, watched so chat is not read over the top of an alert. */
  tako: { enabled: boolean; link: string; read_aloud: boolean; [key: string]: unknown }
  trakteer: { enabled: boolean; link: string; read_aloud: boolean; [key: string]: unknown }
  _languages?: string[]
  [key: string]: unknown
}

export interface YouTubeStatus {
  /** Whether YouTube is switched on at all; Disconnect switches it off. */
  enabled: boolean
  connected: boolean
  /** "none" | "account" -- whether the sign-in is there. */
  access?: string
  /**
   * Whether there is an OAuth client to sign in with (data/client_secret.json).
   * Without one, Connect cannot work and the page says why instead.
   */
  has_client?: boolean
  channel?: string
  quota_used?: number
  quota_percent?: number
  chat_transport?: string
}

/**
 * One of her channels: an OBS profile and a YouTube sign-in, paired.
 *
 * The pairing is the whole point. `obs_profile` names the OBS profile holding
 * this channel's stream key, and `channel_id` names the sign-in that reads its
 * chat -- switching moves both, so a music review cannot go out on the main
 * channel with the main channel's chat read over it.
 */
export interface ChannelInfo {
  /** What she calls it, and what she says to switch to it. */
  name: string
  /** YouTube's own id. Filled in by connecting; never typed. */
  channel_id: string
  obs_profile: string
  obs_scene_collection: string
  /** The channel's title on YouTube, when a sign-in is stored for it. */
  channel_title?: string
  /** Whether a sign-in for it is actually on disk. */
  connected: boolean
  /** Whether it is the channel MikkiLens is on right now. */
  active: boolean
}

export interface ChannelsPayload {
  channels?: ChannelInfo[]
}

/** What OBS has to offer, so a profile can be picked rather than typed. */
export interface OBSProfiles {
  connected: boolean
  profiles?: string[]
  scene_collections?: string[]
  current_profile?: string
  current_scene_collection?: string
  error?: string
}
