package orchestration

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/clickstack/clickstacktest"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/kube/kubetest"
)

const (
	bootEmail    = "admin@example.test"
	bootPassword = "Sekret-Passw0rd!"
	bootKey      = "git-ingestion-key"
	bootURI      = "mongodb://hyperdx:mongo-pass@clickstack-mongodb-svc:27017/hyperdx"
)

// bootPorts is a cluster holding both Secrets and a HyperDX in the given state.
func bootPorts(installation *clickstacktest.Installation) (BootstrapPorts, *kubetest.Fake) {
	data := &kubetest.Fake{SecretData: map[string][]kube.Secret{clickstackNamespace: {
		{Name: clickstackInputs, Data: map[string][]byte{adminEmailKey: []byte(bootEmail), adminPasswordKey: []byte(bootPassword), ingestionKey: []byte(bootKey)}},
		{Name: mongoURISecret, Data: map[string][]byte{mongoURIKey: []byte(bootURI)}},
	}}}
	installation.URI = bootURI
	return BootstrapPorts{Data: data, Installer: installation}, data
}

func bootstrap(t *testing.T, installation *clickstacktest.Installation, dryRun bool) ([]string, error) {
	t.Helper()
	ports, _ := bootPorts(installation)
	report, err := BootstrapClickStack(context.Background(), ports, BootstrapOptions{DryRun: dryRun})
	lines := []string{}
	for _, line := range report.Lines {
		lines = append(lines, "["+string(line.Level)+"] "+line.Text)
	}
	if err != nil {
		lines = append(lines, "[bad] "+err.Error())
	}
	for _, value := range []string{bootPassword, bootKey, bootURI, "mongo-pass", clickstacktest.NewTeamKey} {
		if strings.Contains(strings.Join(lines, "\n"), value) {
			t.Fatalf("the report repeats a credential:\n%s", strings.Join(lines, "\n"))
		}
	}
	return lines, err
}

func writes(installation *clickstacktest.Installation) []string {
	return slices.DeleteFunc(slices.Clone(installation.Calls), func(call string) bool {
		return call != "POST /register/password" && call != "set team key"
	})
}

