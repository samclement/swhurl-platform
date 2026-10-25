package kernel

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// ProfileID is a platform profile's content identity: prof-sha256 over its canonical encoding.
type ProfileID string

// PlatformProfile is what the platform owns and every compiled instance shares: the pinned
// deployment primitive, where Flux finds the files, and workload and quota defaults.
type PlatformProfile struct {
	SchemaVersion int32    `json:"schemaVersion"`
	Name          string   `json:"name"`
	Chart         Chart    `json:"chart"`
	Delivery      Delivery `json:"delivery"`
	Workload      Workload `json:"workload"`
	Quota         Quota    `json:"quota"`
	// Routing is how a signed-in or public route reaches an instance. A profile without it cannot compile
	// an exposed instance; a private instance never uses it.
	Routing Routing `json:"routing"`
	// Auth is what an app needs to verify a call: the cluster's token issuer and its public signing keys.
	// A callee cannot be rendered without it, because it would accept no caller it could not check.
	Auth Auth `json:"auth"`
	// Storage is the platform's class for an instance's data volume. A database instance cannot be
	// rendered without it; the size belongs to the instance.
	Storage PlatformStorage `json:"storage"`
	// Egress is the platform's own way out: the proxy every instance reaches the internet through, and
	// the DNS service that lets it resolve a name at all.
	Egress EgressProxy `json:"egress"`
	DNS    DNS         `json:"dns"`
}

// EgressProxy is where the platform's proxy lives and which pods serve it. The instance's own port is
// rendered from its allocation; this block is the same for every instance.
type EgressProxy struct {
	Address   string            `json:"address"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

// DNS is the cluster's name service: no instance may reach anything without resolving it first.
type DNS struct {
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

// PlatformStorage is where a data volume lives and what survives its instance.
type PlatformStorage struct {
	Class string `json:"class"`
}

// Auth is the platform's token verification facts. Keys is the JWKS text exactly as an instance receives
// it, so a rotation changes the profile's identity and every compilation that renders it.
type Auth struct {
	Issuer string `json:"issuer"`
	Keys   string `json:"keys"`
}

// Routing is the platform's own facts about routes: the domain names hang off, the ingress class, the
// shared sign-in middleware, the certificate issuer, and which pods may reach an instance's port. Host
// names are derived from it, never stored per instance.
type Routing struct {
	BaseDomain       string            `json:"baseDomain"`
	Class            string            `json:"class"`
	Middleware       string            `json:"middleware"`
	Issuer           string            `json:"issuer"`
	IngressNamespace string            `json:"ingressNamespace"`
	IngressLabels    map[string]string `json:"ingressLabels"`
}

type Chart struct {
	Name                string `json:"name"`
	Version             string `json:"version"`
	Repository          string `json:"repository"`
	RepositoryNamespace string `json:"repositoryNamespace"`
}

// Delivery is how Flux finds the files: its GitRepository, the root instances are written under, the
// root units are written under (kustomize can only list a unit from inside its own directory), and the
// units every instance waits for. DependsOn is copied whenever a profile is used, so nothing aliases it.
type Delivery struct {
	Source     string   `json:"source"`
	OutputRoot string   `json:"outputRoot"`
	UnitsRoot  string   `json:"unitsRoot"`
	DependsOn  []string `json:"dependsOn"`
}

type Workload struct {
	CPURequestMilli      int32 `json:"cpuRequestMilli"`
	StartupSeconds       int32 `json:"startupSeconds"`
	StartupPeriodSeconds int32 `json:"startupPeriodSeconds"`
}

type Quota struct {
	Pods              int32 `json:"pods"`
	RequestsCPUMilli  int32 `json:"requestsCpuMilli"`
	RequestsMemoryMiB int32 `json:"requestsMemoryMiB"`
	LimitsMemoryMiB   int32 `json:"limitsMemoryMiB"`
}

var (
	labelPattern      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	versionPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	rootPattern       = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(/[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	domainPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	issuerPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	middlewarePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?(@[a-z]+)?$`)
	labelKeyPattern   = regexp.MustCompile(`^([a-z0-9]([a-z0-9.-]*[a-z0-9])?/)?[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)
	issuerURLPattern  = regexp.MustCompile(`^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)
	jwksPattern       = regexp.MustCompile(`"keys"\s*:\s*\[`)
)

