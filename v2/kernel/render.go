package kernel

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// HTTPWorkload is supplied rendering evidence, not a complete or verified release.
type HTTPWorkload struct {
	Image, Digest string
	Port          int32
	HealthPath    string
	UID           int32
}

// HTTPInstance contains the environment-owned facts for an HTTP preview. An empty Exposure means
// private; any other value must be one the renderer supports. Calls and Callers are the connected pairs
// the relationship resolver established, so rendering never decides consent.
type HTTPInstance struct {
	Application, Environment string
	Configuration            Configuration
	Exposure                 string
	Calls                    []Call
	Callers                  []string
	Database                 string // "sqlite", or empty for an image that keeps no state
	Storage                  Storage
	Egress                   *Egress
}

// Egress is an instance's way out: its own port on the platform's proxy, and the hosts it may reach. The
// port identifies the instance to the proxy, so it is allocated and kept, never derived.
type Egress struct {
	Port   int32
	Grants []string
}

// Call is one connected callee: the app and the port its Service listens on.
type Call struct {
	Callee string
	Port   int32
}

// ResourceSet owns fresh resource bytes and slices, without release identities or readiness.
type ResourceSet struct {
	Files         []File
	Inventory     []Claim
	Registrations []string
}

// RenderHTTP builds resources from known facts without inventing release provenance.
// Validation order is names/environment, workload, configuration, profile, quota fit.
func RenderHTTP(spec HTTPInstance, http HTTPWorkload, profile PlatformProfile) (ResourceSet, error) {
	exposure, err := spec.validate(http, profile)
	if err != nil {
		return ResourceSet{}, err
	}
	// Instances under the instance root; units under the profile's unit root, which kustomize can list.
	folder := profile.Delivery.OutputRoot + "/instances/" + spec.Application + "/" + spec.Environment
	unitPath := profile.Delivery.UnitsRoot + "/" + unitName(spec) + ".json"
	resources := resourcesOf(spec, http, profile, exposure)
	var files []File
	var listed []any
	for _, resource := range resources {
		files = append(files, File{folder + "/" + resource.name, encodeManifest(resource.doc)})
		listed = append(listed, resource.name)
	}
	files = append(files,
		File{folder + "/kustomization.yaml", encodeManifest(manifest{
			"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization", "resources": listed})},
		File{unitPath, encodeManifest(unit(spec, profile, folder))})
	if spec.Database == "sqlite" {
		// Flux substitutes every reference in the folder, so a reference v2 did not mean to write would be
		// replaced or could not be resolved; nothing else may look like one.
		if err := substitutionProblem(files); err != nil {
			return ResourceSet{}, err
		}
	}
	return ResourceSet{files, inventory(spec), []string{unitPath}}, nil
}

func namespaceName(spec HTTPInstance) string { return spec.Application + "-" + spec.Environment }
func unitName(spec HTTPInstance) string      { return "app-" + namespaceName(spec) }
func owner(spec HTTPInstance) string         { return spec.Application + "/" + spec.Environment }

