import { FormEvent, useCallback, useEffect, useState } from 'react'
import {
  inviteAccount,
  listCredentials,
  revokeCredential,
  type CredentialSummary,
  type InvitableRole,
} from '@/lib/accounts'
import { frameListPath } from '@/lib/frame-sdk'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Trash, UserPlus } from '@phosphor-icons/react'

/**
 * Who can sign in, with what rights, and on which keys.
 *
 * Two different backends, and the split is not arbitrary. Roles and
 * `spec.state` are written straight to the apiserver through
 * `frame-uiproxy`, so they travel under the signed-in admin's own
 * impersonated identity — which is exactly what the FrameUser admission
 * webhook asks for when it refuses a role or state change from a non-admin,
 * and the only reason promoting someone to admin works at all. Invitations and
 * key revocation go to authd, which owns credentials and is the only writer of
 * `status.credentials`.
 *
 * The screen is reachable only for an admin (App gates the nav entry on the
 * token's groups claim). That gate is a courtesy: every write below is
 * refused server-side for anyone else.
 */
interface Account {
  name: string
  email: string
  role: string
  state: string
  keyCount: number
}

const FRAMEUSERS = frameListPath('frameusers')

/**
 * The accounts, with how many keys each holds.
 *
 * The count comes from authd rather than from `status.credentials` on the
 * FrameUser, even though the apiserver would return it in the same list this
 * function already makes. That is deliberate: `GET /auth/credentials` is the
 * one shape the console is allowed to see of a credential record — no
 * public-key material — and reading the raw status here would put material on
 * screen that the dedicated route exists to withhold. One request per account
 * is affordable: these are humans, not nodes.
 */
async function loadAccounts(): Promise<Account[]> {
  const res = await fetch(FRAMEUSERS, {
    headers: { Authorization: `Bearer ${(globalThis as Record<string, unknown>).__FRAME_TOKEN__ ?? ''}` },
  })
  if (!res.ok) throw new Error(`could not list accounts: ${res.status} ${await res.text()}`)
  const body = (await res.json()) as {
    items: Array<{ metadata: { name: string }; spec: { email: string; role: string; state?: string } }>
  }
  return Promise.all(
    body.items.map(async (i) => ({
      name: i.metadata.name,
      email: i.spec.email,
      role: i.spec.role,
      // An account written before spec.state existed reads back defaulted by
      // the apiserver; the fallback is for a response assembled anywhere else.
      state: i.spec.state ?? 'enabled',
      // A failure to read one account's keys must not blank the whole table:
      // -1 renders as "?" below, which is honest, where 0 would be a lie
      // about an account that may well hold keys.
      keyCount: await listCredentials(i.spec.email).then((k) => k.length).catch(() => -1),
    })),
  )
}

async function patchAccount(name: string, patch: Record<string, unknown>): Promise<void> {
  const res = await fetch(`${FRAMEUSERS}/${name}`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/merge-patch+json',
      Authorization: `Bearer ${(globalThis as Record<string, unknown>).__FRAME_TOKEN__ ?? ''}`,
    },
    body: JSON.stringify({ spec: patch }),
  })
  if (!res.ok) throw new Error(await res.text())
}