// sortedKeys is a map's keys in order, so validation and identity encoding are deterministic.
func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func DecodeProfile(data []byte) (PlatformProfile, error) {
	if err := document(data); err != nil {
		return PlatformProfile{}, err
	}
	root, err := fields(data, "profile", []string{"schemaVersion", "name", "chart", "delivery", "workload", "quota"}, "routing", "auth", "storage", "egress", "dns")
	if err != nil {
		return PlatformProfile{}, err
	}
	for _, part := range []struct {
		key      string
		required []string
	}{
		{"chart", []string{"name", "version", "repository", "repositoryNamespace"}},
		{"delivery", []string{"source", "outputRoot", "unitsRoot", "dependsOn"}},
		{"workload", []string{"cpuRequestMilli", "startupSeconds", "startupPeriodSeconds"}},
		{"quota", []string{"pods", "requestsCpuMilli", "requestsMemoryMiB", "limitsMemoryMiB"}},
		// routing and auth are optional blocks: a profile that renders only private, call-less instances
		// needs neither, and an absent block is valid.
		{"routing", []string{"baseDomain", "class", "middleware", "issuer", "ingressNamespace", "ingressLabels"}},
		{"auth", []string{"issuer", "keys"}},
		{"storage", []string{"class"}},
		{"egress", []string{"address", "namespace", "labels"}},
		{"dns", []string{"namespace", "labels"}},
	} {
		if _, present := root[part.key]; !present {
			continue
		}
		if _, err := fields(root[part.key], part.key, part.required); err != nil {
			return PlatformProfile{}, err
		}
	}
	var profile PlatformProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return PlatformProfile{}, refuse(Invalid, "profile", "incorrect field type or integer range")
	}
	if err := profile.validate(); err != nil {
		return PlatformProfile{}, err
	}
	return profile, nil
}

// validate checks the fields in the order of the profile's field table.
func (p PlatformProfile) validate() error {
	if p.SchemaVersion != 1 {
		return refuse(Unsupported, "schemaVersion", "only version 1 is supported")
	}
	if !namePattern.MatchString(p.Name) {
		return refuse(Invalid, "name", "expected an app-name-style profile name")
	}
	if err := p.Chart.validate(); err != nil {
		return err
	}
	if err := p.Delivery.validate(); err != nil {
		return err
	}
	if err := p.Workload.validate(); err != nil {
		return err
	}
	if err := p.Quota.validate(); err != nil {
		return err
	}
	return p.validateBlocks()
}

// validateBlocks checks the platform's optional blocks, in the order of the field table: routing, egress,
// DNS, storage and auth.
func (p PlatformProfile) validateBlocks() error {
	for _, block := range []interface{ validate() error }{p.Routing, p.Egress, p.DNS, p.Auth} {
		if err := block.validate(); err != nil {
			return err
		}
	}
	if p.Storage.Class != "" && !labelPattern.MatchString(p.Storage.Class) {
		return refuse(Invalid, "storage.class", "expected a DNS label")
	}
	return nil
}

// validate checks a configured auth block: an https issuer, and keys that are a JWKS document.
func (a Auth) validate() error {
	if a.Issuer == "" && a.Keys == "" {
		return nil
	}
	if !issuerURLPattern.MatchString(a.Issuer) {
		return refuse(Invalid, "auth.issuer", "expected an https issuer URL")
	}
	if !json.Valid([]byte(a.Keys)) || !jwksPattern.MatchString(a.Keys) {
		return refuse(Invalid, "auth.keys", "expected a JSON Web Key Set")
	}
	return nil
}

