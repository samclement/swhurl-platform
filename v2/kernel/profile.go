package kernel

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
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
	// Telemetry is where an image with an OpenTelemetry SDK sends: the node collector's endpoint and
	// transport. It is a platform fact, so an app states only whether it has an SDK.
	Telemetry Telemetry `json:"telemetry"`
	// Notify is the platform's notification service, which every instance is told about and carries a token
	// for. The token is derived from the pod, so this block is one address, not a credential.
	Notify Notify `json:"notify"`
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
	// Backup is how the platform protects an instance's data: the job image, where it publishes, the key
	// that encrypts the archive, the Secret its upload key is substituted from, and whether schedules run.
	Backup Backup `json:"backup"`
	// Network is the cluster's pod network. A call's rules are stated as this block rather than as peers of
	// pods, because the platform's policy controller honours address blocks and ignores pod and namespace
	// selectors (docs/current-state.md): the caller's identity is its token, not its address.
	Network Network `json:"network"`
	// NATS is the platform's event service: where an instance signs in, where the user list lives, the
	// Secret Flux decrypts an instance's seed with, and the two service identities the platform runs
	// besides the instances. Every key here is public; no seed is.
	NATS NATS `json:"nats"`
}

// NATS is the platform's broker and the identities that are not instances: the stream controller, which
// creates what the instances declare, and the console, which writes feature-flag overrides.
type NATS struct {
	URL              string `json:"url"`
	Namespace        string `json:"namespace"`
	DecryptionSecret string `json:"decryptionSecret"`
	ControllerKey    string `json:"controllerKey"`
	ConsoleKey       string `json:"consoleKey"`
}

// Network is the cluster's pod address block, as a call's rules need it.
type Network struct {
	PodCIDR string `json:"podCIDR"`
}

// Telemetry is the platform's collector: the endpoint an SDK exports to and the OTLP transport it speaks.
type Telemetry struct {
	Endpoint string `json:"endpoint"`
	Protocol string `json:"protocol"`
}

// Notify is the platform's notification service address. The audience and token path are v1's fixed token
// contract, not a per-platform choice.
type Notify struct {
	Address string `json:"address"`
}

