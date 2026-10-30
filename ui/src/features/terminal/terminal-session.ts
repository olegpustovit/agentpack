import { Terminal, type ITheme } from "@xterm/xterm"
import { Unicode11Addon } from "@xterm/addon-unicode11"
import { WebLinksAddon } from "@xterm/addon-web-links"
import { api, LOCAL } from "@/lib/api"
import type { SessionInfo, TerminalState } from "@/lib/api-types"
import { BASE_FONT, fitFont, MIN_FONT, nudgeFont } from "./fit"

declare global {
  interface Window {
    // Only set under WebDriver (Playwright) so tests can read selection and modes.
    __agentpackTerminal?: Terminal
  }
}

export type TerminalSnapshot = {
  status: string
  busy: boolean
  banner: string
  grid: { cols: number; rows: number }
  fontSize: number
  fit: boolean
  mouseOwned: boolean
}

type ControlMessage = {
  type?: string
  attachment?: number
  offset?: number
  error?: string
  state?: TerminalState
}

const encoder = new TextEncoder()
const MONO = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
const CHUNK_BYTES = 32 * 1024
const MAX_BUFFERED_BYTES = 1024 * 1024
const MAX_PENDING_BYTES = 16 * 1024 * 1024
const REPLAY_DEADLINE_MS = 30_000
const RESTART_POLL_MS = 250
const RESTART_DEADLINE_MS = 90_000
const PHONE_WIDTH = 760
const PHONE_FLOOR = 9

function reconnectDelay(retries: number): number {
  return Math.min(10_000, 500 * 2 ** (retries - 1))
}

// Timing marks that the browser performance tooling reads.
function terminalMark(name: string) {
  performance.mark(`agentpack:terminal:${name}`)
  console.debug("agentpack terminal timing", name, Math.round(performance.now()))
}

// data-theme wins; without it the system appearance decides, as the shell's CSS does.
function terminalTheme(systemDark: boolean): ITheme {
  const appearance = document.documentElement.dataset.theme
  const dark = appearance === "dark" || (!appearance && systemDark)
  return dark
    ? { background: "#171b22", foreground: "#e6e8ed", cursor: "#e6e8ed", selectionBackground: "#ffffff40" }
    : {
        background: "#ffffff",
        foreground: "#202124",
        cursor: "#202124",
        selectionBackground: "#0969da40",
        black: "#202124",
        red: "#a31515",
        green: "#116329",
        yellow: "#7d4e00",
        blue: "#0550ae",
        magenta: "#8250df",
        cyan: "#006b75",
        white: "#d0d7de",
        brightBlack: "#57606a",
        brightRed: "#cf222e",
        brightGreen: "#1a7f37",
        brightYellow: "#9a6700",
        brightBlue: "#0969da",
        brightMagenta: "#a475f9",
        brightCyan: "#007c8a",
        brightWhite: "#ffffff",
      }
}

// The worker's replies (DA, DSR, color queries) belong to the responder only. Other viewers swallow them.
function registerReplyHandlers(term: Terminal, canRespond: () => boolean) {
  const consume = () => !canRespond()
  for (const id of [{ final: "c" }, { prefix: ">", final: "c" }, { final: "n" }, { prefix: "?", final: "n" }, { intermediates: "$", final: "p" }, { prefix: "?", intermediates: "$", final: "p" }]) term.parser.registerCsiHandler(id, consume)
  term.parser.registerCsiHandler({ final: "t" }, (params) => typeof params[0] === "number" && [14, 16, 18].includes(params[0]) && consume())
  term.parser.registerOscHandler(4, (data) => data.split(";").some((part, index) => index % 2 === 1 && part === "?") && consume())
  for (const id of [10, 11, 12]) term.parser.registerOscHandler(id, (data) => data.split(";").includes("?") && consume())
  term.parser.registerDcsHandler({ intermediates: "$", final: "q" }, consume)
}

// One terminal for one session: owns the xterm instance, the worker WebSocket, and the detached
// element xterm draws into. The pane starts it on mount and disposes it on unmount.
export class TerminalSession {
  readonly #machine: string
  readonly #sessionId: string
  readonly #element = document.createElement("div")
  readonly #listeners = new Set<() => void>()
  readonly #appearance = matchMedia("(prefers-color-scheme: dark)")
  #listed = true
  #stage: HTMLElement | null = null
  #term: Terminal | null = null
  #socket: WebSocket | null = null
  #observer: ResizeObserver | null = null
  #frame = 0
  #attachment = 0
  #state: TerminalState | null = null
  #replayComplete = true
  #firstFrameMarked = false
  #retries = 0
  // Bumped per connection so stale socket callbacks see they are obsolete.
  #generation = 0
  // Bumped on teardown so pending reconnect and restart timers see the session is gone.
  #restartToken = 0
  #fit = true
  #busy = false
  #status = ""
  #banner = ""
  #snapshot: TerminalSnapshot

