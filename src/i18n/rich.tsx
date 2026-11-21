import { Fragment, type ReactNode } from 'react'

/**
 * Fills `{slot}` placeholders in a translated sentence with React nodes, so a
 * sentence can carry a monospace identifier or a link without being cut into
 * fragments that translators cannot reorder. Unknown slots stay as written.
 */
export function richText(text: string, slots: Record<string, ReactNode>): ReactNode[] {
  return text.split(/(\{\w+\})/).map((part, i) => {
    const m = /^\{(\w+)\}$/.exec(part)
    return <Fragment key={i}>{m && m[1] in slots ? slots[m[1]] : part}</Fragment>
  })
}