func TestBootstrapAFreshInstallRegistersTheAdminAndSetsTheKey(t *testing.T) {
	installation := &clickstacktest.Installation{}
	lines, err := bootstrap(t, installation, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(installation.Accounts, []string{bootEmail}) || !slices.Equal(installation.TeamKeys, []string{bootKey}) {
		t.Fatalf("accounts %v, keys %v", installation.Accounts, installation.TeamKeys)
	}
	want := []string{"[ok] HyperDX API is ready", "[ok] registered the admin account " + bootEmail, "[ok] " + bootEmail + " has an account",
		"[ok] set the team ingestion key to CLICKSTACK_INGESTION_KEY", "[info] the ClickStack collector picks up the new key over OpAMP within a minute"}
	if !slices.Equal(lines, want) {
		t.Fatalf("report:\n%s", strings.Join(lines, "\n"))
	}
}

func TestBootstrapASecondRunChangesNothing(t *testing.T) {
	installation := &clickstacktest.Installation{Accounts: []string{bootEmail}, TeamKeys: []string{bootKey}}
	lines, err := bootstrap(t, installation, false)
	if err != nil || len(writes(installation)) != 0 {
		t.Fatalf("%v, writes %v", err, writes(installation))
	}
	if !slices.Contains(lines, "[ok] a team exists; registration is closed") || !slices.Contains(lines, "[ok] team ingestion key matches CLICKSTACK_INGESTION_KEY") {
		t.Fatalf("report:\n%s", strings.Join(lines, "\n"))
	}
}

func TestBootstrapAnExistingTeamWithARandomKeyGetsTheGitKey(t *testing.T) {
	installation := &clickstacktest.Installation{Accounts: []string{bootEmail}, TeamKeys: []string{clickstacktest.NewTeamKey}}
	if _, err := bootstrap(t, installation, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(writes(installation), []string{"set team key"}) || installation.TeamKeys[0] != bootKey {
		t.Fatalf("never registers while a team exists: writes %v, keys %v", writes(installation), installation.TeamKeys)
	}
}

func TestBootstrapATeamRegisteredWithAnotherAddressWarnsAndStillSetsTheKey(t *testing.T) {
	installation := &clickstacktest.Installation{Accounts: []string{"someone@example.test"}, TeamKeys: []string{clickstacktest.NewTeamKey}}
	lines, err := bootstrap(t, installation, false)
	if err != nil || installation.TeamKeys[0] != bootKey {
		t.Fatalf("%v, keys %v", err, installation.TeamKeys)
	}
	if !slices.Contains(lines, "[warn] no account for "+bootEmail+": the team was registered with another address") {
		t.Fatalf("report:\n%s", strings.Join(lines, "\n"))
	}
}

func TestBootstrapADryRunWritesNothing(t *testing.T) {
	for name, installation := range map[string]*clickstacktest.Installation{
		"fresh":      {},
		"random key": {Accounts: []string{bootEmail}, TeamKeys: []string{clickstacktest.NewTeamKey}},
	} {
		lines, err := bootstrap(t, installation, true)
		if err != nil || len(writes(installation)) != 0 {
			t.Fatalf("%s: %v, writes %v", name, err, writes(installation))
		}
		if !slices.Contains(lines, "[info] would set the team ingestion key to CLICKSTACK_INGESTION_KEY") {
			t.Fatalf("%s: report:\n%s", name, strings.Join(lines, "\n"))
		}
	}
	lines, _ := bootstrap(t, &clickstacktest.Installation{}, true)
	if !slices.Contains(lines, "[info] would register the admin account "+bootEmail) {
		t.Fatalf("report:\n%s", strings.Join(lines, "\n"))
	}
}

func TestBootstrapFailuresStopTheBootstrap(t *testing.T) {
	cases := map[string]struct {
		installation *clickstacktest.Installation
		reason       string
	}{
		"registration refused": {&clickstacktest.Installation{Refused: 400}, "registration failed: clickstack: POST /register/password: HTTP 400"},
		"two teams":            {&clickstacktest.Installation{Accounts: []string{bootEmail}, TeamKeys: []string{"a", "b"}}, "2 teams exist; expected one"},
		"API down":             {&clickstacktest.Installation{Down: true}, "HyperDX API not ready after 0s"},
	}
	for name, c := range cases {
		_, err := bootstrap(t, c.installation, false)
		if err == nil || !strings.Contains(err.Error(), c.reason) || slices.Contains(c.installation.Calls, "set team key") {
			t.Errorf("%s: %v, writes %v", name, err, writes(c.installation))
		}
	}
	down := cases["API down"].installation
	if slices.ContainsFunc(down.Calls, func(call string) bool { return !strings.HasPrefix(call, "GET /ready") }) {
		t.Fatalf("nothing is asked before the API is up: %v", down.Calls)
	}
}

func TestBootstrapEveryCredentialIsReadBeforeClickStackIsAsked(t *testing.T) {
	for _, key := range []string{adminEmailKey, adminPasswordKey, ingestionKey} {
		installation := &clickstacktest.Installation{}
		ports, data := bootPorts(installation)
		delete(data.SecretData[clickstackNamespace][0].Data, key)
		_, err := BootstrapClickStack(context.Background(), ports, BootstrapOptions{})
		if err == nil || !strings.Contains(err.Error(), "observability/clickstack-runtime-inputs has no "+key) || len(installation.Calls) != 0 {
			t.Errorf("%s: %v, calls %v", key, err, installation.Calls)
		}
	}
	installation := &clickstacktest.Installation{}
	ports, data := bootPorts(installation)
	data.SecretData[clickstackNamespace] = data.SecretData[clickstackNamespace][:1]
	if _, err := BootstrapClickStack(context.Background(), ports, BootstrapOptions{}); err == nil ||
		!strings.Contains(err.Error(), "is ClickStack's MongoDB running?") || len(installation.Calls) != 0 {
		t.Errorf("no MongoDB address: %v, calls %v", err, installation.Calls)
	}
}

// slowStart is an installation whose API answers its readiness probe only from the given attempt on.
type slowStart struct {
	*clickstacktest.Installation
	attempts, readyFrom int
}

func (s *slowStart) Ready(ctx context.Context) error {
	s.attempts++
	if s.attempts < s.readyFrom {
		return errors.New("not yet")
	}
	return s.Installation.Ready(ctx)
}

func TestBootstrapWaitsForTheAPIUntilItAnswers(t *testing.T) {
	installation := &clickstacktest.Installation{Accounts: []string{bootEmail}, TeamKeys: []string{bootKey}}
	ports, _ := bootPorts(installation)
	slow := &slowStart{Installation: installation, readyFrom: 3}
	ports.Installer = slow
	if _, err := BootstrapClickStack(context.Background(), ports, BootstrapOptions{Wait: time.Minute, Interval: time.Millisecond}); err != nil || slow.attempts != 3 {
		t.Fatalf("%v after %d attempts", err, slow.attempts)
	}
	slow = &slowStart{Installation: installation, readyFrom: 1000}
	ports.Installer = slow
	_, err := BootstrapClickStack(context.Background(), ports, BootstrapOptions{Wait: 20 * time.Millisecond, Interval: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "not ready after 20ms") || slow.attempts < 2 {
		t.Fatalf("%v after %d attempts", err, slow.attempts)
	}
}

// unreadableAccounts is an installation whose account check cannot be answered.
type unreadableAccounts struct{ *clickstacktest.Installation }

func (unreadableAccounts) AccountExists(context.Context, string, string) (bool, error) {
	return false, errors.New("clickstack: account: malformed")
}

func TestBootstrapAnAccountCheckThatFailsStopsIt(t *testing.T) {
	installation := &clickstacktest.Installation{Accounts: []string{bootEmail}, TeamKeys: []string{clickstacktest.NewTeamKey}}
	ports, _ := bootPorts(installation)
	ports.Installer = unreadableAccounts{installation}
	report, err := BootstrapClickStack(context.Background(), ports, BootstrapOptions{})
	if err == nil || !strings.Contains(err.Error(), "account: malformed") || slices.Contains(installation.Calls, "set team key") {
		t.Fatalf("%v, %+v, calls %v", err, report, installation.Calls)
	}
}
