// Package calls resolves which instances may call which. A pair is connected only when the caller
// declares the call and the callee consents, by app name or by the caller's team. It reads nothing: the
// caller supplies the declarations and the facts a connection needs.
//
// The three outcomes are the ones the platform states: connected, waiting for the other side (not an
// error), and refused whatever the other side says (a self-call, a callee that cannot serve HTTP, or a
// malformed declaration).
package calls

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Declaration is one instance's side of every pair it takes part in, with the release facts a connection
// needs: what the instance is, and the port its own Service listens on.
type Declaration struct {
	Application, Environment, Team, Kind string
	Port                                 int32
	Calls, Allows                        []string
}

// Pair is a connected caller and callee in one environment. A caller's client reaches the callee's
// Service, so both ports are facts of the pair.
type Pair struct{ Caller, Callee, Environment string }

// Wait is a declaration that renders nothing until the other side agrees or appears.
type Wait struct{ Caller, Callee, Environment, Reason string }

// Refusal is a declaration that is an error whatever any other instance says.
type Refusal struct{ Application, Environment, Callee, Reason string }

// Resolution is every connected pair, every waiting declaration and every refusal, each in a
// deterministic order.
type Resolution struct {
	Pairs   []Pair
	Waiting []Wait
	Refused []Refusal
}

// The fixed reasons a waiting or refused declaration carries.
const (
	CalleeAbsent = "callee_absent"
	CalleeNotWeb = "callee_not_web"
	NoConsent    = "no_consent"
	SelfCall     = "self_call"
)

// Error is a malformed declaration: a name that is not an app name, a team grant without a team, or two
// declarations for one instance.
type Error struct{ Field, Reason string }

func (e *Error) Error() string { return fmt.Sprintf("invalid: %s: %s", e.Field, e.Reason) }

// Resolve evaluates every declaration against the others in its environment.
func Resolve(declarations []Declaration) (Resolution, error) {
	byInstance, err := index(declarations)
	if err != nil {
		return Resolution{}, err
	}
	resolution := Resolution{Pairs: []Pair{}, Waiting: []Wait{}, Refused: []Refusal{}}
	for _, declaration := range byInstance {
		if err := validateAllows(declaration); err != nil {
			return Resolution{}, err
		}
		for _, callee := range declaration.Calls {
			wait, refusal, pair, err := evaluate(declaration, callee, byInstance)
			if err != nil {
				return Resolution{}, err
			}
			switch {
			case pair != nil:
				resolution.Pairs = append(resolution.Pairs, *pair)
			case wait != nil:
				resolution.Waiting = append(resolution.Waiting, *wait)
			case refusal != nil:
				resolution.Refused = append(resolution.Refused, *refusal)
			}
		}
	}
	sortResolution(&resolution)
	return resolution, nil
}

// index is every instance by application and environment, refusing a repeat: two declarations for one
// instance would make its consent ambiguous.
func index(declarations []Declaration) ([]Declaration, error) {
	seen := map[string]bool{}
	ordered := make([]Declaration, 0, len(declarations))
	for _, declaration := range declarations {
		key := declaration.Environment + "/" + declaration.Application
		if seen[key] {
			return nil, &Error{"calls", "repeated_instance"}
		}
		seen[key] = true
		ordered = append(ordered, declaration)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Environment+" "+ordered[i].Application < ordered[j].Environment+" "+ordered[j].Application
	})
	return ordered, nil
}

// validateAllows checks every grant's shape, whether or not a caller ever uses it: a malformed grant is
// the declaration's error, not the absent caller's.
func validateAllows(declaration Declaration) error {
	for _, grant := range declaration.Allows {
		if name, team := strings.CutPrefix(grant, "team:"); team {
			if !namePattern().MatchString(name) {
				return &Error{"allows", "invalid_team"}
			}
			continue
		}
		if !namePattern().MatchString(grant) {
			return &Error{"allows", "invalid_app"}
		}
	}
	return nil
}

// evaluate is one declared call: connected, waiting, or refused.
func evaluate(caller Declaration, callee string, byInstance []Declaration) (*Wait, *Refusal, *Pair, error) {
	if !namePattern().MatchString(callee) {
		return nil, nil, nil, &Error{"calls", "invalid_app"}
	}
	if callee == caller.Application {
		return nil, &Refusal{caller.Application, caller.Environment, callee, SelfCall}, nil, nil
	}
	target, found := find(byInstance, caller.Environment, callee)
	switch {
	case !found:
		return &Wait{caller.Application, callee, caller.Environment, CalleeAbsent}, nil, nil, nil
	case target.Kind != "web":
		return nil, &Refusal{caller.Application, caller.Environment, callee, CalleeNotWeb}, nil, nil
	case !consents(target, caller):
		return &Wait{caller.Application, callee, caller.Environment, NoConsent}, nil, nil, nil
	}
	return nil, nil, &Pair{caller.Application, callee, caller.Environment}, nil
}

// consents is whether the callee allows this caller: by its app name, or by its team. An app's own team is
// never implicit consent for itself, and a caller with no team matches no team grant.
func consents(callee, caller Declaration) bool {
	for _, grant := range callee.Allows {
		if grant == caller.Application {
			return true
		}
		if team, isTeam := strings.CutPrefix(grant, "team:"); isTeam && caller.Team != "" && team == caller.Team {
			return true
		}
	}
	return false
}

// find is one instance in one environment.
func find(byInstance []Declaration, environment, application string) (Declaration, bool) {
	for _, declaration := range byInstance {
		if declaration.Environment == environment && declaration.Application == application {
			return declaration, true
		}
	}
	return Declaration{}, false
}

// sortResolution orders every list deterministically.
func sortResolution(resolution *Resolution) {
	sort.Slice(resolution.Pairs, func(i, j int) bool {
		a, b := resolution.Pairs[i], resolution.Pairs[j]
		return key(a.Environment, a.Caller, a.Callee) < key(b.Environment, b.Caller, b.Callee)
	})
	sort.Slice(resolution.Waiting, func(i, j int) bool {
		a, b := resolution.Waiting[i], resolution.Waiting[j]
		return key(a.Environment, a.Caller, a.Callee) < key(b.Environment, b.Caller, b.Callee)
	})
	sort.Slice(resolution.Refused, func(i, j int) bool {
		a, b := resolution.Refused[i], resolution.Refused[j]
		return key(a.Environment, a.Application, a.Callee) < key(b.Environment, b.Application, b.Callee)
	})
}

func key(parts ...string) string { return strings.Join(parts, "\x00") }

func namePattern() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
}
