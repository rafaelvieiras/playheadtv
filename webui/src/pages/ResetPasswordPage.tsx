import { type FormEvent, useEffect, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Link } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/http'
import { extractResetToken, validateNewPassword } from '@/lib/passwordReset'
import { useAuthStore } from '@/store/auth'

interface ConsumeResponse {
  message: string
}

// Public page (no session): the person who ran `server reset-password` on the
// server chooses the new password here. The token comes from the URL fragment
// or query string, is kept only in component state, and is removed from the
// address bar right after load so it does not stay in history or screenshots.
export function ResetPasswordPage() {
  const [token, setToken] = useState(() => extractResetToken(window.location.search, window.location.hash))
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [formError, setFormError] = useState<string | null>(null)
  const logout = useAuthStore((s) => s.logout)

  useEffect(() => {
    if (window.location.search || window.location.hash) {
      window.history.replaceState(window.history.state, '', window.location.pathname)
    }
  }, [])

  const mutation = useMutation({
    mutationFn: () => api.post<ConsumeResponse>('/password-resets/consume', { token, new_password: password }),
    onSuccess: () => {
      // The server ended every session, so drop the stale local one too.
      logout()
      setPassword('')
      setConfirm('')
      setToken('')
    },
  })

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const problem = validateNewPassword(password, confirm)
    if (problem) {
      setFormError(problem)
      return
    }
    if (!token) {
      setFormError('Enter the reset token.')
      return
    }
    setFormError(null)
    mutation.mutate()
  }

  const done = mutation.isSuccess
  const failure = formError ?? (mutation.isError ? mutation.error.message : null)

  return (
    <div className="flex min-h-svh items-center justify-center bg-background px-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Reset password</CardTitle>
          <CardDescription>
            Run <code>server reset-password</code> on the server to get a token, then choose a new password.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {done ? (
            <div className="flex flex-col gap-4">
              <p role="status" data-testid="reset-success" className="text-sm">
                {mutation.data.message}
              </p>
              <Button asChild>
                <Link to="/login">Go to sign in</Link>
              </Button>
            </div>
          ) : (
            <form onSubmit={handleSubmit}>
              <FieldGroup>
                <Field>
                  <FieldLabel htmlFor="reset-token">Reset token</FieldLabel>
                  <Input
                    id="reset-token"
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    required
                    value={token}
                    onChange={(e) => setToken(e.target.value.trim())}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="new-password">New password</FieldLabel>
                  <Input
                    id="new-password"
                    type="password"
                    autoComplete="new-password"
                    required
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="confirm-password">Repeat new password</FieldLabel>
                  <Input
                    id="confirm-password"
                    type="password"
                    autoComplete="new-password"
                    required
                    value={confirm}
                    onChange={(e) => setConfirm(e.target.value)}
                  />
                </Field>
                {failure && (
                  <p role="alert" data-testid="reset-error" className="text-sm text-destructive">
                    {failure}
                  </p>
                )}
                <Button type="submit" disabled={mutation.isPending}>
                  {mutation.isPending ? 'Saving…' : 'Change password'}
                </Button>
                <Link to="/login" className="text-center text-sm text-muted-foreground underline">
                  Back to sign in
                </Link>
              </FieldGroup>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
