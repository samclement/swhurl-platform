package dashboards

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// The catalogue is the fixed list of platform services; a unit that runs a workload of its own is covered by an
// entry, and every other unit is named with no entry and its reason. A new unit under platform/ or infra/ fails
// both halves of this test until it is added to one of them.
var coveredUnits = map[string][]string{
	"infra/base":                    nil, // namespaces and the storage class, no workload
	"infra/cert-manager":            {"cert-manager"},
	"infra/issuers":                 nil, // a ClusterIssuer, no workload
	"infra/traefik":                 {"traefik"},
	"platform/alerts":               nil, // Flux's Provider and Alert, no workload
	"platform/auth":                 nil, // rendered ConfigMaps
	"platform/backup":               {"backup"},
	"platform/clickstack":           {"clickstack"},
	"platform/clickstack-operators": nil, // operators, whose lines the log filter drops as noise
	"platform/console":              {"console", "console-dashboards", "console-notifications"},
	"platform/egress":               {"egress"},
	"platform/flags":                nil,                  // ConfigMaps and Streams
	"platform/flux-webhook":         {"flux-controllers"}, // a Service in front of Flux's notification-controller
	"platform/image-automation":     {"flux-controllers"}, // the image controllers run and log in flux-system
	"platform/nats":                 {"nats"},
	"platform/notify":               {"notify"},
	"platform/oauth2-proxy":         {"oauth2-proxy"},
	"platform/otel":                 nil, // the collectors' own logs, not a service of the platform
	"platform/reloader":             {"reloader"},
}

func TestEveryUnitIsInTheCatalogueOrExplained(t *testing.T) {
	dirs := map[string]bool{}
	for _, parent := range []string{"../../infra", "../../platform"} {
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				dirs[parent[6:]+"/"+entry.Name()] = true
			}
		}
	}
	for unit := range dirs {
		if _, known := coveredUnits[unit]; !known {
			t.Errorf("%s is in neither the catalogue nor coveredUnits: a new unit cannot be left out silently", unit)
		}
	}
	for unit := range coveredUnits {
		if !dirs[unit] {
			t.Errorf("%s is not a unit any more; drop it or name its replacement", unit)
		}
	}
	named := map[string]bool{}
	for _, services := range coveredUnits {
		for _, service := range services {
			named[service] = true
		}
	}
	for _, service := range PlatformServices() {
		if !named[service.Name] {
			t.Errorf("the catalogue's %q is in no unit of coveredUnits", service.Name)
		}
	}
}

func TestTheCatalogueIsWellFormedAndSorted(t *testing.T) {
	services := PlatformServices()
	if len(services) == 0 {
		t.Fatal("an empty catalogue would be read as every service gone")
	}
	names := []string{}
	seen := map[string]bool{}
	for _, service := range services {
		if !appName.MatchString(service.Name) || seen[service.Name] {
			t.Errorf("%q is not a unique DNS label", service.Name)
		}
		seen[service.Name] = true
		names = append(names, service.Name)
		switch service.Kind {
		case Web, Worker:
			if service.Namespace == "" {
				t.Errorf("%q states no namespace", service.Name)
			}
			if service.HostPrefix != "" && service.Kind != Web {
				t.Errorf("%q has a request host but is not a web service", service.Name)
			}
		case Job:
			if service.RunMarker == "" {
				t.Errorf("%q is a job with no run marker: its runs would never be counted", service.Name)
			}
			if service.Namespace == "" && service.PodPattern == "" {
				t.Errorf("%q selects no pods", service.Name)
			}
		case LogsOnly:
		default:
			t.Errorf("%q has kind %q", service.Name, service.Kind)
		}
	}
	if !slices.IsSorted(names) {
		t.Errorf("the catalogue is not sorted: %v", names)
	}
}

// A platform service's tiles read what its kind reports: the edge's spans by request host, its own spans, its job's
// run marker, or only its logs.
func TestPlatformTilesFollowTheServicesKind(t *testing.T) {
	edge := Service{Name: "console", Kind: Web, Namespace: "console", Container: "main", HostPrefix: "console."}
	own := Service{Name: "traefik", Kind: Web, Namespace: "kube-system", Container: "traefik"}
	worker := Service{Name: "agent", Kind: Worker, Namespace: "agent"}
	job := Service{Name: "console-dashboards", Kind: Job, Namespace: "console", Container: "main",
		RunMarker: "Body LIKE 'ClickStack dashboard pass completed%'"}
	only := Service{Name: "nats", Kind: LogsOnly, Namespace: "messaging", Container: "nats"}

	board := PlatformDefinition(edge, "t1", "l1")
	if board.Name != "Platform: console" || !slices.Equal(board.Tags, []string{PlatformTag}) {
		t.Fatalf("%q %v", board.Name, board.Tags)
	}
	where := board.Tiles[0].Config.Select[0].Where
	if board.Tiles[0].Name != "Requests per minute" || !strings.Contains(where, "SpanKind = 'Server'") ||
		!strings.Contains(where, "'kube-system'") || !strings.Contains(where, "server.address'] LIKE 'console.%'") {
		t.Errorf("edge requests %q: %q", board.Tiles[0].Name, where)
	}
	if !strings.Contains(board.Tiles[1].Config.Select[0].Where, "http.response.status_code'] >= '500'") {
		t.Errorf("edge errors %q", board.Tiles[1].Config.Select[0].Where)
	}
	if !strings.Contains(board.Tiles[0].Config.Select[0].Where, "k8s.namespace.name") {
		t.Errorf("the edge's filter does not name its namespace: %q", where)
	}

	if got := PlatformDefinition(own, "t1", "l1").Tiles[0].Config.Select[0].Where; !strings.Contains(got, "k8s.namespace.name'] = 'kube-system'") {
		t.Errorf("a service's own requests %q", got)
	}
	if got := PlatformDefinition(worker, "t1", "l1").Tiles[0].Name; got != "Runs per minute" {
		t.Errorf("worker first tile %q", got)
	}
	if got := PlatformDefinition(job, "t1", "l1").Tiles[0]; got.Name != "Runs per minute" ||
		!strings.Contains(got.Config.Select[0].Where, "ClickStack dashboard pass completed") {
		t.Errorf("job runs %q: %+v", got.Name, got.Config.Select)
	}
	board = PlatformDefinition(only, "t1", "l1")
	if len(board.Tiles) != 3 || board.Tiles[0].Name != "Error logs" || board.Tiles[2].Name != "Latest logs" {
		t.Fatalf("logs-only tiles %+v", board.Tiles)
	}
	for _, tile := range board.Tiles {
		where := tile.Config.Where
		if tile.Config.Select != nil {
			where = tile.Config.Select[0].Where
		}
		if tile.Config.SourceID != "l1" || !strings.Contains(where, "k8s.namespace.name'] = 'messaging'") {
			t.Errorf("tile %q reads %s where %q", tile.Name, tile.Config.SourceID, where)
		}
	}
	// Every row stacks without overlapping the one above it, in a 24-column grid.
	ys := []int{}
	for _, tile := range board.Tiles {
		if tile.X+tile.W > 24 {
			t.Errorf("tile %q at %d+%d", tile.Name, tile.X, tile.W)
		}
		ys = append(ys, tile.Y)
	}
	if !slices.IsSorted(ys) || ys[0] != 0 {
		t.Errorf("rows %v", ys)
	}
}

