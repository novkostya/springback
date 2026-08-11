package httpapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/novkostya/springback/core/internal/jobs"
	"github.com/novkostya/springback/core/internal/store"
	"github.com/novkostya/springback/core/internal/tools"
)

// Persistent sign-in — retrying a login while Apple is refusing them.
//
// WHY THIS EXISTS. Apple is currently rejecting new sign-ins to the unofficial Store client
// INTERMITTENTLY: measured on one box within two minutes, the same command against the same
// address returned HTTP 204, then HTTP 403, then a bogus 2FA prompt. Two other Apple IDs signed
// in successfully on the same box earlier the same day. So it is not a wall — it is a door that
// is open some of the time, and the only way through is to keep knocking politely.
//
// Doing that by hand means sitting there tapping a button, which is both miserable and the way
// to get rate-limited. Doing it on a schedule, with backoff, bounded, and stopping the instant it
// works, is strictly better behaved than a person with the same goal.
//
// THE COST, STATED. The password must stay in memory for the life of the job, where a single
// request would have held it for a second. It is never written to disk, never logged, and is
// dropped when the job ends. That is a real widening of SPEC §3's "held in memory only", so it is
// OPT-IN — the ordinary sign-in path is unchanged — and the UI says what it means before you
// choose it.
const (
	// signInFirstDelay is the wait after the first refusal. Long enough not to look like a
	// hammering client, short enough to catch a brief opening.
	signInFirstDelay = 20 * time.Second
	// signInMaxDelay caps the backoff. Apple's openings are short, so there is no point
	// backing off to half an hour.
	signInMaxDelay = 3 * time.Minute
	// signInMaxWindow bounds the whole job, and therefore how long the password is held.
	signInMaxWindow = 30 * time.Minute
)

// startPersistentSignIn runs login attempts until one works, the window closes, or the failure
// turns out to be something retrying cannot fix.
func (s *Server) startPersistentSignIn(acc store.Account, password string) *jobs.Job {
	home := acc.Home(s.Accounts.Root)
	pp := acc.KeychainPP

	return s.Jobs.Start(jobs.SignIn, "signin:"+acc.Slug, acc.Email, "", func(ctx context.Context, h *jobs.Handle) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, signInMaxWindow)
		defer cancel()

		deadline := time.Now().Add(signInMaxWindow)
		delay := signInFirstDelay
		attempt := 0

		for {
			attempt++

			// STOP IF IT IS ALREADY DONE. The user can complete a 2FA sign-in by hand
			// while this is running, and a job that kept re-authenticating an account
			// that already works would be pure risk for no gain.
			if _, err := s.Tools.AuthInfo(ctx, home, pp); err == nil {
				h.Progress(100, "signed in", "")
				_ = s.recordName(ctx, acc)
				return acc.Public(), nil
			}

			h.Progress(-1, fmt.Sprintf("attempt %d", attempt), "asking Apple")
			err := s.Tools.AuthLogin(ctx, home, pp, acc.Email, password, "")

			switch {
			case err == nil:
				h.Progress(100, "signed in", "")
				_ = s.recordName(ctx, acc)
				return acc.Public(), nil

			case errors.Is(err, tools.ErrNeeds2FA):
				// KEEP GOING RATHER THAN STOPPING HERE, because a 2FA prompt does not
				// mean Apple sent a code: ipatool prints the same prompt when it cannot
				// read Apple's reply at all (measured against an address that does not
				// exist). Stopping on it would end the job on the most common symptom of
				// the very outage it exists to wait out. If a code really did arrive, the
				// user enters it on the form and that path finishes the sign-in — this
				// job then sees the account authenticated on its next pass and stops.
				h.Progress(-1, fmt.Sprintf("attempt %d", attempt),
					"Apple asked for a code — enter it if one arrived, otherwise this keeps trying")

			case errors.Is(err, tools.ErrAppleRejected):
				h.Progress(-1, fmt.Sprintf("attempt %d", attempt), shortReason(err))

			default:
				// A wrong password, or anything else specific, is not something more
				// attempts will fix — and retrying a rejected credential is exactly how
				// an account gets locked. Stop and say so.
				return nil, err
			}

			wait := delay
			if remaining := time.Until(deadline); remaining < wait {
				if remaining <= 0 {
					return nil, fmt.Errorf("gave up after %d attempts over %s — Apple was still refusing. "+
						"Accounts already signed in are unaffected; try again later",
						attempt, signInMaxWindow)
				}
				wait = remaining
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("gave up after %d attempts — Apple was still refusing", attempt)
			case <-time.After(wait):
			}
			if delay = time.Duration(float64(delay) * 1.5); delay > signInMaxDelay {
				delay = signInMaxDelay
			}
		}
	})
}

// recordName stores the display name ipatool reports once a sign-in lands.
func (s *Server) recordName(ctx context.Context, acc store.Account) error {
	info, err := s.Tools.AuthInfo(ctx, acc.Home(s.Accounts.Root), acc.KeychainPP)
	if err != nil || info.Name == "" {
		return err
	}
	return s.Accounts.SetName(acc.Slug, info.Name)
}

// shortReason turns an Apple refusal into something that fits on one line of a progress row.
func shortReason(err error) string {
	msg := err.Error()
	if i := len(msg) - 1; i > 0 {
		if j := indexOf(msg, "Apple answered "); j >= 0 {
			return "Apple answered " + msg[j+len("Apple answered "):]
		}
	}
	return "Apple refused"
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
