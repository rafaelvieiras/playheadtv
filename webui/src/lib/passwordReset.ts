// Pure helpers of the password reset page (pages/ResetPasswordPage.tsx).

export const MIN_PASSWORD_LENGTH = 8

// The token handed out by `server reset-password` rides in the URL fragment
// (never sent to the server); a query string is accepted too, and the fragment
// wins when both are present.
export function extractResetToken(search: string, hash: string): string {
  const fromHash = new URLSearchParams(hash.replace(/^#/, '')).get('token')
  if (fromHash) return fromHash.trim()
  return (new URLSearchParams(search).get('token') ?? '').trim()
}

// Returns an error message, or null when the pair is acceptable.
export function validateNewPassword(password: string, confirm: string): string | null {
  if (password.length < MIN_PASSWORD_LENGTH) {
    return `Password must be at least ${MIN_PASSWORD_LENGTH} characters.`
  }
  if (password !== confirm) {
    return 'The two passwords do not match.'
  }
  return null
}
