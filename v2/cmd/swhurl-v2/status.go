package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/s3"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/observe"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// readTimeout bounds one observation's requests: a server that does not answer leaves every fact
// unavailable, which is what the report then says.
const readTimeout = 20 * time.Second

// defaultMaxAge is how old a database instance's newest successful backup run may be: a daily job with
// room for one missed run. It is v1's BACKUP_MAX_AGE_HOURS default, and the operator can state another.
const defaultMaxAge = 26 * time.Hour

// status compares the cluster with what one instance's records say should run, and prints the observation: its
// state in text, or the v2-observation document with --json. It reads only, and a report is exit 0 whatever the
// state says.
func (c cli) status(r request) int {
	if r.all {
		return c.statusAll(r)
	}
	inputs, status := c.statusInputs(r)
	if status != 0 {
		return status
	}
	maxAge, status := c.maxAge(r)
	if status != 0 {
		return status
	}
	report, status := c.observeLive(r, inputs, maxAge)
	if status != 0 {
		return status
	}
	if !r.json {
		return emit(c.out, c.diagnostic, []byte(statusText(r.app, r.env, report)))
	}
	document, err := observe.EncodeReport(report)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	return emit(c.out, c.diagnostic, document)
}

// statusInputs reads one instance's records from the tree — its record pair and the app record — and the platform
// documents its compilation takes. An application and environment with no record is refused, not an I/O failure.
func (c cli) statusInputs(r request) (orchestration.Inputs, int) {
	folder := filepath.Join(r.records, r.app, r.env)
	if _, err := os.Stat(filepath.Join(folder, instanceFile)); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(c.diagnostic, "invalid: records: no instance record for %s/%s under %s\n", r.app, r.env, r.records)
		return orchestration.Inputs{}, 2
	}
	inputs := orchestration.Inputs{}
	for _, read := range []struct {
		path, what string
		target     *[]byte
	}{
		{filepath.Join(folder, instanceFile), "instance", &inputs.Instance},
		{filepath.Join(folder, releaseFile), "release", &inputs.Release},
		{filepath.Join(r.records, r.app, appFile), "app", &inputs.App},
		{r.profile, "profile", &inputs.Profile},
		{r.callFile, "calls", &inputs.Calls},
		{r.egressFile, "egress", &inputs.Egress},
	} {
		if read.path == "" {
			continue // an optional document the operator did not give
		}
		data, status := c.read(read.path, read.what)
		if status != 0 {
			return orchestration.Inputs{}, status
		}
		*read.target = data
	}
	return inputs, c.sameInstance(r, inputs.Instance)
}

// sameInstance refuses a record that names another instance than the one asked for, so a path the arguments
// reshape cannot report on something else.
func (c cli) sameInstance(r request, instance []byte) int {
	spec, err := kernel.DecodeInstance(instance)
	if err != nil {
		return fail(c.diagnostic, err)
	}
	if spec.Application != r.app || spec.Environment != r.env {
		return fail(c.diagnostic, fmt.Errorf("invalid: records: the record for %s/%s names %s/%s", r.app, r.env,
			spec.Application, spec.Environment))
	}
	return 0
}

// observeLive runs the status use case against the API server the credential names — and, when asked, the object
// store's archive listing — and returns the observation.
func (c cli) observeLive(r request, inputs orchestration.Inputs, maxAge time.Duration) (observe.Report, int) {
	request, status := c.liveStatusInputs(r, inputs.Profile, maxAge)
	if status != 0 {
		return observe.Report{}, status
	}
	request.Inputs = inputs
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	report, err := orchestration.Status(ctx, request)
	if err != nil {
		return observe.Report{}, fail(c.diagnostic, err)
	}
	return report, 0
}

// connection is the API server and credential status was given, and the name of that evidence: the kubeconfig's
// current context, or the pod's own ServiceAccount and its namespace.
func (c cli) connection(r request) (kube.Connection, string, int) {
	if r.inCluster {
		return c.serviceAccount()
	}
	kubeconfig, status := c.read(r.kubeconfig, "kubeconfig")
	if status != 0 {
		return kube.Connection{}, "", status
	}
	connection, err := kube.Connect(kubeconfig)
	if err != nil {
		return kube.Connection{}, "", fail(c.diagnostic, err)
	}
	return connection, connection.Context, 0
}

