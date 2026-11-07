package orchestration

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
)

// BootstrapPorts is the bootstrap's only effects: the Secrets it reads by name, and HyperDX's first run.
type BootstrapPorts struct {
	Data      kube.Data
	Installer clickstack.Installer
}

// BootstrapOptions bounds the wait for HyperDX's API: Ready is retried every Interval until it answers or Wait has
// passed (zero tries once). DryRun reads and decides, and neither registers nor sets the key.
type BootstrapOptions struct {
	DryRun         bool
	Wait, Interval time.Duration
}

// BootstrapLevel is a line's verdict: ok is a fact that holds or a change made, info a change a dry run would make
// or a note, warn something to look at that does not fail the run.
type BootstrapLevel string

const (
	BootstrapOK   BootstrapLevel = "ok"
	BootstrapInfo BootstrapLevel = "info"
	BootstrapWarn BootstrapLevel = "warn"
)

// BootstrapLine is one outcome; it never holds a credential, only the admin's email.
type BootstrapLine struct {
	Level BootstrapLevel
	Text  string
}

// BootstrapReport is what the bootstrap found and did, in order.
type BootstrapReport struct{ Lines []BootstrapLine }

func (r *BootstrapReport) add(level BootstrapLevel, text string) {
	r.Lines = append(r.Lines, BootstrapLine{Level: level, Text: text})
}

// The bootstrap's inputs: the admin account and the ingestion key, from SOPS through clickstack-runtime-inputs.
const (
	adminPasswordKey = "CLICKSTACK_ADMIN_PASSWORD"
	ingestionKey     = "CLICKSTACK_INGESTION_KEY"
)

// bootstrapInputs are the credentials the bootstrap writes from, and MongoDB's address.
type bootstrapInputs struct{ email, password, key, uri string }

// BootstrapClickStack makes HyperDX's first account and its team's ingestion key the ones in Git: it reads the
// credentials, waits for the API, registers the admin when no team exists, and sets the one team's key when it
// differs. Run again it changes nothing. The first failure stops it and is the error; the report says how far it
// got. A dry run decides the same and writes nothing; with no team it cannot check the account or the key.
func BootstrapClickStack(ctx context.Context, ports BootstrapPorts, options BootstrapOptions) (BootstrapReport, error) {
	var report BootstrapReport
	inputs, err := readBootstrapInputs(ctx, ports.Data)
	if err != nil {
		return report, err
	}
	if err := waitReady(ctx, ports.Installer, options); err != nil {
		return report, err
	}
	report.add(BootstrapOK, "HyperDX API is ready")
	registered, err := register(ctx, ports.Installer, inputs, options.DryRun, &report)
	if err != nil || !registered {
		return report, err
	}
	exists, err := ports.Installer.AccountExists(ctx, inputs.uri, inputs.email)
	switch {
	case err != nil:
		return report, err
	case exists:
		report.add(BootstrapOK, inputs.email+" has an account")
	default:
		report.add(BootstrapWarn, "no account for "+inputs.email+": the team was registered with another address")
	}
	return report, setTeamKey(ctx, ports.Installer, inputs, options.DryRun, &report)
}

// readBootstrapInputs reads both Secrets, each once, before anything is asked of ClickStack.
func readBootstrapInputs(ctx context.Context, data kube.Data) (bootstrapInputs, error) {
	var inputs bootstrapInputs
	if err := secretKeys(ctx, data, clickstackInputs, "see platform/clickstack/secret.sops.yaml", map[string]*string{
		adminEmailKey: &inputs.email, adminPasswordKey: &inputs.password, ingestionKey: &inputs.key}); err != nil {
		return inputs, err
	}
	return inputs, secretKeys(ctx, data, mongoURISecret, "is ClickStack's MongoDB running?", map[string]*string{mongoURIKey: &inputs.uri})
}

// secretKeys reads one Secret in ClickStack's namespace by name and sets each key's value; absent, empty or
// unreadable is an error naming only where, with a hint.
func secretKeys(ctx context.Context, data kube.Data, name, hint string, into map[string]*string) error {
	where := clickstackNamespace + "/" + name
	secret, err := data.GetSecret(ctx, clickstackNamespace, name)
	if err != nil {
		return errors.New("cannot read " + where + ": " + err.Error() + " (" + hint + ")")
	}
	for _, key := range slices.Sorted(maps.Keys(into)) {
		if secret == nil || len(secret.Data[key]) == 0 {
			return errors.New(where + " has no " + key + " (" + hint + ")")
		}
		*into[key] = string(secret.Data[key])
	}
	return nil
}

// waitReady retries the readiness probe until it answers or the wait has passed.
func waitReady(ctx context.Context, installer clickstack.Installer, options BootstrapOptions) error {
	deadline := time.Now().Add(options.Wait)
	for {
		err := installer.Ready(ctx)
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("HyperDX API not ready after " + options.Wait.String() + " (" + err.Error() +
				"; kubectl -n observability logs deploy/clickstack-app)")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(options.Interval):
		}
	}
}

// register registers the admin when no team exists, which HyperDX allows only then. It answers whether a team now
// exists: a dry run with no team stops there, since nothing after it can be checked.
func register(ctx context.Context, installer clickstack.Installer, inputs bootstrapInputs, dryRun bool, report *BootstrapReport) (bool, error) {
	exists, err := installer.TeamExists(ctx)
	if err != nil {
		return false, err
	}
	switch {
	case exists:
		report.add(BootstrapOK, "a team exists; registration is closed")
	case dryRun:
		report.add(BootstrapInfo, "would register the admin account "+inputs.email)
		report.add(BootstrapInfo, "would set the team ingestion key to "+ingestionKey)
		return false, nil
	default:
		if err := installer.Register(ctx, inputs.email, inputs.password); err != nil {
			return false, errors.New("registration failed: " + err.Error())
		}
		report.add(BootstrapOK, "registered the admin account "+inputs.email)
	}
	return true, nil
}

// setTeamKey sets the one team's key when it differs; any number of teams but one is an error.
func setTeamKey(ctx context.Context, installer clickstack.Installer, inputs bootstrapInputs, dryRun bool, report *BootstrapReport) error {
	state, err := installer.TeamKey(ctx, inputs.uri, inputs.key)
	switch {
	case err != nil:
		return err
	case state.Teams != 1:
		return errors.New(strconv.Itoa(state.Teams) + " teams exist; expected one")
	case state.Matches:
		report.add(BootstrapOK, "team ingestion key matches "+ingestionKey)
	case dryRun:
		report.add(BootstrapInfo, "would set the team ingestion key to "+ingestionKey)
	default:
		if err := installer.SetTeamKey(ctx, inputs.uri, inputs.key); err != nil {
			return err
		}
		report.add(BootstrapOK, "set the team ingestion key to "+ingestionKey)
		report.add(BootstrapInfo, "the ClickStack collector picks up the new key over OpAMP within a minute")
	}
	return nil
}
