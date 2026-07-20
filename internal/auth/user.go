package auth

import "context"

// User is a logged-in Google account (allowlisted).
type User struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	Sub   string `json:"sub,omitempty"`
}

type ctxKey int

const userKey ctxKey = 1

// WithUser attaches u to the request context.
func WithUser(ctx context.Context, u *User) context.Context {
	if u == nil {
		return ctx
	}
	return context.WithValue(ctx, userKey, u)
}

// UserFromContext returns the authenticated user, if any.
func UserFromContext(ctx context.Context) *User {
	if ctx == nil {
		return nil
	}
	u, _ := ctx.Value(userKey).(*User)
	return u
}