// serviceAccountDir is where the kubelet mounts a pod's ServiceAccount token, the cluster's CA and the pod's namespace.
const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// inCluster is where a pod finds its ServiceAccount: the folder the kubelet mounts it in, and the environment that
// names the API server's service.
type inCluster struct {
	dir    string
	getenv func(string) string
}

// podServiceAccount is the binary's own: the kubelet's mount and the process environment.
func podServiceAccount() inCluster {
	return inCluster{dir: serviceAccountDir, getenv: os.Getenv}
}

// serviceAccount assembles the connection the pod's ServiceAccount gives. Each part is read once, here: the kubelet
// rotates the token file, and every status is a new process. A missing part is named and refused.
func (c cli) serviceAccount() (kube.Connection, string, int) {
	var token, ca, namespace []byte
	for _, part := range []struct {
		name   string
		target *[]byte
	}{{"token", &token}, {"ca.crt", &ca}, {"namespace", &namespace}} {
		data, status := c.mounted(filepath.Join(c.inCluster.dir, part.name))
		if status != 0 {
			return kube.Connection{}, "", status
		}
		*part.target = data
	}
	host, port := c.inCluster.getenv("KUBERNETES_SERVICE_HOST"), c.inCluster.getenv("KUBERNETES_SERVICE_PORT")
	for _, variable := range [][2]string{{"KUBERNETES_SERVICE_HOST", host}, {"KUBERNETES_SERVICE_PORT", port}} {
		if variable[1] == "" {
			fmt.Fprintf(c.diagnostic, "invalid: in-cluster: %s is not set\n", variable[0])
			return kube.Connection{}, "", 2
		}
	}
	connection, err := kube.ServiceAccount(token, ca, host, port)
	if err != nil {
		return kube.Connection{}, "", fail(c.diagnostic, err)
	}
	return connection, "serviceaccount in " + strings.TrimSpace(string(namespace)), 0
}

// mounted is one file of the ServiceAccount's mount. A file that is not there, or holds nothing, is named: outside a
// pod, or with the token's automount turned off, that is the answer the operator needs.
func (c cli) mounted(path string) ([]byte, int) {
	data, err := c.readFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(c.diagnostic, "invalid: in-cluster: %s is not mounted\n", path)
		return nil, 2
	case err != nil:
		fmt.Fprintf(c.diagnostic, "io: in-cluster: cannot read %s\n", path)
		return nil, 1
	case len(bytes.TrimSpace(data)) == 0:
		fmt.Fprintf(c.diagnostic, "invalid: in-cluster: %s is empty\n", path)
		return nil, 2
	}
	return data, 0
}

// archiveLister lists the instance's own prefix in the object store: the credential is the operator's, the bucket
// and root are the platform profile's, and the prefix is the instance's namespace.
func (c cli) archiveLister(r request, profile []byte) func(context.Context, string) (observe.ArchiveList, error) {
	return func(ctx context.Context, namespace string) (observe.ArchiveList, error) {
		query, err := archiveQueryOf(profile, namespace)
		if err != nil {
			return observe.ArchiveList{}, err
		}
		reader, err := c.archives(query.Bucket, r.region)
		if err != nil {
			return observe.ArchiveList{}, err
		}
		return reader.List(ctx, query)
	}
}

// archiveQuery is the instance's own prefix: the profile's root and the instance's namespace.
func (c cli) archiveQuery(r request, target observe.Target) (observe.ArchiveQuery, int) {
	profile, status := c.read(r.profile, "profile")
	if status != 0 {
		return observe.ArchiveQuery{}, status
	}
	query, err := archiveQueryOf(profile, target.Namespace)
	if err != nil {
		return observe.ArchiveQuery{}, fail(c.diagnostic, err)
	}
	return query, 0
}