func metadata(name, namespace string) manifest {
	meta := manifest{"name": name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	return meta
}

func namespaceManifest(spec HTTPInstance) manifest {
	meta := metadata(namespaceName(spec), "")
	meta["labels"] = manifest{labelDomain + "managed": "true", labelDomain + "app": spec.Application,
		labelDomain + "environment": spec.Environment, labelDomain + "exposure": exposureLabel(spec)}
	if spec.Database != "" {
		// Deleting the instance's files must not delete its data: Flux keeps the namespace, and the
		// retained volume class keeps the claim's data. Deleting either is a separate, deliberate action.
		meta["annotations"] = manifest{"kustomize.toolkit.fluxcd.io/prune": "disabled"}
	}
	return manifest{"apiVersion": "v1", "kind": "Namespace", "metadata": meta}
}

// exposureLabel is the instance's exposure as a label, private when it states none.
func exposureLabel(spec HTTPInstance) string {
	if spec.Exposure == "" {
		return "private"
	}
	return spec.Exposure
}

func helmRelease(spec HTTPInstance, http HTTPWorkload, profile PlatformProfile) manifest {
	chart := manifest{"chart": profile.Chart.Name, "version": profile.Chart.Version, "interval": helmInterval,
		"sourceRef": manifest{"kind": "HelmRepository", "name": profile.Chart.Repository, "namespace": profile.Chart.RepositoryNamespace}}
	remediation := manifest{"remediation": manifest{"retries": retries}}
	meta := metadata(spec.Application, namespaceName(spec))
	if spec.Egress != nil && len(spec.Egress.Grants) > 0 {
		// What the proxy may read from its log for this instance: the hosts the instance declares it needs.
		meta["annotations"] = manifest{labelDomain + "egress": strings.Join(spec.Egress.Grants, ",")}
	}
	return manifest{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": meta,
		"spec": manifest{"interval": helmInterval, "timeout": failAfter, "chart": manifest{"spec": chart},
			"install": remediation, "upgrade": manifest{"remediation": manifest{"retries": retries}},
			"values": values(spec, http, profile.Workload, profile.Storage.Class, profile.Egress.Address)}}
}

// values are the chart's values: one controller, one container, its Service and /tmp.
func values(spec HTTPInstance, http HTTPWorkload, workload Workload, storageClass, egressAddress string) manifest {
	uid := http.UID
	pod := manifest{"automountServiceAccountToken": false, "securityContext": manifest{
		"runAsNonRoot": true, "runAsUser": uid, "runAsGroup": uid, "fsGroup": uid,
		"seccompProfile": manifest{"type": "RuntimeDefault"}}}
	controller := manifest{"replicas": spec.Configuration.Replicas, "containers": manifest{"main": container(spec, http, workload, egressAddress)}}
	if spec.Database == "sqlite" {
		// One pod at a time: a rolling update would open one database file twice.
		controller["strategy"] = "Recreate"
	}
	persistence := manifest{"tmp": manifest{"type": "emptyDir", "globalMounts": []any{manifest{"path": "/tmp"}}}}
	if spec.Database == "sqlite" {
		persistence["data"] = dataVolume(spec.Storage, storageClass)
	}
	if len(spec.Calls) > 0 {
		persistence["tokens"] = tokensPersistence(spec.Calls)
	}
	return manifest{"defaultPodOptions": pod, "controllers": manifest{"main": controller},
		"persistence": persistence,
		"service": manifest{"main": manifest{"controller": "main",
			"ports": manifest{"http": manifest{"port": http.Port}}}}}
}

func container(spec HTTPInstance, http HTTPWorkload, workload Workload, egressAddress string) manifest {
	memory := fmt.Sprintf("%dMi", spec.Configuration.MemoryMiB)
	env := manifest{"SWHURL_APP": spec.Application, "SWHURL_ENV": spec.Environment}
	if spec.Database == "sqlite" {
		env["DATABASE_PATH"] = databasePath
	}
	if spec.Egress != nil {
		// Every client library must use the proxy, and the JVM keeps its own list.
		address := fmt.Sprintf("http://%s:%d", egressAddress, spec.Egress.Port)
		env["HOST_IP"] = manifest{"valueFrom": manifest{"fieldRef": manifest{"fieldPath": "status.hostIP"}}}
		env["HTTP_PROXY"] = address
		env["HTTPS_PROXY"] = address
		env["NODE_USE_ENV_PROXY"] = "1"
		env["NO_PROXY"] = "localhost,127.0.0.1,.svc,.cluster.local,$(HOST_IP)"
		env["JAVA_TOOL_OPTIONS"] = fmt.Sprintf("-Dhttp.proxyHost=%s -Dhttp.proxyPort=%d -Dhttps.proxyHost=%s -Dhttps.proxyPort=%d -Dhttp.nonProxyHosts=localhost|127.*|*.svc|*.cluster.local|$(HOST_IP)",
			egressAddress, spec.Egress.Port, egressAddress, spec.Egress.Port)
	}
	for _, call := range spec.Calls {
		env[callVariable(call.Callee)] = calleeAddress(call, spec.Environment)
	}
	container := manifest{
		"image": manifest{"repository": http.Image, "digest": http.Digest},
		"resources": manifest{"requests": manifest{"cpu": fmt.Sprintf("%dm", workload.CPURequestMilli), "memory": memory},
			"limits": manifest{"memory": memory}},
		"securityContext": manifest{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
			"capabilities": manifest{"drop": []any{"ALL"}}},
		"env":    env,
		"probes": probes(http, workload),
	}
	if len(spec.Callers) > 0 {
		callers := slices.Clone(spec.Callers)
		slices.Sort(callers)
		env["SWHURL_ALLOWED_CALLERS"] = strings.Join(callers, ",")
		// The keys come from the platform's own ConfigMap in this namespace, not from the values.
		container["envFrom"] = []any{manifest{"configMapRef": manifest{"name": spec.Application + "-auth"}}}
	}
	return container
}

// callVariable is the variable an app reads one callee's address from: the callee's name in the
// platform's spelling.
func callVariable(callee string) string {
	return "SWHURL_SERVICE_" + strings.ToUpper(strings.ReplaceAll(callee, "-", "_")) + "_URL"
}

// calleeAddress is the callee's Service in the caller's own environment. It is rendered in full rather
// than through the kubelet's env expansion, so the value says what it is.
func calleeAddress(call Call, environment string) string {
	return fmt.Sprintf("http://%s.%s-%s.svc.cluster.local:%d", call.Callee, call.Callee, environment, call.Port)
}

// tokensPersistence is one projected volume carrying a service-account token per callee, each issued for
// that callee's audience only and renewed by the kubelet before it expires.
func tokensPersistence(calls []Call) manifest {
	sources := make([]any, 0, len(calls))
	for _, call := range calls {
		sources = append(sources, manifest{"serviceAccountToken": manifest{
			"audience": call.Callee, "expirationSeconds": 600, "path": call.Callee + "/token"}})
	}
	return manifest{"type": "projected", "sources": sources,
		"globalMounts": []any{manifest{"path": "/var/run/swhurl/tokens", "readOnly": true}}}
}

// probes: liveness waits until the app first answers, for up to the profile's startup allowance.
func probes(runtime HTTPWorkload, workload Workload) manifest {
	probe := func(extra manifest) manifest {
		spec := manifest{"httpGet": manifest{"path": runtime.HealthPath, "port": runtime.Port}}
		for key, value := range extra {
			spec[key] = value
		}
		return manifest{"enabled": true, "custom": true, "spec": spec}
	}
	return manifest{"readiness": probe(nil), "liveness": probe(nil), "startup": probe(manifest{
		"periodSeconds":    workload.StartupPeriodSeconds,
		"failureThreshold": workload.StartupSeconds / workload.StartupPeriodSeconds})}
}

func resourceQuota(spec HTTPInstance, quota Quota) manifest {
	return manifest{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": metadata(spec.Application, namespaceName(spec)),
		"spec": manifest{"hard": manifest{
			"pods": fmt.Sprint(quota.Pods), "requests.cpu": cpuQuantity(quota.RequestsCPUMilli),
			"requests.memory": memoryQuantity(quota.RequestsMemoryMiB), "limits.memory": memoryQuantity(quota.LimitsMemoryMiB)}}}
}

func cpuQuantity(milli int32) string {
	if milli%1000 == 0 {
		return fmt.Sprint(milli / 1000)
	}
	return fmt.Sprintf("%dm", milli)
}

func memoryQuantity(mib int32) string {
	if mib%1024 == 0 {
		return fmt.Sprintf("%dGi", mib/1024)
	}
	return fmt.Sprintf("%dMi", mib)
}

// networkPolicy is the isolation boundary: a private instance accepts no ingress, and an exposed one
// accepts only its ingress controller's own pods on its port. By the operator's decision of
// 25 October 2026 no instance sends egress, and an empty egress rule list denies all of it.
func networkPolicy(spec HTTPInstance, exposure string, http HTTPWorkload, profile PlatformProfile) manifest {
	ingressRules := []any{}
	if exposure == "authenticated-web" {
		labels := manifest{}
		for key, value := range profile.Routing.IngressLabels {
			labels[key] = value
		}
		ingressRules = append(ingressRules, manifest{
			"from": []any{manifest{
				"namespaceSelector": manifest{"matchLabels": manifest{"kubernetes.io/metadata.name": profile.Routing.IngressNamespace}},
				"podSelector":       manifest{"matchLabels": labels},
			}},
			"ports": []any{manifest{"port": http.Port, "protocol": "TCP"}},
		})
	}
	// A callee admits each connected caller's own pods on its port; a caller reaches each connected
	// callee's pods on the callee's port and nothing else. Egress stays denied everywhere else.
	for _, caller := range spec.Callers {
		ingressRules = append(ingressRules, manifest{
			"from":  []any{peerSelector(caller, spec.Environment)},
			"ports": []any{manifest{"port": http.Port, "protocol": "TCP"}},
		})
	}
	egressRules := []any{}
	for _, call := range spec.Calls {
		egressRules = append(egressRules, manifest{
			"to":    []any{peerSelector(call.Callee, spec.Environment)},
			"ports": []any{manifest{"port": call.Port, "protocol": "TCP"}},
		})
	}
	// Anything that leaves the pod's own namespace needs a name resolved first, and a proxy port is the
	// only way out: both are admitted narrowly, and nothing else is.
	if len(spec.Calls) > 0 || spec.Egress != nil {
		egressRules = append(egressRules, manifest{
			"to": []any{manifest{
				"namespaceSelector": manifest{"matchLabels": manifest{"kubernetes.io/metadata.name": profile.DNS.Namespace}},
				"podSelector":       manifest{"matchLabels": labelManifest(profile.DNS.Labels)},
			}},
			"ports": []any{manifest{"port": 53, "protocol": "UDP"}, manifest{"port": 53, "protocol": "TCP"}},
		})
	}
	if spec.Egress != nil {
		egressRules = append(egressRules, manifest{
			"to": []any{manifest{
				"namespaceSelector": manifest{"matchLabels": manifest{"kubernetes.io/metadata.name": profile.Egress.Namespace}},
				"podSelector":       manifest{"matchLabels": labelManifest(profile.Egress.Labels)},
			}},
			"ports": []any{manifest{"port": spec.Egress.Port, "protocol": "TCP"}},
		})
	}
	return manifest{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": metadata(spec.Application, namespaceName(spec)),
		"spec": manifest{"podSelector": manifest{"matchLabels": manifest{instanceLabel: spec.Application}},
			"policyTypes": []any{"Ingress", "Egress"}, "ingress": ingressRules, "egress": egressRules}}
}

// labelManifest is a label set as the map a selector needs.
func labelManifest(labels map[string]string) manifest {
	out := manifest{}
	for key, value := range labels {
		out[key] = value
	}
	return out
}

// peerSelector is one instance's own pods in its own environment: the only peer a call or a consent
// needs. The namespace carries the environment, so staging never reaches production.
func peerSelector(application, environment string) manifest {
	return manifest{
		"namespaceSelector": manifest{"matchLabels": manifest{"kubernetes.io/metadata.name": application + "-" + environment}},
		"podSelector":       manifest{"matchLabels": manifest{instanceLabel: application}},
	}
}

// ingress is the route: a host derived from the platform's base domain, TLS from the platform's issuer and
// the shared sign-in middleware. The chart keeps its own ingress off; this object is the route.
func ingress(spec HTTPInstance, http HTTPWorkload, profile PlatformProfile) manifest {
	host := routeHost(spec, profile.Routing)
	route := profile.Routing
	return manifest{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": manifest{"name": spec.Application, "namespace": namespaceName(spec),
			"annotations": manifest{"cert-manager.io/cluster-issuer": route.Issuer,
				"traefik.ingress.kubernetes.io/router.middlewares": route.Middleware}},
		"spec": manifest{"ingressClassName": route.Class,
			"rules": []any{manifest{"host": host, "http": manifest{"paths": []any{manifest{"path": "/", "pathType": "Prefix",
				"backend": manifest{"service": manifest{"name": spec.Application, "port": manifest{"number": http.Port}}}}}}}},
			"tls": []any{manifest{"hosts": []any{host}, "secretName": spec.Application + "-tls"}}}}
}

