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
  onEngineStatus(listener: (status: EngineStatus) => void): void
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
  listening?: boolean
  last_transcript?: string
  last_command?: string
  stt_backend?: string
  stt_loaded?: boolean
  wake_model?: string | null
  wake_score?: number
  hotkey?: string | null
  command_count?: number
  unhandled?: string[]
  mic_frames?: number
  mic_error?: string
  [key: string]: unknown
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
    output_device: string
    earcon_volume: number
    confirm_timeout_s: number
  }
  audio: { input_device: string; [key: string]: unknown }
  wake: { enabled: boolean; model: string; threshold: number; cooldown_s: number }
  hotkey: { enabled: boolean; combination: string; push_to_talk: boolean }
  obs: { host: string; port: number; password: string; mic_source: string; [key: string]: unknown }
  vision: { base_url: string; model: string; api_key_env: string; [key: string]: unknown }
  youtube: {
    api_key_env: string
    channel_id: string
    video_id: string
    [key: string]: unknown
  }
  _languages?: string[]
  [key: string]: unknown
}

export interface MatcherModel {
  name: string
  file: string
  bytes: number
  vision: boolean
  summary: string
}

export interface MatcherProgress {
  stage: string
  downloaded: number
  total: number
  percent: number
  detail: string
  bytes_per_second: number
}

export interface MatcherStatus {
  enabled: boolean
  models: MatcherModel[]
  installed_model: string
  runtime_installed: boolean
  loading: boolean
  ready: boolean
  vision: boolean
  vision_is_local: boolean
  downloading: boolean
  progress: MatcherProgress
  external_base_url?: string
  external_model?: string
}

export interface YouTubeStatus {
  enabled: boolean
  connected: boolean
  /** "none" | "public" | "account" -- how much MikkiLens can currently do. */
  access?: string
  has_api_key?: boolean
  channel_id?: string
  video_id?: string
  api_key_env?: string
  has_client_secret?: boolean
  /** "file" | "none" -- whether data/client_secret.json holds an OAuth client. */
  client_source?: string
  channel?: string
  quota_used?: number
  quota_percent?: number
  chat_transport?: string
}