func archiveQueryOf(document []byte, namespace string) (observe.ArchiveQuery, error) {
	profile, err := kernel.DecodeProfile(document)
	if err != nil {
		return observe.ArchiveQuery{}, err
	}
	root, err := s3.ParseURL(profile.Backup.S3URI)
	if err != nil {
		return observe.ArchiveQuery{}, err
	}
	return observe.ArchiveQuery{Bucket: root.Bucket, Prefix: root.Prefix + namespace + "/"}, nil
}

// maxAge is the threshold the backup answer is judged by: the operator's, or the default.
func (c cli) maxAge(r request) (time.Duration, int) {
	if r.maxAge == "" {
		return defaultMaxAge, 0
	}
	stated, err := time.ParseDuration(r.maxAge)
	if err != nil || stated <= 0 {
		fmt.Fprint(c.diagnostic, usage)
		return 0, 2
	}
	return stated, 0
}

// statusText is the observation for a person: the state and what decided it, the revision and image the records
// expect, every finding, the route and its certificates, the containers that are not ready, and the protection.
// The --json document is the report; this is its reading, and never adds a fact.
func statusText(app, env string, report observe.Report) string {
	target := report.Target
	lines := []string{
		fmt.Sprintf("Instance   %s/%s (namespace %s)", app, env, target.Namespace),
		"State      " + stateText(report.State),
		fmt.Sprintf("Revision   %s, the commit the records were read at", target.Revision),
		"Applied    " + report.Currentness.AppliedRevision,
		"Current    " + outcomeText(report.Currentness.State, report.Currentness.Reason),
		"Image      " + target.Digest,
		fmt.Sprintf("Replicas   %d wanted", target.Replicas),
	}
	for i, finding := range report.Findings {
		lines = append(lines, labelled(i == 0, "Findings", fmt.Sprintf("%-14s %s", finding.Field, outcomeText(finding.Outcome, finding.Reason))))
	}
	lines = append(lines, routeLines(report)...)
	for i, problem := range report.Problems {
		lines = append(lines, labelled(i == 0, "Problems", strings.TrimSpace(fmt.Sprintf("%s/%s: %s %s",
			problem.Pod, problem.Container, problem.Reason, problem.Message))))
	}
	lines = append(lines, backupLines(report.Backup)...)
	lines = append(lines, fmt.Sprintf("Observed   %s at %s", report.Source, report.ObservedAt))
	return strings.Join(lines, "\n")
}

// labelled is one line of a section: the label on its first line, its indentation on the others.
func labelled(first bool, label, text string) string {
	if first {
		return fmt.Sprintf("%-10s %s", label, text)
	}
	return strings.Repeat(" ", 11) + text
}

func stateText(state observe.State) string {
	if state.Field == "" {
		return state.Name
	}
	return fmt.Sprintf("%s: %s %s", state.Name, state.Field, state.Reason)
}

func outcomeText(outcome, reason string) string {
	if reason == "" {
		return outcome
	}
	return outcome + "/" + reason
}

// routeLines is the route the records expect — each host it serves, or why it is not there — and each certificate.
func routeLines(report observe.Report) []string {
	if len(report.Routes) == 0 {
		return []string{"Route      none: the records expose no route"}
	}
	var lines []string
	for _, route := range report.Routes {
		if route.Outcome != observe.Present {
			lines = append(lines, fmt.Sprintf("Route      %s: %s", route.Name, outcomeText(route.Outcome, route.Reason)))
		}
		for _, host := range route.Hosts {
			lines = append(lines, "Route      https://"+host)
		}
	}
	for _, certificate := range report.Certificates {
		lines = append(lines, fmt.Sprintf("TLS        %s: %s", certificate.Name, outcomeText(certificate.Outcome, certificate.Reason)))
	}
	return lines
}

// backupLines is a database instance's protection and its archive; an instance that renders none has no lines.
func backupLines(backup observe.BackupReport) []string {
	if backup.State == observe.BackupNotApplicable {
		return nil
	}
	return []string{"Backup     " + outcomeText(backup.State, backup.Reason),
		"Archive    " + outcomeText(backup.Archive.State, backup.Archive.Reason)}
}
