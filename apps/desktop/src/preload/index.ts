import { contextBridge, ipcRenderer } from 'electron'

/**
 * The whole surface the page is allowed to reach.
 *
 * Everything else -- the filesystem, the engine process, the shell -- stays in
 * the main process. The page talks to the engine over its local HTTP API like
 * any other client would, so there is one API to understand and one place
 * where a mistake could matter.
 */
const api = {
  /** Where the engine's HTTP API is listening. */
  engineURL: (): Promise<string> => ipcRenderer.invoke('engine:url'),

  /** Whether the engine is answering right now. */
  engineStatus: (): Promise<{ reachable: boolean; url: string }> =>
    ipcRenderer.invoke('engine:status'),

  /** Stop and start the engine, for when something has wedged. */
  restartEngine: (): Promise<{ reachable: boolean; detail: string }> =>
    ipcRenderer.invoke('engine:restart'),

  /** The window's own strings, for one language, with English behind them. */
  loadLocale: (language?: string): Promise<{ language: string; strings: Record<string, string> }> =>
    ipcRenderer.invoke('locale:load', language),

  /** Which languages the window can be shown in. */
  availableLocales: (): Promise<string[]> => ipcRenderer.invoke('locale:available'),

  /** Switch the whole app, menus and tray included, to one language. */
  applyLocale: (language: string): Promise<string> =>
    ipcRenderer.invoke('locale:apply', language),

  /** Open a web address in her real browser. */
  openExternal: (url: string): Promise<boolean> => ipcRenderer.invoke('open-external', url),

  /** The tail of the engine log, for the diagnosis page. */
  readLogTail: (lines: number): Promise<string> => ipcRenderer.invoke('read-log-tail', lines),

  /** Read or set whether MikkiLens opens when she signs in. */
  loginItem: (enabled?: boolean): Promise<boolean> => ipcRenderer.invoke('login-item', enabled),

  /** The version of the app itself, for the header. */
  version: (): Promise<string> => ipcRenderer.invoke('app:version'),

  /** Look for a new version now, because she asked. */
  checkForUpdate: (): Promise<CheckResult> => ipcRenderer.invoke('update:check'),

  /** Called once at startup with how the engine came up. */
  onEngineStatus: (listener: (status: EngineStatus) => void): void => {
    ipcRenderer.on('engine-status', (_event, status: EngineStatus) => listener(status))
  },
}

export interface CheckResult {
  outcome: 'current' | 'downloading' | 'ready' | 'failed' | 'unsupported'
  version: string
  detail?: string
}

export interface EngineStatus {
  reachable: boolean
  owned: boolean
  url: string
  detail: string
}

export type MikkiLensAPI = typeof api

contextBridge.exposeInMainWorld('mikkilens', api)
