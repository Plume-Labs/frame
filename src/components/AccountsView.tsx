import { FormEvent, useCallback, useEffect, useState } from 'react'
import {
  inviteAccount,
  isOnlyEnabledAdmin,
  listAccounts,
  listCredentials,
  revokeCredential,
  setAccountRole,
  setAccountState,
  type Account,
  type CredentialSummary,
  type InvitableRole,
} from '@/lib/accounts'
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
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Trash, UserPlus } from '@phosphor-icons/react'

/**
 * Who can sign in, with what rights, and on which keys.
 *
 * Two different backends, and the split is not arbitrary. Roles and
 * `spec.state` are written through `src/lib/accounts.ts`'s `setAccountRole`/
 * `setAccountState`, which go straight to the apiserver through
 * `frame-uiproxy` — so they travel under the signed-in admin's own
 * impersonated identity, which is exactly what the FrameUser admission
 * webhook asks for when it refuses a role or state change from a non-admin,
 * and the only reason promoting someone to admin works at all. Invitations and
 * key revocation go to authd, which owns credentials and is the only writer of
 * `status.credentials`.
 *
 * The screen is reachable only for an admin (App gates both the nav entry and
 * the `renderTab` case on the token's groups claim). That gate is a courtesy:
 * every write below is refused server-side for anyone else. `currentEmail`,
 * when known, is what lets this screen refuse to *offer* a demotion or a
 * disable that would strand the signed-in admin — see `isOnlyEnabledAdmin`.
 */
export function AccountsView({ currentEmail }: { currentEmail?: string }) {
  const [accounts, setAccounts] = useState<Account[]>([])
  const [error, setError] = useState<string | undefined>(undefined)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<InvitableRole>('viewer')
  const [inviteLink, setInviteLink] = useState<string | undefined>(undefined)
  const [keysFor, setKeysFor] = useState<string | undefined>(undefined)
  const [keys, setKeys] = useState<CredentialSummary[]>([])
  // Distinct from `keys.length === 0`: a failed read must not read as "this
  // account holds no keys" (the same "-1 is not 0" instinct as `keyCount`
  // below, applied to the panel instead of the count).
  const [keysError, setKeysError] = useState(false)
  const [pendingRole, setPendingRole] = useState<{ account: Account; role: string } | undefined>(undefined)
  const [pendingDisable, setPendingDisable] = useState<Account | undefined>(undefined)
  const [pendingRevoke, setPendingRevoke] = useState<{ email: string; key: CredentialSummary } | undefined>(
    undefined,
  )

  const refresh = useCallback(() => {
    listAccounts()
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
    setKeysError(false)
    try {
      const k = await listCredentials(email)
      setKeys(k)
      setError(undefined)
    } catch (err) {
      setKeys([])
      setKeysError(true)
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
            {accounts.map((a) => {
              // True only for the row that is both "me" and the one enabled
              // admin left — the exact condition the webhook's last-admin
              // rule would refuse anyway. Disabling the controls here is a
              // courtesy that avoids a confusing "why won't this save"; the
              // server-side rule is what actually prevents a lockout.
              const lastAdmin =
                currentEmail !== undefined && a.email === currentEmail && isOnlyEnabledAdmin(accounts, currentEmail)
              return (
                <tr key={a.name} className="border-t border-border">
                  <td className="py-1.5">{a.email}</td>
                  <td>
                    <select
                      aria-label={`Role for ${a.email}`}
                      className="bg-transparent disabled:opacity-50"
                      value={a.role}
                      disabled={lastAdmin}
                      title={lastAdmin ? 'You are the only enabled admin — promote another admin first.' : undefined}
                      onChange={(e) => setPendingRole({ account: a, role: e.target.value })}
                    >
                      <option value="viewer">viewer</option>
                      <option value="operator">operator</option>
                      <option value="admin">admin</option>
                    </select>
                  </td>
                  <td>
                    <span className="text-muted-foreground mr-2">{a.state}</span>
                    <Button
                      variant="outline"
                      size="sm"
                      className="font-mono"
                      disabled={lastAdmin && a.state === 'enabled'}
                      title={
                        lastAdmin && a.state === 'enabled'
                          ? 'You are the only enabled admin — enable another admin first.'
                          : undefined
                      }
                      onClick={() =>
                        a.state === 'enabled'
                          ? setPendingDisable(a)
                          : void act(() => setAccountState(a.name, a.email, 'enabled'))
                      }
                    >
                      {a.state === 'enabled' ? 'Disable' : 'Enable'}
                    </Button>
                  </td>
                  <td className="text-right">
                    <Button variant="ghost" size="sm" className="font-mono" onClick={() => void showKeys(a.email)}>
                      {a.keyCount < 0 ? '?' : a.keyCount} {a.keyCount === 1 ? 'key' : 'keys'}
                    </Button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>

        {keysFor && (
          <div className="space-y-1.5 pt-2 border-t border-border">
            <p className="text-[10px] font-mono uppercase tracking-widest text-muted-foreground">
              Keys enrolled by {keysFor}
            </p>
            {keysError ? (
              <p className="text-xs text-destructive">could not read this account's keys</p>
            ) : keys.length === 0 ? (
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
                      aria-label={`Revoke ${k.label || k.id}`}
                      title={`Revoke ${k.label || k.id}`}
                      onClick={() => setPendingRevoke({ email: keysFor, key: k })}
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

      <AlertDialog open={!!pendingRole} onOpenChange={(open) => !open && setPendingRole(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">
              {pendingRole?.role === 'admin' ? 'Promote' : 'Change the role of'} {pendingRole?.account.email}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRole?.role === 'admin'
                ? `This grants ${pendingRole.account.email} full admin rights over the cluster.`
                : pendingRole?.account.role === 'admin'
                  ? `This removes ${pendingRole.account.email}'s admin rights.`
                  : `This changes ${pendingRole?.account.email}'s role from ${pendingRole?.account.role} to ${pendingRole?.role}.`}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!pendingRole) return
                const { account, role } = pendingRole
                setPendingRole(undefined)
                void act(() => setAccountRole(account.name, account.email, role))
              }}
            >
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!pendingDisable} onOpenChange={(open) => !open && setPendingDisable(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">Disable {pendingDisable?.email}?</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDisable?.email} will no longer be able to sign in. Nothing is deleted — their
              enrolled keys stay on the account, and re-enabling restores access.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!pendingDisable) return
                const account = pendingDisable
                setPendingDisable(undefined)
                void act(() => setAccountState(account.name, account.email, 'disabled'))
              }}
            >
              Disable
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!pendingRevoke} onOpenChange={(open) => !open && setPendingRevoke(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">
              Revoke {pendingRevoke?.key.label || pendingRevoke?.key.id}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRevoke?.email} will no longer be able to sign in with this key. This cannot be
              undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!pendingRevoke) return
                const { email, key } = pendingRevoke
                setPendingRevoke(undefined)
                void act(async () => {
                  await revokeCredential(key.id, email)
                  setKeys(await listCredentials(email))
                })
              }}
            >
              Revoke
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}
