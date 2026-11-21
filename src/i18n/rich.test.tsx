import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { richText } from '@/i18n/rich'

describe('richText', () => {
  it('puts nodes into their slots, in the order the sentence has them', () => {
    const html = renderToStaticMarkup(<p>{richText('use {a} then {b}', { b: <i>B</i>, a: <b>A</b> })}</p>)
    expect(html).toBe('<p>use <b>A</b> then <i>B</i></p>')
  })

  it('leaves an unknown slot as written', () => {
    expect(renderToStaticMarkup(<p>{richText('x {nope}', {})}</p>)).toBe('<p>x {nope}</p>')
  })
})
