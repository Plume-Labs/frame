import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import * as auth from '@/lib/auth'
import { LoginView } from '@/components/LoginView'

// Pins "no sign-in by password" on the console side, the counterpart of
// TestPasswordLoginIsNotServed in internal/authd/server_test.go. authd does
// not serve POST /auth/login/password; a password form here would be a form
// that always fails, and a client helper for it would invite someone to wire
// one back.
//
// Reopening password sign-in is not a matter of deleting this file: it ships
// only together with a second factor (TOTP) in authd — as does the future
// "set a password" route — and this test is then rewritten to expect both
// fields, not removed.
describe('LoginView', () => {
  const html = renderToStaticMarkup(createElement(LoginView, { onSignedIn: () => {} }))

  it('offers no password field', () => {
    expect(html).not.toMatch(/type="password"/)
    expect(html).not.toMatch(/current-password/)
    expect(html).not.toMatch(/>Password</)
  })

  it('offers passkey sign-in', () => {
    expect(html).toContain('Sign in with a passkey')
  })
})

describe('the auth client', () => {
  it('has no password sign-in helper', () => {
    expect(Object.keys(auth)).not.toContain('loginWithPassword')
  })
})
