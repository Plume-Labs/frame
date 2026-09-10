import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

import { Button } from '@/components/ui/button'
import { ensureToken } from '@/lib/auth'
import {
  CHANNEL_ERROR,
  CHANNEL_STDERR,
  CHANNEL_STDOUT,
  decodeFrame,
  encodeResize,
  encodeStdin,
  execExitMessage,
  execSubprotocols,
  execUrl,
  frameText,
} from '@/lib/exec-protocol'
import type { WorkloadPod } from '@/lib/workloads'

/**
 * `sh` is the only shell every image on this cluster has. `-c 'exec bash …'`
 * upgrades where bash exists and falls back where it does not, in one command,
 * so nobody has to guess which image they are opening.
 */
const SHELL = ['/bin/sh', '-c', 'exec bash -l 2>/dev/null || exec sh -l']

/**
 * An interactive shell in a container.
 *
 * Rendered with xterm rather than by hand: colours, cursor addressing, resize
 * and anything full-screen (`top`, `vim`, a pager) are a terminal emulator's
 * problem, and it is not a problem worth solving twice.
 *
 * The connection is a WebSocket to the apiserver's `pods/exec`, through
 * `frame-uiproxy` like everything else. The token rides a subprotocol because
 * `new WebSocket()` accepts no headers (see `@/lib/exec-protocol`), and the
 * proxy opens a session-shaped FrameTask when the socket connects and closes it
 * when the socket does — so the record carries the duration.
 *
 * Nothing here is tested. It is `.tsx`, which vitest does not execute, and a
 * real socket against a real apiserver is not something a unit test reaches.
 * Everything that could be silently wrong lives in `@/lib/exec-protocol`,
 * which is.
 */
export function TerminalTab({ pod, admin }: { pod: WorkloadPod; admin: boolean }) {
  const host = useRef<HTMLDivElement | null>(null)
  const [container, setContainer] = useState(pod.containers[0] ?? '')
  const [open, setOpen] = useState(false)
  const [error, setError] = useState<string | undefined>()

  useEffect(() => {
    if (!open || !host.current || !container) return

    const term = new Terminal({
      convertEol: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 12,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host.current)
    fit.fit()

    let socket: WebSocket | undefined
    let observer: ResizeObserver | undefined
    let disposed = false

    void (async () => {
      const token = await ensureToken()
      if (disposed) return
      if (!token) {
        setError('No session — sign in again.')
        return
      }
      const ws = new WebSocket(
        execUrl(
          { namespace: pod.namespace, pod: pod.name, container, command: SHELL },
          globalThis.location.origin,
        ),
        execSubprotocols(token),
      )
      ws.binaryType = 'arraybuffer'
      socket = ws

      ws.onopen = () => {
        term.focus()
        ws.send(encodeResize(term.cols, term.rows))
      }
      ws.onmessage = (ev) => {
        const frame = decodeFrame(ev.data as ArrayBuffer)
        if (!frame) return
        if (frame.channel === CHANNEL_STDOUT || frame.channel === CHANNEL_STDERR) {
          term.write(frame.payload)
          return
        }
        if (frame.channel === CHANNEL_ERROR) {
          const message = execExitMessage(frameText(frame))
          if (message) term.write(`\r\n\x1b[31m${message}\x1b[0m\r\n`)
        }
      }
      ws.onerror = () => setError('The shell connection failed. Admin rights are required to open one.')
      ws.onclose = () => {
        term.write('\r\n\x1b[90m— session closed —\x1b[0m\r\n')
      }

      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(encodeStdin(data))
      })

      // Both halves of a resize, and both are needed: fit() reflows the
      // browser's view, and the resize frame is what tells the pty. Send only
      // the first and every full-screen program renders into the old width for
      // the life of the session, with nothing to look at.
      observer = new ResizeObserver(() => {
        fit.fit()
        if (ws.readyState === WebSocket.OPEN) ws.send(encodeResize(term.cols, term.rows))
      })
      if (host.current) observer.observe(host.current)
    })()

    return () => {
      disposed = true
      observer?.disconnect()
      socket?.close()
      term.dispose()
    }
  }, [open, container, pod.namespace, pod.name])

  if (!admin) {
    return (
      <p className="font-mono text-xs text-muted-foreground">
        Opening a shell requires an admin account. A shell carries every permission the container
        itself holds, so it is the one action in this screen that is not delegated further down.
      </p>
    )
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <select
          value={container}
          onChange={(e) => setContainer(e.target.value)}
          disabled={open}
          className="border rounded-md bg-background px-2 py-1 font-mono text-xs"
        >
          {pod.containers.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </select>
        <Button
          size="sm"
          variant={open ? 'outline' : 'default'}
          className="font-mono"
          onClick={() => {
            setError(undefined)
            setOpen((v) => !v)
          }}
        >
          {open ? 'Close session' : 'Open shell'}
        </Button>
      </div>

      {/* Said where the person opening the session can read it, before they
          open it — not in a document nobody opens. */}
      <p className="font-mono text-[10px] text-muted-foreground">
        This session is recorded: who opened it, which pod and container, when, and for how long.
        What is typed and what is displayed are <strong>not</strong> recorded — keystroke capture
        was considered and rejected, because it would put every secret typed or printed here into
        the audit trail.
      </p>

      {error && <p className="font-mono text-xs text-destructive">{error}</p>}

      <div ref={host} className="h-96 border rounded-md bg-black p-2" />
    </div>
  )
}
