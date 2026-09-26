package git

import "context"

// Env is the environment Run gives a git process, for the tests that check it.
func (r Remote) Env(ctx context.Context) ([]string, error) {
	env, _, err := r.env(ctx)
	return env, err
}
