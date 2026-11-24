import { type FormEvent, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { CircleAlert } from 'lucide-react'
import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom'

import { InlineWarning } from '@/components/InlineWarning'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  ApiError,
  api,
  isNetworkFailure,
  NEXT_PARAM,
  requestFailureReason,
  safeRouterPath,
  SESSION_ENDED_PARAM,
} from '@/lib/http'
import type { LoginResponse } from '@/lib/types'
import { useAuthStore } from '@/store/auth'

type FieldErrors = Partial<Record<'email' | 'password', string>>

// The browser's own `required` bubble points at whichever control it picks and
// hides the other problem; both are reported under their field instead, in one
// submit (see AGENTS.md "Web admin UI (webui/)").
function validate(email: string, password: string): FieldErrors {
  const errors: FieldErrors = {}
  if (email.trim() === '') {
    errors.email = 'Enter the email address of the account.'
  }
  if (password === '') {
    errors.password = 'Enter the password.'
  }
  return errors
}

// Why a sign-in failed. The backend answers 401 for both an unknown address and
// a wrong password, on purpose (backend/internal/api/auth.go login), so this
// must not claim to know which one it was. A request that never reached the
// server is a different failure entirely and used to be reported as a bad
// password, which is why an operator whose backend was down kept retyping it.
function signInFailure(error: unknown): string {
  if (isNetworkFailure(error)) {
    return requestFailureReason(error)
  }
  if (error instanceof ApiError) {
    if (error.status === 401) {
      return 'Invalid email or password.'
    }
    return `The server refused the sign-in (${error.status}: ${error.message}).`
  }
  return requestFailureReason(error)
}

// Where a successful sign-in goes: the guarded URL the router recorded in
// `state.from` (components/RequireAuth.tsx), else the destination carried
// through the 401 page load (lib/http.ts), else the guide. Both are checked
// before use, so a crafted link cannot send the browser off this origin or
// back to the login screen.
function nextPath(state: unknown, search: string): string {
  const from = (state as { from?: { pathname?: string; search?: string; hash?: string } } | null)?.from
  const fromState = from?.pathname ? `${from.pathname}${from.search ?? ''}${from.hash ?? ''}` : null
  const fromQuery = new URLSearchParams(search).get(NEXT_PARAM)
  return safeRouterPath(fromState) ?? safeRouterPath(fromQuery) ?? '/'
}

export function LoginPage() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<FieldErrors>({})
  const token = useAuthStore((s) => s.token)
  const login = useAuthStore((s) => s.login)
  const navigate = useNavigate()
  const location = useLocation()
  const target = nextPath(location.state, location.search)
  const sessionEnded = new URLSearchParams(location.search).get(SESSION_ENDED_PARAM) === 'expired'

  const mutation = useMutation({
    mutationFn: () => api.post<LoginResponse>('/sessions', { email: email.trim(), password }),
    onSuccess: (data) => {
      login(data.token, data.user)
      // The screen's own redirect used to win over the deep link, so a guarded
      // URL always landed on the guide. Both paths now use the same target.
      navigate(target, { replace: true })
    },
  })

  if (token) {
    return <Navigate to={target} replace />
  }

  const failure = mutation.isError ? signInFailure(mutation.error) : null

  // A message stops being true the moment the value behind it changes.
  function clear(field: keyof FieldErrors) {
    setErrors((current) => (current[field] ? { ...current, [field]: undefined } : current))
    if (mutation.isError) {
      mutation.reset()
    }
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const found = validate(email, password)
    setErrors(found)
    if (found.email || found.password) {
      return
    }
    mutation.mutate()
  }

  return (
    <div className="flex min-h-svh items-center justify-center bg-background px-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Playhead TV</CardTitle>
          <CardDescription>Sign in to manage channels and schedules.</CardDescription>
        </CardHeader>
        <CardContent>
          {/* noValidate: the app reports the problems, under the field they
              belong to, instead of the browser's one bubble at a time. */}
          <form onSubmit={handleSubmit} noValidate>
            <FieldGroup>
              {sessionEnded && (
                <InlineWarning title="Your session ended">
                  The server no longer accepts the session you had, so you were signed out. Sign in again and you
                  will be taken back to the page you were on.
                </InlineWarning>
              )}
              <Field data-invalid={!!errors.email}>
                <FieldLabel htmlFor="email">
                  Email <span className="text-destructive">*</span>
                </FieldLabel>
                <Input
                  id="email"
                  type="email"
                  autoComplete="username"
                  aria-invalid={!!errors.email}
                  aria-describedby={errors.email ? 'email-error' : undefined}
                  value={email}
                  onChange={(e) => {
                    setEmail(e.target.value)
                    clear('email')
                  }}
                />
                <FieldError id="email-error">{errors.email}</FieldError>
              </Field>
              <Field data-invalid={!!errors.password}>
                <div className="flex items-center justify-between gap-2">
                  <FieldLabel htmlFor="password">
                    Password <span className="text-destructive">*</span>
                  </FieldLabel>
                  <Link
                    to="/reset-password"
                    className="inline-flex min-h-11 items-center text-sm text-muted-foreground underline underline-offset-4 hover:text-primary"
                  >
                    Forgot password?
                  </Link>
                </div>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  aria-invalid={!!errors.password}
                  aria-describedby={errors.password ? 'password-error' : undefined}
                  value={password}
                  onChange={(e) => {
                    setPassword(e.target.value)
                    clear('password')
                  }}
                />
                <FieldError id="password-error">{errors.password}</FieldError>
              </Field>
              {failure && (
                // In place, not a 4 s toast: nothing else on this screen says
                // the sign-in failed, and a toast is gone before a message
                // about the server being unreachable can be read.
                <Alert variant="destructive" data-testid="login-error">
                  <CircleAlert />
                  <div className="flex flex-col gap-0.5">
                    <AlertTitle>Could not sign in</AlertTitle>
                    <AlertDescription>{failure}</AlertDescription>
                  </div>
                </Alert>
              )}
              <Button type="submit" disabled={mutation.isPending}>
                {mutation.isPending ? 'Signing in…' : 'Sign in'}
              </Button>
            </FieldGroup>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
