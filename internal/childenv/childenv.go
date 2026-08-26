// Package childenv builds the environment for every process lerp spawns: the
// runners and the provision and dispose commands around them, and lerp's own
// helpers — `ps`, `git`, the OS URL opener — which are resolved through the
// same PATH an agent can write to.
//
// Those processes inherit lerp's environment because they need it — PATH,
// HOME, whatever credentials the agent itself was configured with — with one
// deliberate hole in it. Lerp's own Linear credential is a personal API key:
// write access to every team the operator's account can see, not just the
// served ones. An agent's Linear access is meant to arrive through its own
// authorization instead, under its own identity, so inheriting the key would
// hand every runner a second channel to the board that lerp never granted and
// that no audit trail distinguishes from the operator. That one variable is
// removed here.
//
// Nothing else is filtered. The rest of the environment reaching the agent is
// documented and accepted in SECURITY.md, alongside the larger fact that an
// agent runs as the operator's user and lerp does not sandbox it — which is
// also why this is hygiene rather than containment. It closes the accidental
// path, not a determined one.
//
// It is one helper because it has to be every spawn site or none: a leak at
// the site nobody remembered is the same leak, and this repository has
// already shipped one fix that was applied at one call site and missed at
// the other. TestNoOtherPackageReadsTheEnvironment is what stops a new spawn
// site from quietly going back to os.Environ().
package childenv

import (
	"os"
	"strings"
)

// LinearAPIKeyEnv names the variable lerp reads its own Linear credential
// from, and the one variable Inherited drops.
const LinearAPIKeyEnv = "LINEAR_API_KEY"

// Inherited returns lerp's environment with LinearAPIKeyEnv removed, followed
// by extra — the "NAME=value" entries this particular child is given. Extra
// entries are passed through untouched; they are lerp's own, not inherited.
func Inherited(extra ...string) []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+len(extra))
	for _, entry := range parent {
		// An entry with no "=" is not a variable lerp put there; Cut leaves
		// it whole, so it only matches a bare LINEAR_API_KEY, which carries
		// no value to leak either way.
		if name, _, _ := strings.Cut(entry, "="); name == LinearAPIKeyEnv {
			continue
		}
		env = append(env, entry)
	}
	return append(env, extra...)
}