// validate checks the platform's proxy block when it states one: a DNS name, a namespace and the pods
// that serve it.
func (e EgressProxy) validate() error {
	if e.Address == "" && e.Namespace == "" && len(e.Labels) == 0 {
		return nil
	}
	if !domainPattern.MatchString(e.Address) {
		return refuse(Invalid, "egress.address", "expected a DNS name")
	}
	if !labelPattern.MatchString(e.Namespace) {
		return refuse(Invalid, "egress.namespace", "expected a DNS label")
	}
	return validateLabels("egress.labels", e.Labels)
}

// validate checks the cluster's DNS block the same way.
func (d DNS) validate() error {
	if d.Namespace == "" && len(d.Labels) == 0 {
		return nil
	}
	if !labelPattern.MatchString(d.Namespace) {
		return refuse(Invalid, "dns.namespace", "expected a DNS label")
	}
	return validateLabels("dns.labels", d.Labels)
}

// validateLabels checks a selector's labels: keys as label keys, values as DNS labels.
func validateLabels(field string, labels map[string]string) error {
	if len(labels) == 0 {
		return refuse(Invalid, field, "expected at least one label")
	}
	for _, key := range sortedKeys(labels) {
		if !labelKeyPattern.MatchString(key) || !labelPattern.MatchString(labels[key]) {
			return refuse(Invalid, field, "expected label keys and DNS-label values")
		}
	}
	return nil
}

// validate checks a configured routing block. An absent block is valid: a profile that compiles only
// private instances needs no domain, class or middleware.
func (r Routing) validate() error {
	if r.absent() {
		return nil
	}
	if !domainPattern.MatchString(r.BaseDomain) {
		return refuse(Invalid, "routing.baseDomain", "expected a DNS name")
	}
	for _, field := range []struct{ name, value string }{
		{"routing.class", r.Class}, {"routing.ingressNamespace", r.IngressNamespace},
	} {
		if !labelPattern.MatchString(field.value) {
			return refuse(Invalid, field.name, "expected a DNS label")
		}
	}
	if !issuerPattern.MatchString(r.Issuer) {
		return refuse(Invalid, "routing.issuer", "expected a certificate issuer name")
	}
	if !middlewarePattern.MatchString(r.Middleware) {
		return refuse(Invalid, "routing.middleware", "expected a middleware reference")
	}
	return r.validateIngress()
}

// absent is whether the profile states no routing at all, which is valid: such a profile compiles only
// private instances.
func (r Routing) absent() bool {
	return !r.configured() && r.Class == "" && r.Middleware == "" && r.Issuer == "" &&
		r.IngressNamespace == "" && len(r.IngressLabels) == 0
}

// validateIngress checks which pods may reach an instance's port.
func (r Routing) validateIngress() error {
	if len(r.IngressLabels) == 0 {
		return refuse(Invalid, "routing.ingressLabels", "expected at least one label")
	}
	for _, key := range sortedKeys(r.IngressLabels) {
		if !labelKeyPattern.MatchString(key) || !labelPattern.MatchString(r.IngressLabels[key]) {
			return refuse(Invalid, "routing.ingressLabels", "expected label keys and DNS-label values")
		}
	}
	return nil
}

func (c Chart) validate() error {
	for _, field := range []struct{ name, value string }{
		{"chart.name", c.Name}, {"chart.repository", c.Repository}, {"chart.repositoryNamespace", c.RepositoryNamespace},
	} {
		if !labelPattern.MatchString(field.value) {
			return refuse(Invalid, field.name, "expected a DNS label")
		}
	}
	if !versionPattern.MatchString(c.Version) {
		return refuse(Invalid, "chart.version", "expected MAJOR.MINOR.PATCH")
	}
	return nil
}

