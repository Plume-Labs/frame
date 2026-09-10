import { describe, it, expect } from 'vitest'
import { podLogPath, pumpLogLines, type ByteReader } from './pod-logs'

/** A reader that hands back the given chunks, one per read(), then ends. */
function readerOf(chunks: Uint8Array[]): ByteReader {
  let i = 0
  return {
    async read() {
      if (i >= chunks.length) return { done: true, value: undefined }
      return { done: false, value: chunks[i++] }
    },
  }
}

const enc = (s: string) => new TextEncoder().encode(s)

describe('podLogPath', () => {
  // The full path, namespace segment included. `.includes('/log')` is true of
  // the wrong pod in the wrong namespace, which is the failure this repo has
  // already shipped once.
  it('addresses one container of one pod', () => {
    expect(podLogPath({ namespace: 'neura', pod: 'api-0', container: 'api', tailLines: 500 })).toBe(
      '/api/v1/namespaces/neura/pods/api-0/log?container=api&tailLines=500',
    )
  })

  it('asks for a follow when told to', () => {
    expect(podLogPath({ namespace: 'neura', pod: 'api-0', container: 'api', follow: true }))
      .toBe('/api/v1/namespaces/neura/pods/api-0/log?container=api&follow=true')
  })

  // The tab that matters most: when a container has crash-looped, the cause is
  // in the instance that died, not in the one running now. Drop this parameter
  // and the screen answers a different question from the one asked, with no
  // sign that it did.
  it('reads the previous instance when asked', () => {
    expect(podLogPath({ namespace: 'neura', pod: 'api-0', container: 'api', previous: true }))
      .toBe('/api/v1/namespaces/neura/pods/api-0/log?container=api&previous=true')
  })

  // follow and previous together are a contradiction — the dead instance
  // writes nothing more — and the apiserver answers 400. Asking for both would
  // make "previous" appear broken for anyone who left follow on.
  it('never asks to follow a dead instance', () => {
    const p = podLogPath({
      namespace: 'neura', pod: 'api-0', container: 'api', follow: true, previous: true,
    })
    expect(p).toContain('previous=true')
    expect(p).not.toContain('follow=true')
  })
})

describe('pumpLogLines', () => {
  // The reason this is a function and not three lines in a component. Chunk
  // boundaries fall wherever the network puts them: emit one line per chunk
  // and "hello" arrives as "hel" and "lo".
  it('joins a line split across two chunks', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([enc('hel'), enc('lo\nworld\n')]), (l) => lines.push(l))
    expect(lines).toEqual(['hello', 'world'])
  })

  it('splits several lines out of one chunk', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([enc('a\nb\nc\n')]), (l) => lines.push(l))
    expect(lines).toEqual(['a', 'b', 'c'])
  })

  // A pod that is still writing has no trailing newline on its last line. Drop
  // the flush and the most recent line — the one being waited for — never
  // appears.
  it('emits the trailing partial line when the stream ends', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([enc('done\nhalf')]), (l) => lines.push(l))
    expect(lines).toEqual(['done', 'half'])
  })

  // A multi-byte character can be split across chunks. Decode each chunk
  // independently and it becomes two replacement characters, permanently, in
  // the middle of the log line.
  it('reassembles a UTF-8 character split across chunks', async () => {
    const bytes = enc('café\n')
    const lines: string[] = []
    await pumpLogLines(
      readerOf([bytes.subarray(0, 4), bytes.subarray(4)]),
      (l) => lines.push(l),
    )
    expect(lines).toEqual(['café'])
  })

  it('emits nothing for an empty stream', async () => {
    const lines: string[] = []
    await pumpLogLines(readerOf([]), (l) => lines.push(l))
    expect(lines).toEqual([])
  })
})
