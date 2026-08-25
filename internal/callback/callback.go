// Package callback reports sandbox results back to the producer over
// HTTP. This path is unchanged from the existing declarative,
// per-provider callback spec and is already language-agnostic.
package callback

import (
	"context"

	"github.com/Jiffy-Agnet/worker/internal/sandbox"
)

// Report sends a successful result to callbackURL.
func Report(ctx context.Context, callbackURL string, result sandbox.Result) error {
	// TODO: POST result as JSON to callbackURL.
	return nil
}

// ReportFailure sends failure details to callbackURL.
func ReportFailure(ctx context.Context, callbackURL string, cause error) error {
	// TODO: POST failure details as JSON to callbackURL.
	return nil
}