// AppTree is where v1 keeps its app records. No profile root may point into it, so nothing compiled
// can land among them.
const AppTree = "apps"

// reservedRoot is whether a profile root points at v1's app tree.
func reservedRoot(path string) bool { return path == AppTree || strings.HasPrefix(path, AppTree+"/") }

func (f Delivery) validate() error {
	if !labelPattern.MatchString(f.Source) {
		return refuse(Invalid, "delivery.source", "expected a DNS label")
	}
	// Field order: outputRoot, then unitsRoot.
	for _, root := range []struct{ name, value string }{
		{"delivery.outputRoot", f.OutputRoot},
		{"delivery.unitsRoot", f.UnitsRoot},
	} {
		if !rootPattern.MatchString(root.value) {
			return refuse(Invalid, root.name, "expected a relative path of DNS-label segments")
		}
		if reservedRoot(root.value) {
			return refuse(Unsupported, root.name, "must not be inside "+AppTree+"/, which records v1 instances")
		}
	}
	for i, unit := range f.DependsOn {
		if !labelPattern.MatchString(unit) || slices.Contains(f.DependsOn[:i], unit) {
			return refuse(Invalid, "delivery.dependsOn", "expected distinct DNS labels")
		}
	}
	return nil
}

func (w Workload) validate() error {
	if w.CPURequestMilli < 1 {
		return refuse(Invalid, "workload.cpuRequestMilli", "expected a positive integer")
	}
	if w.StartupPeriodSeconds < 1 {
		return refuse(Invalid, "workload.startupPeriodSeconds", "expected a positive integer")
	}
	if w.StartupSeconds < w.StartupPeriodSeconds || w.StartupSeconds%w.StartupPeriodSeconds != 0 {
		return refuse(Invalid, "workload.startupSeconds", "expected a positive multiple of startupPeriodSeconds")
	}
	return nil
}

func (q Quota) validate() error {
	for _, field := range []struct {
		name  string
		value int32
	}{
		{"quota.pods", q.Pods}, {"quota.requestsCpuMilli", q.RequestsCPUMilli},
		{"quota.requestsMemoryMiB", q.RequestsMemoryMiB}, {"quota.limitsMemoryMiB", q.LimitsMemoryMiB},
	} {
		if field.value < 1 {
			return refuse(Invalid, field.name, "expected a positive integer")
		}
	}
	return nil
}

func profileCanonical(p PlatformProfile) []byte {
	dependsOn := slices.Clone(p.Delivery.DependsOn)
	if dependsOn == nil {
		dependsOn = []string{}
	}
	return canonical([]any{"swhurl.profile.v1", p.SchemaVersion, p.Name,
		p.Chart.Name, p.Chart.Version, p.Chart.Repository, p.Chart.RepositoryNamespace,
		p.Delivery.Source, p.Delivery.OutputRoot, p.Delivery.UnitsRoot, dependsOn,
		p.Workload.CPURequestMilli, p.Workload.StartupSeconds, p.Workload.StartupPeriodSeconds,
		p.Quota.Pods, p.Quota.RequestsCPUMilli, p.Quota.RequestsMemoryMiB, p.Quota.LimitsMemoryMiB,
		// Routing and auth are platform facts, so a changed domain, issuer, middleware or key set changes
		// the profile's identity; an absent block encodes as empty values rather than being omitted.
		p.Routing.BaseDomain, p.Routing.Class, p.Routing.Middleware, p.Routing.Issuer,
		p.Routing.IngressNamespace, labelPairs(p.Routing.IngressLabels),
		p.Auth.Issuer, p.Auth.Keys, p.Storage.Class,
		p.Egress.Address, p.Egress.Namespace, labelPairs(p.Egress.Labels),
		p.DNS.Namespace, labelPairs(p.DNS.Labels)})
}

func (p PlatformProfile) ID() (ProfileID, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	return ProfileID(hash("prof-sha256:", profileCanonical(p))), nil
}
