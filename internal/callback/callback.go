// Package callback reports sandbox results back to the producer over
// HTTP, authenticated with the per-task callback secret. This path is
// unchanged from the existing declarative, per-provider callback spec
// and is already language-agnostic.
package callback

import (
	"context"

	"github.com/Jiffy-Agnet/worker/internal/sandbox"
)

// Report sends a successful result to callbackURL, authenticated with
// secret (e.g. as an HMAC or bearer header — see the producer's callback
// spec for the exact scheme).
func Report(ctx context.Context, callbackURL, secret string, result sandbox.Result) error {
	// TODO: POST result as JSON to callbackURL, authenticated with secret.
	return nil
}

// ReportFailure sends failure details to callbackURL, authenticated the
// same way.
func ReportFailure(ctx context.Context, callbackURL, secret string, cause error) error {
	// TODO: POST failure details as JSON to callbackURL, authenticated
	// with secret.
	return nil
}