// Backup is the platform's backup contract for an instance's SQLite database. The recipient is a public
// key, so the job encrypts before it uploads and never holds the private key; the writer is a Secret in
// the platform's own namespace that Flux substitutes the upload-only key from, so v2 renders a reference
// and never a credential value. The region is the bucket's: the job uploads to the regional endpoint, so the
// proxy must grant that host to every instance whose backup it carries.
type Backup struct {
	Image           string `json:"image"`
	Digest          string `json:"digest"`
	S3URI           string `json:"s3Uri"`
	Region          string `json:"region"`
	Recipient       string `json:"recipient"`
	WriterNamespace string `json:"writerNamespace"`
	WriterName      string `json:"writerName"`
	Suspended       bool   `json:"suspended"`
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
	// An S3 URI is a bucket and a prefix ending in "/", so a namespace appends to it unambiguously.
	backupURIPattern    = regexp.MustCompile(`^s3://[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]/[A-Za-z0-9._/-]*/$`)
	ageRecipientPattern = regexp.MustCompile(`^age1[0-9a-z]{58}$`)
	regionPattern       = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	cidrPattern         = regexp.MustCompile(`^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$`)
	telemetryEndpoint   = regexp.MustCompile(`^https?://[A-Za-z0-9$()._-]+(:[0-9]{1,5})?$`)
	notifyAddress       = regexp.MustCompile(`^https?://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	natsURLPattern      = regexp.MustCompile(`^nats://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
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
	root, err := fields(data, "profile", []string{"schemaVersion", "name", "chart", "delivery", "workload", "quota"}, "telemetry", "notify", "routing", "auth", "storage", "egress", "dns", "backup", "network", "nats")
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
		{"telemetry", []string{"endpoint", "protocol"}},
		{"notify", []string{"address"}},
		// routing and auth are optional blocks: a profile that renders only private, call-less instances
		// needs neither, and an absent block is valid.
		{"routing", []string{"baseDomain", "class", "middleware", "issuer", "ingressNamespace", "ingressLabels"}},
		{"auth", []string{"issuer", "keys"}},
		{"storage", []string{"class"}},
		{"egress", []string{"address", "namespace", "labels"}},
		{"dns", []string{"namespace", "labels"}},
		{"backup", []string{"image", "digest", "s3Uri", "region", "recipient", "writerNamespace", "writerName", "suspended"}},
		{"network", []string{"podCIDR"}},
		{"nats", []string{"url", "namespace", "decryptionSecret", "controllerKey", "consoleKey"}},
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
// DNS, storage, auth and backup.
func (p PlatformProfile) validateBlocks() error {
	for _, block := range []interface{ validate() error }{p.Telemetry, p.Notify, p.Routing, p.Egress, p.DNS, p.Auth, p.Backup, p.Network, p.NATS} {
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

// validate checks the pod network when the profile states one.
func (n Network) validate() error {
	if n.PodCIDR == "" {
		return nil
	}
	if !cidrPattern.MatchString(n.PodCIDR) {
		return refuse(Invalid, "network.podCIDR", "expected an IPv4 address block, as 10.42.0.0/16")
	}
	return nil
}

// validate checks a configured backup block: an image by digest, an S3 prefix, an age public key, and the
// Secret its upload key comes from (a reference, never a value). A block that states anything must state
// all of it: a half-configured backup would render a job that cannot publish.
func (b Backup) validate() error {
	if b.absent() {
		return nil
	}
	if !imagePattern.MatchString(b.Image) {
		return refuse(Invalid, "backup.image", "expected a repository name")
	}
	if !digestPattern.MatchString(b.Digest) {
		return refuse(Invalid, "backup.digest", "expected sha256: and 64 lowercase hex characters")
	}
	if !backupURIPattern.MatchString(b.S3URI) {
		return refuse(Invalid, "backup.s3Uri", "expected s3://<bucket>/<prefix>/")
	}
	if !regionPattern.MatchString(b.Region) {
		return refuse(Invalid, "backup.region", "expected an AWS region such as eu-west-2")
	}
	if !ageRecipientPattern.MatchString(b.Recipient) {
		return refuse(Invalid, "backup.recipient", "expected an age public key")
	}
	for _, field := range []struct{ name, value string }{
		{"backup.writerNamespace", b.WriterNamespace}, {"backup.writerName", b.WriterName},
	} {
		if !labelPattern.MatchString(field.value) {
			return refuse(Invalid, field.name, "expected a DNS label")
		}
	}
	return nil
}

// absent is whether the profile states no backup at all, which is valid: only a database instance needs
// one, and a profile that compiles none needs no facts.
func (b Backup) absent() bool {
	return b.Image == "" && b.Digest == "" && b.S3URI == "" && b.Region == "" && b.Recipient == "" &&
		b.WriterNamespace == "" && b.WriterName == "" && !b.Suspended
}

// Host is the endpoint a database's backup uploads to, as the proxy grants it: the bucket's regional S3 host
// on 443. It is empty when the profile states no backup, so nothing is granted for a job that is not rendered.
func (b Backup) Host() string {
	if b.absent() {
		return ""
	}
	bucket, _, _ := strings.Cut(strings.TrimPrefix(b.S3URI, "s3://"), "/")
	return bucket + ".s3." + b.Region + ".amazonaws.com:443"
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
		p.DNS.Namespace, labelPairs(p.DNS.Labels),
		// The backup facts are platform facts too: another bucket, image or key set is another identity.
		p.Backup.Image, p.Backup.Digest, p.Backup.S3URI, p.Backup.Region, p.Backup.Recipient,
		p.Backup.WriterNamespace, p.Backup.WriterName, strconv.FormatBool(p.Backup.Suspended),
		// The event service is a platform fact too: another broker, namespace, decryption Secret or service
		// identity is another platform's identity.
		p.NATS.URL, p.NATS.Namespace, p.NATS.DecryptionSecret, p.NATS.ControllerKey, p.NATS.ConsoleKey,
		// The pod network is a platform fact too: another block is another cluster's identity.
		p.Network.PodCIDR,
		// Telemetry and notifications are platform facts: another collector or notify service is another
		// identity, and an absent block encodes as empty values.
		p.Telemetry.Endpoint, p.Telemetry.Protocol, p.Notify.Address})
}

// validate checks a configured telemetry block: an HTTP(S) endpoint and an OTLP transport.
func (t Telemetry) validate() error {
	if t.Endpoint == "" && t.Protocol == "" {
		return nil
	}
	if !telemetryEndpoint.MatchString(t.Endpoint) {
		return refuse(Invalid, "telemetry.endpoint", "expected an http(s) endpoint")
	}
	if t.Protocol != "http/protobuf" && t.Protocol != "grpc" {
		return refuse(Unsupported, "telemetry.protocol", "only http/protobuf and grpc are supported")
	}
	return nil
}

// validate checks a configured notify block: the service's HTTP(S) address.
func (n Notify) validate() error {
	if n.Address == "" {
		return nil
	}
	if !notifyAddress.MatchString(n.Address) {
		return refuse(Invalid, "notify.address", "expected an http(s) address")
	}
	return nil
}

func (p PlatformProfile) ID() (ProfileID, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	return ProfileID(hash("prof-sha256:", profileCanonical(p))), nil
}

// validate checks the event service when the profile states one. The two service identities are public keys;
// a profile that states none of the block renders no events identity for an instance.
func (n NATS) validate() error {
	if !n.stated() {
		// An absent block is a profile that renders no events identity; a stated one must be complete.
		return nil
	}
	if !natsURLPattern.MatchString(n.URL) {
		return refuse(Invalid, "nats.url", "expected a nats:// address")
	}
	if err := n.validateNames(); err != nil {
		return err
	}
	return n.validateKeys()
}

// stated is whether the profile states the block at all; a profile without it renders no events identity.
func (n NATS) stated() bool {
	return n.URL != "" || n.Namespace != "" || n.DecryptionSecret != "" || n.ControllerKey != "" || n.ConsoleKey != ""
}

// validateNames checks the broker's address, where its user list lives, and the Secret an instance's seed is
// decrypted with.
func (n NATS) validateNames() error {
	for _, field := range []struct{ name, value string }{{"nats.namespace", n.Namespace},
		{"nats.decryptionSecret", n.DecryptionSecret}} {
		if !labelPattern.MatchString(field.value) {
			return refuse(Invalid, field.name, "expected a DNS label")
		}
	}
	return nil
}

// validateKeys checks the platform's two public identities: the stream controller and the console.
func (n NATS) validateKeys() error {
	for _, key := range []struct{ name, value string }{{"nats.controllerKey", n.ControllerKey},
		{"nats.consoleKey", n.ConsoleKey}} {
		if !nkeyPattern().MatchString(key.value) {
			return refuse(Invalid, key.name, "expected a user public nkey")
		}
	}
	return nil
}
