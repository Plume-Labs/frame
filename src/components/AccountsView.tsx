import { FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import {
  canReissueInvitation,
  inviteAccount,
  isOnlyEnabledAdmin,
  listAccounts,
  listCredentials,
  reissueInvitation,
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
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'

const ROLE_LABEL: Record<string, MessageKey> = {
  viewer: 'acc.role.viewer',
  operator: 'acc.role.operator',
  admin: 'acc.role.admin',
}

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
  const { t, tn } = useI18n()
  const roleLabel = (r: string) => (ROLE_LABEL[r] ? t(ROLE_LABEL[r]) : r)
  const [accounts, setAccounts] = useState<Account[]>([])
  const [error, setError] = useState<string | undefined>(undefined)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRole, setInviteRole] = useState<InvitableRole>('viewer')
  const [inviteLink, setInviteLink] = useState<string | undefined>(undefined)
  // Set only when the dialog was opened from a row's "New link" button rather
  // than the top-level "Invite" button — reuses the same dialog and the same
  // "shown once" framing to display a freshly reissued link, but skips the
  // email/role form since the account already exists.
  const [reissueFor, setReissueFor] = useState<string | undefined>(undefined)
  // Which account's link is currently in flight, read back after the
  // `await` below to tell a stale response apart from the one the dialog is
  // currently showing. A ref, not state: it must be current at the instant
  // the request resolves, not just at the render that started it — two
  // quick clicks on different rows must not let the first row's response
  // land under the second row's now-open title after the second request
  // has already started.
  const reissuePending = useRef<string | undefined>(undefined)
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

  async function handleReissue(email: string) {
    reissuePending.current = email
    setReissueFor(email)
    setInviteLink(undefined)
    setInviteOpen(true)
    try {
      // The link is shown, never sent — same as a fresh invitation.
      const url = await reissueInvitation(email)
      if (reissuePending.current !== email) return // superseded by a later click
      setInviteLink(url)
      setError(undefined)
    } catch (err) {
      if (reissuePending.current !== email) return
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  function closeInviteDialog() {
    setInviteOpen(false)
    setReissueFor(undefined)
    reissuePending.current = undefined
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
        <CardTitle className="font-mono text-sm">{t('acc.title')}</CardTitle>
        <Button
          className="font-mono gap-1.5"
          onClick={() => {
            reissuePending.current = undefined
            setReissueFor(undefined)
            setInviteLink(undefined)
            setInviteOpen(true)
          }}
        >
          <UserPlus />
          {t('acc.invite')}
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
              <th className="text-left font-normal">{t('acc.email')}</th>
              <th className="text-left font-normal">{t('acc.role')}</th>
              <th className="text-left font-normal">{t('acc.state')}</th>
              <th className="text-right font-normal">{t('acc.keys')}</th>
              <th className="text-right font-normal" />
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
                      aria-label={t('acc.roleFor', { email: a.email })}
                      className="bg-transparent disabled:opacity-50"
                      value={a.role}
                      disabled={lastAdmin}
                      title={lastAdmin ? t('acc.onlyAdminRole') : undefined}
                      onChange={(e) => setPendingRole({ account: a, role: e.target.value })}
                    >
                      <option value="viewer">{roleLabel('viewer')}</option>
                      <option value="operator">{roleLabel('operator')}</option>
                      <option value="admin">{roleLabel('admin')}</option>
                    </select>
                  </td>
                  <td>
                    <span className="text-muted-foreground mr-2">{a.state === 'enabled' ? t('acc.enabled') : a.state === 'disabled' ? t('acc.disabled') : a.state}</span>
                    <Button
                      variant="outline"
                      size="sm"
                      className="font-mono"
                      disabled={lastAdmin && a.state === 'enabled'}
                      title={
                        lastAdmin && a.state === 'enabled'
                          ? t('acc.onlyAdminState')
                          : undefined
                      }
                      onClick={() =>
                        a.state === 'enabled'
                          ? setPendingDisable(a)
                          : void act(() => setAccountState(a.name, a.email, 'enabled'))
                      }
                    >
                      {a.state === 'enabled' ? t('acc.disable') : t('acc.enable')}
                    </Button>
                  </td>
                  <td className="text-right">
                    <Button variant="ghost" size="sm" className="font-mono" onClick={() => void showKeys(a.email)}>
                      {a.keyCount < 0 ? `? ${t('acc.keysUnit')}` : tn('acc.keyCount', a.keyCount)}
                    </Button>
                  </td>
                  <td className="text-right">
                    {/* See canReissueInvitation's own doc comment for what this excludes and why. */}
                    {canReissueInvitation(a) && (
                      <Button
                        variant="outline"
                        size="sm"
                        className="font-mono"
                        onClick={() => void handleReissue(a.email)}
                      >
                        {t('acc.newLink')}
                      </Button>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>

        {keysFor && (
          <div className="space-y-1.5 pt-2 border-t border-border">
            <p className="text-[10px] font-mono uppercase tracking-widest text-muted-foreground">
              {t('acc.keysBy', { email: keysFor })}
            </p>
            {keysError ? (
              <p className="text-xs text-destructive">{t('acc.keysError')}</p>
            ) : keys.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t('acc.noKeys')}</p>
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
                      aria-label={t('acc.revokeKey', { name: k.label || k.id })}
                      title={t('acc.revokeKey', { name: k.label || k.id })}
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

      <Dialog open={inviteOpen} onOpenChange={(open) => (open ? setInviteOpen(true) : closeInviteDialog())}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="font-mono">
              {reissueFor ? t('acc.newLinkFor', { email: reissueFor }) : t('acc.inviteSomeone')}
            </DialogTitle>
            <DialogDescription>
              {reissueFor ? t('acc.reissueIntro') : t('acc.inviteIntro')}{' '}
              {/*
                No number here on purpose. The lifetime is
                ServerConfig.InviteTTL (internal/authd/server.go), which
                defaults to 24 hours but exists to be set; a literal "24
                hours" in this copy agrees with it only for as long as
                nothing does. Rendering the real value would mean widening
                the /auth/invite response, its SDK type and its tests, and
                formatting a duration for humans — a lot of machinery for a
                reassurance clause whose job is only "this does not sit
                around forever". Saying that without a number cannot go
                stale.
              */}
              {t('acc.inviteNote')}
            </DialogDescription>
          </DialogHeader>
          {/*
            Duplicated from the CardContent error paragraph below, not moved:
            Radix renders DialogContent in a portal and marks the rest of the
            page inert while it's open, so a refusal (a disabled account, or
            one that already holds a credential — the button above is meant
            to keep both unreachable, but the server is the real guard) set
            only at :165 would land outside the open dialog's accessible
            subtree — dimmed for a sighted admin, invisible to a screen
            reader, and with the reissue dialog skipping its form entirely
            (see !reissueFor below), the only thing rendered would be the
            title and description, forever.
          */}
          {error && (
            <p role="alert" className="text-xs font-mono text-destructive break-words">
              {error}
            </p>
          )}
          {!reissueFor && (
            <form className="space-y-3" onSubmit={handleInvite}>
              <div className="space-y-1.5">
                <Label htmlFor="invite-email">{t('acc.email')}</Label>
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
                <Label htmlFor="invite-role">{t('acc.role')}</Label>
                <select
                  id="invite-role"
                  className="w-full bg-transparent border border-border rounded px-2 py-1.5 text-sm font-mono"
                  value={inviteRole}
                  onChange={(e) => setInviteRole(e.target.value as InvitableRole)}
                >
                  <option value="viewer">{roleLabel('viewer')}</option>
                  <option value="operator">{roleLabel('operator')}</option>
                </select>
                <p className="text-[10px] text-muted-foreground">
                  {t('acc.noAdminInvite')}
                </p>
              </div>
              <Button type="submit" className="w-full font-mono" disabled={!inviteEmail.trim()}>
                {t('acc.createInvitation')}
              </Button>
            </form>
          )}
          {inviteLink && (
            <p className="text-xs font-mono break-all rounded border border-border bg-secondary/30 p-2">
              {inviteLink}
            </p>
          )}
          <DialogFooter>
            <Button variant="outline" className="font-mono" onClick={closeInviteDialog}>
              {t('common.close')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!pendingRole} onOpenChange={(open) => !open && setPendingRole(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">
              {pendingRole?.role === 'admin'
                ? t('acc.promoteTitle', { email: pendingRole.account.email })
                : t('acc.changeRoleTitle', { email: pendingRole?.account.email ?? '' })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRole?.role === 'admin'
                ? t('acc.promoteDesc', { email: pendingRole.account.email })
                : pendingRole?.account.role === 'admin'
                  ? t('acc.demoteDesc', { email: pendingRole.account.email })
                  : t('acc.changeRoleDesc', {
                      email: pendingRole?.account.email ?? '',
                      from: roleLabel(pendingRole?.account.role ?? ''),
                      to: roleLabel(pendingRole?.role ?? ''),
                    })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!pendingRole) return
                const { account, role } = pendingRole
                setPendingRole(undefined)
                void act(() => setAccountRole(account.name, account.email, role))
              }}
            >
              {t('common.confirm')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!pendingDisable} onOpenChange={(open) => !open && setPendingDisable(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">{t('acc.disableTitle', { email: pendingDisable?.email ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('acc.disableDesc', { email: pendingDisable?.email ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!pendingDisable) return
                const account = pendingDisable
                setPendingDisable(undefined)
                void act(() => setAccountState(account.name, account.email, 'disabled'))
              }}
            >
              {t('acc.disable')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!pendingRevoke} onOpenChange={(open) => !open && setPendingRevoke(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">
              {t('acc.revokeTitle', { name: pendingRevoke?.key.label || pendingRevoke?.key.id || '' })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t('acc.revokeDesc', { email: pendingRevoke?.email ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!pendingRevoke) return
                const { email, key } = pendingRevoke
                setPendingRevoke(undefined)
                void act(async () => {
                  await revokeCredential(key.id, email)
                  // The revoke already succeeded — a failure here is only in
                  // re-reading the list, and must not leave the just-revoked
                  // key looking live under an unrelated error message. Same
                  // fix as showKeys/refreshKeys: clear the list and flag it,
                  // rather than silently keeping the stale (pre-revoke) one.
                  try {
                    setKeys(await listCredentials(email))
                    setKeysError(false)
                  } catch (err) {
                    setKeys([])
                    setKeysError(true)
                    throw err
                  }
                })
              }}
            >
              {t('acc.revoke')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}