  constructor(machine: string, sessionId: string) {
    this.#machine = machine
    this.#sessionId = sessionId
    this.#snapshot = this.#build()
  }

  readonly subscribe = (listener: () => void) => {
    this.#listeners.add(listener)
    return () => {
      this.#listeners.delete(listener)
    }
  }

  readonly getSnapshot = () => this.#snapshot

  setListed(listed: boolean) {
    this.#listed = listed
  }

  start(stage: HTMLElement) {
    this.#teardown()
    this.#stage = stage
    stage.append(this.#element)
    this.#openFresh()
    this.#observer = new ResizeObserver(() => {
      cancelAnimationFrame(this.#frame)
      this.#frame = requestAnimationFrame(() => this.#refit())
    })
    this.#observer.observe(stage)
    this.#term?.focus()
    this.#appearance.addEventListener("change", this.#onAppearance)
  }

  dispose() {
    this.#teardown()
    this.#appearance.removeEventListener("change", this.#onAppearance)
    this.#observer?.disconnect()
    this.#observer = null
    this.#element.remove()
    this.#stage = null
    this.#publish()
  }

  focus() {
    this.#term?.focus()
  }

  copy() {
    const text = this.#term?.getSelection()
    if (text) void navigator.clipboard.writeText(text).catch(() => this.#notice("Copy was blocked by the browser."))
  }

  paste() {
    void navigator.clipboard.readText().then(
      (text) => this.#term?.paste(text),
      () => this.#notice("Paste was blocked by the browser."),
    )
  }

  selectAll() {
    this.#term?.selectAll()
  }

  clearSelection() {
    this.#term?.clearSelection()
  }

  zoom(delta: number) {
    this.#setFont(nudgeFont(this.#term?.options.fontSize ?? BASE_FONT, delta))
  }

  actualSize() {
    this.#setFont(BASE_FONT)
  }

  fitToStage() {
    this.#fit = true
    this.#refit()
    this.#publish()
  }

  readonly #onAppearance = () => {
    if (this.#term) this.#term.options.theme = terminalTheme(this.#appearance.matches)
  }

  #build(): TerminalSnapshot {
    const term = this.#term
    return {
      status: this.#status,
      busy: this.#busy,
      banner: this.#banner,
      grid: { cols: term?.cols ?? 0, rows: term?.rows ?? 0 },
      fontSize: term?.options.fontSize ?? BASE_FONT,
      fit: this.#fit,
      mouseOwned: term ? term.modes.mouseTrackingMode !== "none" : false,
    }
  }

  #publish() {
    this.#snapshot = this.#build()
    for (const listener of this.#listeners) listener()
  }