export function AccountsView() {
  const [accounts, setAccounts] = useState<Account[]>([])
  const [error, setError] = useState<string | undefined>(undefined)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<InvitableRole>('viewer')
  const [inviteLink, setInviteLink] = useState<string | undefined>(undefined)
  const [keysFor, setKeysFor] = useState<string | undefined>(undefined)
  const [keys, setKeys] = useState<CredentialSummary[]>([])

  const refresh = useCallback(() => {
    loadAccounts()
      .then((next) => {
        setAccounts(next)
        setError(undefined)
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
  }, [])

  useEffect(refresh, [refresh])

  async function act(fn: () => Promise<void>) {
    try {
      await fn()
      setError(undefined)
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleInvite(e: FormEvent) {
    e.preventDefault()
    try {
      // The link is shown, never sent: there is no mail path in this cluster.
      setInviteLink(await inviteAccount(inviteEmail.trim(), inviteRole))
      setError(undefined)
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  async function showKeys(email: string) {
    setKeysFor(email)
    try {
      setKeys(await listCredentials(email))
    } catch (err) {
      setKeys([])
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="font-mono text-sm">Accounts</CardTitle>
        <Button className="font-mono gap-1.5" onClick={() => { setInviteLink(undefined); setInviteOpen(true) }}>
          <UserPlus />
          Invite
        </Button>
      </CardHeader>
      <CardContent className="space-y-3">
        {error && (
          <p role="alert" className="text-xs font-mono text-destructive break-words">
            {error}
          </p>
        )}
        <table className="w-full text-xs font-mono">
          <thead className="text-muted-foreground">
            <tr>
              <th className="text-left font-normal">Email</th>
              <th className="text-left font-normal">Role</th>
              <th className="text-left font-normal">State</th>
              <th className="text-right font-normal">Keys</th>
            </tr>
          </thead>
          <tbody>
            {accounts.map((a) => (
              <tr key={a.name} className="border-t border-border">
                <td className="py-1.5">{a.email}</td>
                <td>
                  <select
                    className="bg-transparent"
                    value={a.role}
                    onChange={(e) => void act(() => patchAccount(a.name, { role: e.target.value }))}
                  >
                    <option value="viewer">viewer</option>
                    <option value="operator">operator</option>
                    <option value="admin">admin</option>
                  </select>
                </td>
                <td>
                  <Button
                    variant="outline"
                    size="sm"
                    className="font-mono"
                    onClick={() =>
                      void act(() =>
                        patchAccount(a.name, { state: a.state === 'enabled' ? 'disabled' : 'enabled' }),
                      )
                    }
                  >
                    {a.state}
                  </Button>
                </td>
                <td className="text-right">
                  <Button variant="ghost" size="sm" className="font-mono" onClick={() => void showKeys(a.email)}>
                    {a.keyCount < 0 ? '?' : a.keyCount} {a.keyCount === 1 ? 'key' : 'keys'}
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>

        {keysFor && (
          <div className="space-y-1.5 pt-2 border-t border-border">
            <p className="text-[10px] font-mono uppercase tracking-widest text-muted-foreground">
              Keys enrolled by {keysFor}
            </p>
            {keys.length === 0 ? (
              <p className="text-xs text-muted-foreground">No keys enrolled.</p>
            ) : (
              <ul className="space-y-1">
                {keys.map((k) => (
                  <li
                    key={k.id}
                    className="flex items-center justify-between rounded border border-border bg-secondary/30 px-2 py-1.5 text-xs font-mono"
                  >
                    <span className="truncate">{k.label || k.id}</span>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        void act(async () => {
                          await revokeCredential(k.id, keysFor)
                          setKeys(await listCredentials(keysFor))
                        })
                      }
                    >
                      <Trash />
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </CardContent>

      <Dialog open={inviteOpen} onOpenChange={setInviteOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="font-mono">Invite someone</DialogTitle>
            <DialogDescription>
              Creates an account with no credential and returns a link. Copy it to them yourself —
              Frame sends no mail. The link expires in 24 hours and dies the moment they enrol a key.
            </DialogDescription>
          </DialogHeader>
          <form className="space-y-3" onSubmit={handleInvite}>
            <div className="space-y-1.5">
              <Label htmlFor="invite-email">Email</Label>
              <Input
                id="invite-email"
                type="email"
                value={inviteEmail}
                onChange={(e) => setInviteEmail(e.target.value)}
                required
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="invite-role">Role</Label>
              <select
                id="invite-role"
                className="w-full bg-transparent border border-border rounded px-2 py-1.5 text-sm font-mono"
                value={inviteRole}
                onChange={(e) => setInviteRole(e.target.value as InvitableRole)}
              >
                <option value="viewer">viewer</option>
                <option value="operator">operator</option>
              </select>
              <p className="text-[10px] text-muted-foreground">
                An invitation cannot create an admin — authd acts under its own identity, and only an
                admin may mint one. Invite, then change the role in the table above.
              </p>
            </div>
            <Button type="submit" className="w-full font-mono" disabled={!inviteEmail.trim()}>
              Create invitation
            </Button>
          </form>
          {inviteLink && (
            <p className="text-xs font-mono break-all rounded border border-border bg-secondary/30 p-2">
              {inviteLink}
            </p>
          )}
          <DialogFooter>
            <Button variant="outline" className="font-mono" onClick={() => setInviteOpen(false)}>
              Close
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