// routeHost derives the host name the way the platform states it: the environment, the app and the base
// domain. No instance stores a host, so two instances cannot disagree about one name's shape.
func routeHost(spec HTTPInstance, routing Routing) string {
	return spec.Environment + "-" + spec.Application + "." + routing.BaseDomain
}

// configured is whether the profile states routing at all.
func (r Routing) configured() bool { return r.BaseDomain != "" }

func unit(spec HTTPInstance, profile PlatformProfile, folder string) manifest {
	dependsOn := []any{}
	for _, name := range profile.Delivery.DependsOn {
		dependsOn = append(dependsOn, manifest{"name": name})
	}
	specification := manifest{"dependsOn": dependsOn, "interval": unitInterval,
		"sourceRef": manifest{"kind": "GitRepository", "name": profile.Delivery.Source},
		"path":      "./" + folder, "prune": true, "wait": true, "timeout": failAfter}
	if spec.Database == "sqlite" {
		// The backup job reads a key from the platform's own unit, so that unit must exist first, and the
		// file's references are substituted from its Secret. Substitution is one switch for every document
		// in the folder, which is why the compiler refuses any other reference (see substitutionProblem).
		specification["dependsOn"] = append(dependsOn, manifest{"name": backupUnit})
		specification["postBuild"] = manifest{"substituteFrom": []any{manifest{
			"kind": backupSubstitutionKind, "name": profile.Backup.WriterName, "optional": false}}}
	}
	return manifest{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": metadata(unitName(spec), "flux-system"), "spec": specification}
}

// validate runs every check the renderer makes, in a fixed order, and returns the exposure the files are
// rendered for.
func (spec HTTPInstance) validate(http HTTPWorkload, profile PlatformProfile) (string, error) {
	if err := validateNames(spec.Application, spec.Environment); err != nil {
		return "", err
	}
	if err := validateImage(http.Image, http.Digest); err != nil {
		return "", err
	}
	if err := validateHTTP(http.Port, http.HealthPath, http.UID); err != nil {
		return "", err
	}
	exposure, err := exposureOf(spec.Exposure, profile)
	if err != nil {
		return "", err
	}
	if err := spec.Configuration.validate(); err != nil {
		return "", err
	}
	if err := profile.validate(); err != nil {
		return "", err
	}
	if err := fits(spec.Configuration, profile, spec.Database == "sqlite"); err != nil {
		return "", err
	}
	if err := spec.validateResources(profile); err != nil {
		return "", err
	}
	return exposure, spec.validateEgress(profile)
}

// exposureOf is the exposure the files are rendered for: absent means private, and a route needs the
// platform's routing facts before one can be rendered.
func exposureOf(stated string, profile PlatformProfile) (string, error) {
	if stated == "" {
		return "private", nil
	}
	if stated != "private" && stated != "authenticated-web" {
		return "", refuse(Unsupported, "grants.exposure", "only private and authenticated-web exposure are supported")
	}
	if stated == "authenticated-web" && !profile.Routing.configured() {
		return "", refuse(Unsupported, "routing", "the profile states no routing, so no route can be rendered")
	}
	return stated, nil
}

// resourcesOf is the instance's files in their fixed order: the route, when there is one, after the policy
// and before the kustomization that lists them.
func resourcesOf(spec HTTPInstance, http HTTPWorkload, profile PlatformProfile, exposure string) []struct {
	name string
	doc  manifest
} {
	resources := []struct {
		name string
		doc  manifest
	}{
		{"namespace.json", namespaceManifest(spec)},
		{"helmrelease.json", helmRelease(spec, http, profile)},
		{"resourcequota.json", resourceQuota(spec, profile.Quota)},
		{"networkpolicy.json", networkPolicy(spec, exposure, http, profile)},
	}
	if exposure != "private" {
		resources = append(resources, struct {
			name string
			doc  manifest
		}{"ingress.json", ingress(spec, http, profile)})
	}
	if len(spec.Callers) > 0 {
		resources = append(resources, struct {
			name string
			doc  manifest
		}{"auth.json", authConfigMap(spec, profile.Auth)})
	}
	if spec.Database == "sqlite" {
		// A database is the platform's to protect: the job, the reference to the upload key it reads,
		// and a policy for the pod that reaches S3.
		resources = append(resources,
			struct {
				name string
				doc  manifest
			}{"backup-secret.json", backupSecret(spec)},
			struct {
				name string
				doc  manifest
			}{"backup-cronjob.json", backupCronJob(spec, http, profile)},
			struct {
				name string
				doc  manifest
			}{"backup-networkpolicy.json", backupPolicy(spec, profile)})
	}
	return resources
}

// The backup job's own resources: it copies, encrypts and uploads, and needs nothing else.
const (
	backupCPURequestMilli = 10
	backupMemoryMiB       = 64
	backupLimitMiB        = 256
	backupFirstMinute     = 35 // 03:35 UTC: after the MongoDB job at 03:30
	backupMinutes         = 50 // a minute hash into 03:35–04:24, so instances do not all start at once
	backupScratch         = "1Gi"
	backupDeadlineSeconds = 900
	// The job's Secret holds the upload-only key under the names the AWS CLI reads, as references Flux
	// substitutes from the platform's own Secret (whose keys are the BACKUP_ names).
	backupKeyVariable      = "AWS_ACCESS_KEY_ID"
	backupSecretVariable   = "AWS_SECRET_ACCESS_KEY"
	backupKeyReference     = "${BACKUP_AWS_ACCESS_KEY_ID}"
	backupSecretReference  = "${BACKUP_AWS_SECRET_ACCESS_KEY}"
	backupSubstitutionKind = "Secret"
)

// backupSecret is the job's Secret: the upload-only key, as references the instance's unit substitutes
// from the platform's own Secret. The key itself never reaches v2, Git or this document.
func backupSecret(spec HTTPInstance) manifest {
	return manifest{"apiVersion": "v1", "kind": "Secret",
		"metadata":   metadata(backupName(spec), namespaceName(spec)),
		"type":       "Opaque",
		"stringData": manifest{backupKeyVariable: backupKeyReference, backupSecretVariable: backupSecretReference}}
}

// backupCronJob is the nightly job: SQLite's online backup, age's public-key encryption, and an upload
// through the instance's own proxy port. It runs as the app's user on the app's claim, so the data it
// reads is the data the app wrote.
func backupCronJob(spec HTTPInstance, http HTTPWorkload, profile PlatformProfile) manifest {
	name := backupName(spec)
	container := manifest{
		"name": "backup", "image": profile.Backup.Image + "@" + profile.Backup.Digest, "args": []any{"backup-job-sqlite"},
		"env":     backupEnv(spec, profile),
		"envFrom": []any{manifest{"secretRef": manifest{"name": name}}},
		"resources": manifest{"requests": manifest{"cpu": fmt.Sprintf("%dm", backupCPURequestMilli), "memory": fmt.Sprintf("%dMi", backupMemoryMiB)},
			"limits": manifest{"memory": fmt.Sprintf("%dMi", backupLimitMiB)}},
		"securityContext": manifest{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
			"capabilities": manifest{"drop": []any{"ALL"}}},
		"volumeMounts": []any{manifest{"name": "data", "mountPath": dataMount},
			manifest{"name": "scratch", "mountPath": "/scratch"}, manifest{"name": "tmp", "mountPath": "/tmp"}},
	}
	uid := http.UID
	pod := manifest{
		"metadata": manifest{"labels": manifest{backupLabel: "true"}},
		"spec": manifest{"restartPolicy": "Never", "automountServiceAccountToken": false,
			"securityContext": manifest{"runAsNonRoot": true, "runAsUser": uid, "runAsGroup": uid, "fsGroup": uid,
				"seccompProfile": manifest{"type": "RuntimeDefault"}},
			"containers": []any{container},
			// The app's own claim: one node, so a second pod on it is admitted while the app runs.
			"volumes": []any{
				manifest{"name": "data", "persistentVolumeClaim": manifest{"claimName": claimName(spec)}},
				manifest{"name": "scratch", "emptyDir": manifest{"sizeLimit": backupScratch}},
				manifest{"name": "tmp", "emptyDir": manifest{}}}}}
	return manifest{"apiVersion": "batch/v1", "kind": "CronJob",
		"metadata": manifest{"name": name, "namespace": namespaceName(spec), "labels": manifest{backupLabel: "true"}},
		"spec": manifest{"schedule": backupSchedule(namespaceName(spec)), "timeZone": "Etc/UTC",
			"suspend": profile.Backup.Suspended, "concurrencyPolicy": "Forbid", "startingDeadlineSeconds": 3600,
			"successfulJobsHistoryLimit": 1, "failedJobsHistoryLimit": 3,
			"jobTemplate": manifest{"spec": manifest{"backoffLimit": 2, "activeDeadlineSeconds": backupDeadlineSeconds,
				"template": pod}}}}
}

// backupEnv is what the job reads: how to reach the proxy, where to publish, and which database file to
// copy. $(HOST_IP) is the kubelet's own expansion of the field reference above it, not Flux's.
func backupEnv(spec HTTPInstance, profile PlatformProfile) []any {
	env := []any{manifest{"name": "HOST_IP", "valueFrom": manifest{"fieldRef": manifest{"fieldPath": "status.hostIP"}}}}
	if spec.Egress != nil {
		address := fmt.Sprintf("http://%s:%d", profile.Egress.Address, spec.Egress.Port)
		env = append(env,
			manifest{"name": "HTTP_PROXY", "value": address},
			manifest{"name": "HTTPS_PROXY", "value": address},
			manifest{"name": "NO_PROXY", "value": "localhost,127.0.0.1,.svc,.cluster.local,$(HOST_IP)"})
	}
	return append(env,
		manifest{"name": "AGE_RECIPIENT", "value": profile.Backup.Recipient},
		manifest{"name": "BACKUP_S3_URI", "value": profile.Backup.S3URI + namespaceName(spec) + "/"},
		manifest{"name": "BACKUP_SOURCE", "value": namespaceName(spec) + "/" + spec.Application},
		manifest{"name": "DATABASE_PATH", "value": databasePath})
}

// backupPolicy admits nothing to the job's pod and lets it reach only the name service and the instance's
// own proxy port. The app's policy selects the app's pods, so without this the pod that must leave the
// cluster would be the one pod with unrestricted egress.
func backupPolicy(spec HTTPInstance, profile PlatformProfile) manifest {
	egress := []any{manifest{
		"to": []any{manifest{
			"namespaceSelector": manifest{"matchLabels": manifest{"kubernetes.io/metadata.name": profile.DNS.Namespace}},
			"podSelector":       manifest{"matchLabels": labelManifest(profile.DNS.Labels)}}},
		"ports": []any{manifest{"port": 53, "protocol": "UDP"}, manifest{"port": 53, "protocol": "TCP"}}}}
	if spec.Egress != nil {
		egress = append(egress, manifest{
			"to": []any{manifest{
				"namespaceSelector": manifest{"matchLabels": manifest{"kubernetes.io/metadata.name": profile.Egress.Namespace}},
				"podSelector":       manifest{"matchLabels": labelManifest(profile.Egress.Labels)}}},
			"ports": []any{manifest{"port": spec.Egress.Port, "protocol": "TCP"}}})
	}
	return manifest{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": metadata(backupName(spec), namespaceName(spec)),
		"spec": manifest{"podSelector": manifest{"matchLabels": manifest{backupLabel: "true"}},
			"policyTypes": []any{"Ingress", "Egress"}, "ingress": []any{}, "egress": egress}}
}

// backupName is the job, its Secret and its policy: one name per instance's database.
func backupName(spec HTTPInstance) string { return spec.Application + "-backup" }

// backupSchedule is the minute a namespace's backup runs at, derived from its name so it is stable and
// spread: the same rule v1 uses, so a migrated instance keeps the slot its logs name.
func backupSchedule(namespace string) string {
	sum := sha256.Sum256([]byte(namespace))
	remainder := 0
	for _, b := range sum {
		remainder = (remainder*256 + int(b)) % backupMinutes
	}
	minute := backupFirstMinute + remainder
	return fmt.Sprintf("%d %d * * *", minute%60, 3+minute/60)
}

// validateEgress checks an allocated way out: a port in the range the platform proxies on, and a proxy
// the profile states.
func (spec HTTPInstance) validateEgress(profile PlatformProfile) error {
	if spec.Egress == nil {
		return nil
	}
	if spec.Egress.Port < 1024 || spec.Egress.Port > 65535 {
		return refuse(Invalid, "egress.port", "expected an unprivileged port")
	}
	if profile.Egress.Address == "" {
		return refuse(Unsupported, "egress", "the profile states no proxy")
	}
	return nil
}

// validateResources checks what an instance adds to its workload: the pairs it takes part in and the volume
// it asks for.
func (spec HTTPInstance) validateResources(profile PlatformProfile) error {
	if err := validateCalls(spec, profile); err != nil {
		return err
	}
	return spec.validateStorage(profile)
}

// validateStorage checks the volume an instance asks for against what its image needs and what the
// platform provides. A database admits one writer, so more than one replica is refused rather than
// silently reduced, and a size with nothing to put in it is refused too.
func (spec HTTPInstance) validateStorage(profile PlatformProfile) error {
	switch {
	case spec.Database == "" && (spec.Storage.SizeMiB > 0 || spec.Storage.ClaimName != ""):
		return refuse(Invalid, "storage", "the release keeps no database, so a volume would hold nothing")
	case spec.Database == "":
		return nil
	case spec.Database != "sqlite":
		return refuse(Unsupported, "database", "only sqlite is supported")
	}
	if err := spec.validateVolume(profile); err != nil {
		return err
	}
	if spec.Configuration.Replicas > 1 {
		return refuse(Invalid, "configuration.replicas", "a database admits one writer")
	}
	// A database is the platform's to protect: without the backup facts nothing could render a job, and
	// without an allocation the job's pod (which may reach only DNS and the proxy) could not upload.
	if profile.Backup.absent() {
		return refuse(Unsupported, "backup", "the profile states no backup")
	}
	if spec.Egress == nil {
		return refuse(Unsupported, "backup.egress", "a backup must leave the cluster through the proxy, so the instance needs an allocation")
	}
	return nil
}

// validateVolume checks the volume a database asks for: a size to create or the claim of the volume it keeps,
// never both, and a class only where something is created.
func (spec HTTPInstance) validateVolume(profile PlatformProfile) error {
	if spec.Storage.SizeMiB > 0 && spec.Storage.ClaimName != "" {
		return refuse(Invalid, "storage", "a volume is either created from a size or adopted from its claim, not both")
	}
	if spec.Storage.SizeMiB < 1 && spec.Storage.ClaimName == "" {
		return refuse(Invalid, "storage.sizeMiB", "a database needs a volume size, or the claim of the one it keeps")
	}
	if profile.Storage.Class == "" && spec.Storage.ClaimName == "" {
		return refuse(Unsupported, "storage.class", "the profile states no storage class")
	}
	return nil
}

// dataVolume is the instance's volume as the chart takes it: a claim it creates from the platform's class,
// or the claim of a volume that already exists — a migrated instance's data, adopted rather than replaced.
func dataVolume(storage Storage, storageClass string) manifest {
	mounts := []any{manifest{"path": dataMount}}
	if storage.ClaimName != "" {
		return manifest{"type": "persistentVolumeClaim", "existingClaim": storage.ClaimName, "globalMounts": mounts}
	}
	return manifest{"type": "persistentVolumeClaim", "storageClass": storageClass, "accessMode": "ReadWriteOnce",
		"size": fmt.Sprintf("%dMi", storage.SizeMiB), "retain": true, "globalMounts": mounts}
}

// claimName is the volume an instance's data lives on: the one it creates, named after itself, or the one
// it adopted.
func claimName(spec HTTPInstance) string {
	if spec.Storage.ClaimName != "" {
		return spec.Storage.ClaimName
	}
	return spec.Application
}

// validateCalls checks the resolved pairs' shape. Consent is not decided here: the caller supplies only
// connected pairs, and a callee needs the platform's keys to accept any of them.
func validateCalls(spec HTTPInstance, profile PlatformProfile) error {
	for _, call := range spec.Calls {
		if !namePattern.MatchString(call.Callee) || call.Port < 1 || call.Port > 65535 {
			return refuse(Invalid, "calls", "expected an app name and a port")
		}
	}
	for _, caller := range spec.Callers {
		if !namePattern.MatchString(caller) {
			return refuse(Invalid, "callers", "expected an app name")
		}
	}
	if len(spec.Callers) > 0 && !profile.Auth.configured() {
		return refuse(Unsupported, "auth", "the profile states no signing keys, so a callee cannot verify a caller")
	}
	return nil
}

// configured is whether the profile states what a callee needs to verify a token.
func (a Auth) configured() bool { return a.Issuer != "" && a.Keys != "" }

// authConfigMap carries the platform's token verification facts into one instance's namespace. A pod
// cannot read another namespace's ConfigMap, so each callee gets its own copy of the same platform data;
// the HelmRelease consumes it with envFrom rather than embedding the keys.
func authConfigMap(spec HTTPInstance, auth Auth) manifest {
	return manifest{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": metadata(spec.Application+"-auth", namespaceName(spec)),
		"data":     manifest{"SWHURL_AUTH_ISSUER": auth.Issuer, "SWHURL_AUTH_KEYS": auth.Keys}}
}

// substitutionProblem refuses a rendered file holding a Flux reference other than the backup's own two.
// Substitution is switched on for every document in a database instance's folder, so a reference v2 did
// not write on purpose would be replaced too, or fail to resolve: one document, one refusal.
func substitutionProblem(files []File) error {
	allowed := []string{backupKeyReference, backupSecretReference}
	for _, file := range files {
		rest := string(file.Document)
		for {
			start := strings.Index(rest, "${")
			if start < 0 {
				break
			}
			rest = rest[start:]
			if !slices.ContainsFunc(allowed, func(reference string) bool { return strings.HasPrefix(rest, reference) }) {
				return refuse(Invalid, "backup", "a document of a substituted instance may contain only the backup key references")
			}
			rest = rest[len("${"):]
		}
	}
	return nil
}

// inventory is every cluster object the files create, in file order.
func inventory(spec HTTPInstance) []Claim {
	namespace, by := namespaceName(spec), owner(spec)
	claims := []Claim{
		{Object{"", "Namespace", "", namespace}, by},
		{Object{"helm.toolkit.fluxcd.io", "HelmRelease", namespace, spec.Application}, by},
		{Object{"", "ResourceQuota", namespace, spec.Application}, by},
		{Object{"networking.k8s.io", "NetworkPolicy", namespace, spec.Application}, by},
	}
	if spec.Exposure == "authenticated-web" {
		claims = append(claims, Claim{Object{"networking.k8s.io", "Ingress", namespace, spec.Application}, by})
	}
	if len(spec.Callers) > 0 {
		claims = append(claims, Claim{Object{"", "ConfigMap", namespace, spec.Application + "-auth"}, by})
	}
	if spec.Database == "sqlite" {
		name := backupName(spec)
		claims = append(claims,
			Claim{Object{"", "Secret", namespace, name}, by},
			Claim{Object{"batch", "CronJob", namespace, name}, by},
			Claim{Object{"networking.k8s.io", "NetworkPolicy", namespace, name}, by})
	}
	return append(claims, Claim{Object{"kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", unitName(spec)}, by})
}

// encodeManifest is a document's canonical bytes: compact, keys sorted, no HTML escaping.
func encodeManifest(doc manifest) json.RawMessage {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	// Only maps, slices, strings, booleans and int32s built above reach here.
	if err := encoder.Encode(doc); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
}