  #notice(message: string) {
    this.#banner = message
    this.#publish()
  }

  #setFont(size: number) {
    if (!this.#term) return
    this.#fit = false
    this.#term.options.fontSize = size
    this.#publish()
  }

  #refit() {
    const term = this.#term
    const stage = this.#stage
    if (!term || !stage || !this.#fit) return
    const before = term.options.fontSize
    const floor = window.innerWidth <= PHONE_WIDTH ? PHONE_FLOOR : MIN_FONT
    fitFont(term, stage, () => this.#element.querySelector<HTMLElement>(".xterm-screen"), floor)
    if (term.options.fontSize !== before) this.#publish()
  }

  #teardown() {
    this.#restartToken++
    this.#generation++
    this.#retries = 0
    cancelAnimationFrame(this.#frame)
    const socket = this.#socket
    this.#socket = null
    socket?.close()
    this.#term?.dispose()
    this.#term = null
    if (navigator.webdriver) delete window.__agentpackTerminal
    this.#attachment = 0
    this.#state = null
    this.#replayComplete = true
    this.#busy = false
  }

  #openFresh() {
    this.#element.replaceChildren()
    this.#fit = true
    const term = new Terminal({
      cols: 80,
      rows: 24,
      cursorBlink: true,
      fontFamily: MONO,
      fontSize: BASE_FONT,
      scrollback: 5000,
      allowProposedApi: true,
      screenReaderMode: true,
      disableStdin: true,
      macOptionClickForcesSelection: true,
      theme: terminalTheme(this.#appearance.matches),
    })
    term.loadAddon(new Unicode11Addon())
    term.unicode.activeVersion = "11"
    term.loadAddon(new WebLinksAddon())
    term.open(this.#element)
    term.onData((data) => this.#sendInput(data))
    term.onBinary((data) => this.#sendInput(Uint8Array.from(data, (character) => character.charCodeAt(0))))
    term.onWriteParsed(() => {
      const mouseOwned = term.modes.mouseTrackingMode !== "none"
      if (mouseOwned !== this.#snapshot.mouseOwned) this.#publish()
    })
    term.attachCustomKeyEventHandler((event) => this.#key(term, event))
    registerReplyHandlers(term, () => this.#canRespond())
    this.#term = term
    if (navigator.webdriver) window.__agentpackTerminal = term
    void this.#loadWebgl(term)
    this.#connect(false)
    this.#refit()
  }

  // The DOM renderer stays if WebGL is missing, fails to start, or the context is lost.
  async #loadWebgl(term: Terminal) {
    try {
      // Dynamic so the WebGL addon ships as its own chunk; the lazy terminal budget excludes it.
      const { WebglAddon } = await import("@xterm/addon-webgl")
      if (this.#term !== term) return
      const addon = new WebglAddon()
      addon.onContextLoss(() => addon.dispose())
      term.loadAddon(addon)
    } catch {
      // Silent by design: the DOM renderer is the fallback.
    }
  }

  #key(term: Terminal, event: KeyboardEvent): boolean {
    if (event.type !== "keydown") return true
    const key = event.key.toLowerCase()
    if (event.ctrlKey && event.shiftKey && key === "c") {
      event.preventDefault()
      this.copy()
      return false
    }
    if (event.ctrlKey && event.shiftKey && key === "v") {
      event.preventDefault()
      this.paste()
      return false
    }
    // With a selection, Ctrl/⌘+C is a copy: letting the browser copy event reach xterm.
    if ((event.ctrlKey || event.metaKey) && key === "c" && term.hasSelection()) return false
    return true
  }

  #canRespond(): boolean {
    return this.#replayComplete && this.#state?.shared_input === true && this.#state.responder === this.#attachment && !!this.#attachment
  }

  #controls(): boolean {
    return this.#socket?.readyState === WebSocket.OPEN && !!this.#attachment && this.#state?.shared_input === true && !!this.#state?.running && this.#replayComplete
  }

  #sendInput(data: string | Uint8Array<ArrayBuffer>) {
    const socket = this.#socket
    if (!socket || !this.#controls()) return
    if (socket.bufferedAmount > MAX_BUFFERED_BYTES) {
      this.#notice("Connection is busy. Wait before sending more input.")
      return
    }
    const bytes = typeof data === "string" ? encoder.encode(data) : data
    for (let offset = 0; offset < bytes.length; offset += CHUNK_BYTES) socket.send(bytes.subarray(offset, offset + CHUNK_BYTES))
  }

  #updateState(value?: TerminalState) {
    if (value) this.#state = value
    const current = this.#state
    const term = this.#term
    if (value && term && current && current.rows > 0 && current.columns > 0 && (term.rows !== current.rows || term.cols !== current.columns)) {
      term.resize(current.columns, current.rows)
      this.#refit()
    }
    this.#status = !current?.running ? "Stopped" + (current ? ` (${current.exit_code})` : "") : !current.shared_input ? "Read-only" : this.#replayComplete ? "Shared input" : "Replaying output"
    if (term) term.options.disableStdin = !this.#controls()
    if (current?.running && current.shared_input !== true && this.#socket?.readyState === WebSocket.OPEN) this.#banner = "Start a new session to enable shared input."
    this.#publish()
  }

  // Reconnects reset the screen with `ESC c` before the replay: the worker replays from the start.
  #connect(reconnecting: boolean) {
    const term = this.#term
    if (!term) return
    const previous = this.#socket
    this.#socket = null
    previous?.close()
    const generation = ++this.#generation
    this.#attachment = 0
    this.#replayComplete = false
    this.#firstFrameMarked = false
    this.#busy = true
    this.#status = reconnecting ? "Replaying output" : "Connecting…"
    this.#banner = ""

    const url = new URL(`/api/sessions/${encodeURIComponent(this.#sessionId)}/terminal`, location.href)
    if (this.#machine !== LOCAL) url.searchParams.set("node", this.#machine)
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:"
    const socket = new WebSocket(url)
    this.#socket = socket
    socket.binaryType = "arraybuffer"
    // A socket can open without the worker ever completing its attachment.
    const replayDeadline = setTimeout(() => {
      if (this.#socket === socket && !this.#replayComplete) socket.close()
    }, REPLAY_DEADLINE_MS)
    let pendingOutput = 0
    let receivedFrame = false
    let replayReceived = false
    const finishReplay = () => {
      if (this.#socket !== socket || generation !== this.#generation || !replayReceived || pendingOutput || (this.#state?.shared_input !== true && !receivedFrame) || this.#replayComplete) return
      this.#replayComplete = true
      clearTimeout(replayDeadline)
      this.#busy = false
      this.#updateState()
      terminalMark("replay_complete")
    }

    socket.onopen = () => {
      if (generation === this.#generation && this.#socket === socket) terminalMark("socketopen")
    }
    socket.onmessage = (event: MessageEvent) => {
      if (this.#socket !== socket || generation !== this.#generation) return
      if (event.data instanceof ArrayBuffer) {
        const firstFrame = !receivedFrame
        receivedFrame = true
        if (!this.#firstFrameMarked) {
          this.#firstFrameMarked = true
          terminalMark("firstframe")
        }
        pendingOutput += event.data.byteLength
        if (pendingOutput > MAX_PENDING_BYTES) {
          socket.close()
          this.#notice("Output exceeded this browser’s buffer. Reconnect to catch up.")
          return
        }
        const length = event.data.byteLength
        let frame = new Uint8Array(event.data)
        const written = () => {
          pendingOutput -= length
          finishReplay()
        }
        if (reconnecting && firstFrame) {
          const reset = encoder.encode("\x1bc")
          const ordered = new Uint8Array(reset.length + frame.length)
          ordered.set(reset)
          ordered.set(frame, reset.length)
          frame = ordered
        }
        term.write(frame, written)
        return
      }
      try {
        const message = JSON.parse(event.data) as ControlMessage
        if (message.attachment) this.#attachment = message.attachment
        if (message.type === "attached") terminalMark("attached")
        if (message.type === "attached" && message.state?.shared_input !== true) replayReceived = true
        if (message.type === "replay_complete") {
          replayReceived = true
          finishReplay()
        }
        if (message.type === "attached" && this.#retries) {
          this.#retries = 0
          this.#banner = ""
        }
        const previouslyWritable = this.#controls()
        if (message.type === "attached" && message.offset) this.#banner = "Earlier output was truncated. Showing the available terminal history."
        if (message.error) this.#banner = message.error
        if (message.type === "ended" && message.state?.restarting) void this.#reconnectAfterRestart()
        if (message.state) this.#updateState(message.state)
        if (!previouslyWritable && this.#controls()) this.#refit()
        this.#publish()
      } catch {
        this.#notice("The terminal sent an invalid control message.")
        socket.close()
      }
    }
    socket.onclose = () => {
      clearTimeout(replayDeadline)
      if (this.#socket !== socket) return
      this.#attachment = 0
      this.#updateState()
      const state = this.#state
      if (state?.running === false) this.#busy = false
      if (state?.restarting) {
        this.#status = "Restarting · waiting to reconnect"
      } else if (state?.running !== false && this.#listed) {
        // Networks drop (gateway restarts, tailnet hiccups); the worker replays output, so retry quietly.
        this.#retries++
        const delay = reconnectDelay(this.#retries)
        const token = this.#restartToken
        this.#status = `Connection lost · reconnecting in ${Math.ceil(delay / 1000)}s`
        this.#busy = true
        this.#replayComplete = false
        this.#publish()
        setTimeout(() => {
          if (this.#restartToken === token) this.#connect(true)
        }, delay)
      } else if (state?.running !== false) {
        this.#busy = false
        this.#status = "Disconnected · this session is no longer listed"
      }
      this.#publish()
    }
    socket.onerror = () => {
      if (this.#socket === socket && this.#retries >= 2) this.#notice("Still can’t reach this terminal. Retrying automatically…")
    }
    this.#publish()
  }

  // Polls until the agent's new process is listed, then opens a fresh terminal for it.
  async #reconnectAfterRestart() {
    const token = this.#restartToken
    const deadline = Date.now() + RESTART_DEADLINE_MS
    this.#notice("Agent restarting. Waiting to reconnect…")
    while (Date.now() < deadline) {
      await new Promise<void>((resolve) => setTimeout(resolve, RESTART_POLL_MS))
      if (this.#restartToken !== token) return
      try {
        const sessions = await api<SessionInfo[]>("/api/sessions", { machine: this.#machine })
        if (this.#restartToken !== token) return
        const replacement = sessions.find((session) => session.id === this.#sessionId)
        if (replacement?.state && !replacement.state.restarting) {
          this.#teardown()
          this.#openFresh()
          this.#notice("Agent restarted. This is a new process; conversation resume depends on the agent’s launch options.")
          return
        }
      } catch {
        // A restarting worker may briefly be unavailable before its endpoint returns.
      }
    }
    if (this.#restartToken === token) this.#notice("The agent has not become available. Reconnect when its restart completes.")
  }
}