// Each platform service appears once per kind in the catalogue, with the Kubernetes selector it is read by.
func TestTheCatalogueSelectsItsServices(t *testing.T) {
	for _, service := range PlatformServices() {
		switch service.Name {
		case "backup":
			if service.Kind != Job || service.PodPattern != "%-backup-%" ||
				service.RunMarker != "Body LIKE 'Published %'" {
				t.Errorf("backup %+v", service)
			}
		case "console", "clickstack", "oauth2-proxy":
			if service.Kind != Web || service.HostPrefix == "" {
				t.Errorf("%s %+v", service.Name, service)
			}
		case "traefik":
			if service.Kind != Web || service.HostPrefix != "" || service.Namespace != "kube-system" {
				t.Errorf("traefik %+v", service)
			}
		}
	}
}

// The two families never read or delete each other: a platform plan leaves App: dashboards and anything not named
// Platform:, and an app plan leaves Platform: dashboards.
func TestThePlatformPlanLeavesTheAppFamilyAlone(t *testing.T) {
	kept := PlatformDefinition(PlatformServices()[0], "t1", "l1")
	kept.ID = "p1"
	changed := PlatformDefinition(PlatformServices()[3], "t1", "l1")
	changed.ID, changed.Tiles = "p2", nil
	live := []Dashboard{kept, changed,
		{ID: "p3", Name: "Platform: gone", Tags: []string{PlatformTag}},
		{ID: "p4", Name: "Platform: ", Tags: []string{PlatformTag}},
		{ID: "p5", Name: "someone else's", Tags: []string{PlatformTag}},
		{ID: "a1", Name: "App: web", Tags: []string{Tag}},
		{ID: "a2", Name: "App: gone", Tags: []string{Tag}}}
	steps, err := PlanPlatformDashboards(PlatformServices(), live, both, true)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]Action{}
	for _, step := range steps {
		actions[step.Dashboard.Name] = step.Action
		if strings.HasPrefix(step.Dashboard.Name, "App: ") {
			t.Errorf("the platform plan touched %q", step.Dashboard.Name)
		}
	}
	if actions["Platform: console"] != Update || actions["Platform: gone"] != Delete ||
		actions["Platform: "] != Leave || actions["someone else's"] != Leave {
		t.Errorf("platform plan %v", actions)
	}
	if _, touched := actions["App: web"]; touched {
		t.Error("an App: dashboard is not the platform's")
	}

	appSteps, err := PlanDashboards([]App{web}, live, both, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range appSteps {
		if strings.HasPrefix(step.Dashboard.Name, PlatformPrefix) {
			t.Errorf("the app plan touched %q", step.Dashboard.Name)
		}
	}
}

func TestAnEmptyCatalogueIsAFailureNotADeletion(t *testing.T) {
	live := []Dashboard{{ID: "p1", Name: "Platform: console", Tags: []string{PlatformTag}}}
	if _, err := PlanPlatformDashboards(nil, live, both, true); !errors.Is(err, ErrNoServices) {
		t.Fatalf("err %v", err)
	}
}

// A dashboard that is already its definition is kept: the server's defaults and tile ids are not a difference.
func TestAPlatformDashboardThatIsItsDefinitionIsKept(t *testing.T) {
	wanted := PlatformDefinition(Service{Name: "console", Kind: Web, Namespace: "console", Container: "main",
		HostPrefix: "console."}, "t1", "l1")
	wanted.ID = "p1"
	if !Same(decode(t, generic(t, wanted)), wanted) {
		t.Fatal("a dashboard read back from its own JSON is not the same")
	}
	steps, err := PlanPlatformDashboards([]Service{{Name: "console", Kind: Web, Namespace: "console",
		Container: "main", HostPrefix: "console."}}, []Dashboard{wanted}, both, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Action != Keep {
		t.Fatalf("%+v", steps)
	}
}
