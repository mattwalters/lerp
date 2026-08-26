// Package credentials resolves the credential lerp signs Linear requests
// with, and keeps the stored one alive.
//
// SCOPE.md invariant 4: lerp authenticates as the operator, and there are
// two ways it can — a personal API key in LINEAR_API_KEY, or the OAuth
// token `lerp login` stores. This package is the one place that chooses
// between them, so every call site holds a source rather than a string.
//
// The token file is the operator's credential, not the clone's state: it
// lives in the user config dir, never in .lerp/ (invariant 1). Losing it
// costs a re-login, never correctness.
package credentials
