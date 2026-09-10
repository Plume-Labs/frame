import { describe, it, expect } from 'vitest'
import {
  BEARER_SUBPROTOCOL_PREFIX,
  CHANNEL_ERROR,
  CHANNEL_RESIZE,
  CHANNEL_STDIN,
  CHANNEL_STDOUT,
  EXEC_SUBPROTOCOL,
  bearerSubprotocol,
  decodeFrame,
  encodeResize,
  encodeStdin,
  execExitMessage,
  execPath,
  execSubprotocols,
  execUrl,
  frameText,
} from './exec-protocol'

const TARGET = {
  namespace: 'neura',
  pod: 'api-7d9f8-x1',
  container: 'api',
  command: ['/bin/sh'],
}

function buf(...bytes: number[]): ArrayBuffer {
  return new Uint8Array(bytes).buffer
}

describe('execPath', () => {
  // Spelled out in full, not matched on a substring: the namespace segment is
  // where this kind of path goes wrong silently, and `.includes('/exec')` is
  // true of the wrong pod in the wrong namespace too.
  it('addresses the pod, the container and the command', () => {
    expect(execPath(TARGET)).toBe(
      '/api/v1/namespaces/neura/pods/api-7d9f8-x1/exec' +
        '?container=api&stdin=true&stdout=true&tty=true&command=%2Fbin%2Fsh',
    )
  })

  // The apiserver refuses `stderr=true` together with `tty=true` — a TTY
  // merges the two streams, so PodExecOptions treats the pair as invalid and
  // answers 400. Ask for both and the terminal never opens for anyone, with
  // the reason buried in an apiserver validation message.
  it('does not ask for stderr, which a TTY forbids', () => {
    expect(execPath(TARGET)).not.toContain('stderr')
  })

  it('carries a multi-word command as repeated parameters', () => {
    const p = execPath({ ...TARGET, command: ['/bin/sh', '-c', 'exec bash'] })
    expect(p).toContain('command=%2Fbin%2Fsh&command=-c&command=exec+bash')
  })
})

describe('execUrl', () => {
  it('turns the page origin into a WebSocket origin', () => {
    expect(execUrl(TARGET, 'https://frame.example.test')).toBe(
      'wss://frame.example.test' + execPath(TARGET),
    )
    expect(execUrl(TARGET, 'http://localhost:4200')).toBe(
      'ws://localhost:4200' + execPath(TARGET),
    )
  })
})

describe('bearerSubprotocol', () => {
  it('encodes the token as unpadded base64url', () => {
    expect(bearerSubprotocol('header.payload.sig')).toBe(
      BEARER_SUBPROTOCOL_PREFIX + 'aGVhZGVyLnBheWxvYWQuc2ln',
    )
  })

  // RFC 6455 subprotocol tokens cannot contain "=", "+" or "/". A padded or
  // standard-alphabet encoding makes `new WebSocket()` throw a SyntaxError in
  // the browser before a byte leaves the tab — no request, no server log,
  // nothing to find.
  it('produces a value a subprotocol token can legally hold', () => {
    // "aa" pads to two "=" under standard base64; "\xfb\xff" hits both the
    // "+" and "/" characters of the standard alphabet.
    for (const token of ['aa', 'a', 'ûÿ', 'x'.repeat(97)]) {
      const p = bearerSubprotocol(token)
      expect(p).not.toContain('=')
      expect(p).not.toContain('+')
      expect(p).not.toContain('/')
    }
  })

  it('offers the real protocol first, so the server can select it', () => {
    const list = execSubprotocols('a.b.c')
    expect(list[0]).toBe(EXEC_SUBPROTOCOL)
    expect(list[1]).toBe(bearerSubprotocol('a.b.c'))
  })
})

describe('decodeFrame', () => {
  // The whole point of the module. 0x01 is the stdout channel marker, not
  // output: leave it in the payload and the terminal prints a control
  // character at the head of every chunk the pod writes.
  it('splits the channel byte off the payload', () => {
    const f = decodeFrame(buf(CHANNEL_STDOUT, 0x68, 0x69))
    expect(f?.channel).toBe(CHANNEL_STDOUT)
    expect(frameText(f!)).toBe('hi')
  })

  it('reads the error channel', () => {
    const f = decodeFrame(buf(CHANNEL_ERROR, 0x7b, 0x7d))
    expect(f?.channel).toBe(CHANNEL_ERROR)
    expect(frameText(f!)).toBe('{}')
  })

  it('reports nothing for an empty frame', () => {
    expect(decodeFrame(new ArrayBuffer(0))).toBeUndefined()
  })

  // A frame carrying only the channel byte is legal and means "no output on
  // this channel". Returning undefined for it would be indistinguishable from
  // a malformed frame.
  it('reads a channel byte with no payload as empty output', () => {
    const f = decodeFrame(buf(CHANNEL_STDOUT))
    expect(f?.channel).toBe(CHANNEL_STDOUT)
    expect(frameText(f!)).toBe('')
  })
})

describe('encodeStdin', () => {
  it('prefixes the stdin channel', () => {
    expect(Array.from(encodeStdin('hi'))).toEqual([CHANNEL_STDIN, 0x68, 0x69])
  })

  it('sends non-ASCII as UTF-8, not as one byte per character', () => {
    expect(Array.from(encodeStdin('é'))).toEqual([CHANNEL_STDIN, 0xc3, 0xa9])
  })
})

describe('encodeResize', () => {
  // The field names are capitalised because the apiserver unmarshals them into
  // remotecommand.TerminalSize, a Go struct with no json tags. Lowercase keys
  // are accepted, ignored, and produce no error anywhere: the pane resizes in
  // the browser, the pty stays at its original size, and `top` renders into
  // the wrong width forever with nothing to look at.
  it('sends Width and Height capitalised, on the resize channel', () => {
    const bytes = encodeResize(120, 40)
    expect(bytes[0]).toBe(CHANNEL_RESIZE)
    expect(new TextDecoder().decode(bytes.subarray(1))).toBe('{"Width":120,"Height":40}')
  })
})

describe('execExitMessage', () => {
  it('says nothing when the command exited cleanly', () => {
    expect(execExitMessage('{"status":"Success"}')).toBeUndefined()
  })

  it('reports the message the apiserver sent when it did not', () => {
    expect(execExitMessage('{"status":"Failure","message":"command terminated with exit code 1"}'))
      .toBe('command terminated with exit code 1')
  })

  // The error channel is the only place a refused exec explains itself. A
  // body that is not JSON must still say something, or a failure arrives as
  // an empty terminal.
  it('falls back to the raw body when it is not a Status', () => {
    expect(execExitMessage('not json')).toBe('not json')
  })
})
