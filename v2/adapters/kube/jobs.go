package kube

import (
	"context"
	"slices"
	"strconv"

	"github.com/samclement/swhurl-platform/v2/observe"
)

// JobRecords is the console's record of its jobs, kept in one ConfigMap so that a pod which replaces the one that ran
// a job still shows what it did and can tell a job was cut short. One key per job, written by the pod that runs it;
// a key the writer no longer keeps is deleted in the same patch, so two pods writing at once cannot drop each
// other's. It is the store the job registry is given: the record is best effort, and the jobs use case never fails a
// job because its record could not be written.
//
// The namespace, name and field manager default to the console's own (platform/console/jobs-state.yaml), which is the
// only ConfigMap this adapter writes data into.
type JobRecords struct {
	Data         Data
	Pods         Reader // the pods that may still be running a job
	Namespace    string
	Name         string
	FieldManager string
}

const (
	jobRecordsNamespace    = "console"
	jobRecordsName         = "console-jobs"
	jobRecordsFieldManager = "console-jobs"
	jobKeyPrefix           = "job-"
)

// Load is every record the ConfigMap holds, in key order; a ConfigMap with no data is no records, and no error.
func (r JobRecords) Load(ctx context.Context) ([]string, error) {
	if r.Data == nil {
		return nil, nil
	}
	found, err := r.Data.GetConfigMap(ctx, r.namespace(), r.name())
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(found.Data))
	for key := range found.Data {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	records := make([]string, 0, len(keys))
	for _, key := range keys {
		records = append(records, found.Data[key])
	}
	return records, nil
}

// Save writes each record in one merge patch, deleting the ones whose value is nil.
func (r JobRecords) Save(ctx context.Context, records map[int]*string) error {
	if r.Data == nil {
		return nil
	}
	data := make(map[string]*string, len(records))
	for id, record := range records {
		data[jobKey(id)] = record
	}
	return r.Data.SetConfigMapKeys(ctx, r.namespace(), r.name(), data, r.fieldManager())
}

// Alive is whether the pod that ran a job still exists. An unreadable answer is an error, which the registry counts as
// alive: a job is called interrupted only when its pod is known to be gone.
func (r JobRecords) Alive(ctx context.Context, owner string) (bool, error) {
	if r.Pods == nil {
		return false, &Error{RefusedRequest, "pod", "reader_unavailable"}
	}
	read, err := r.Pods.Read(ctx, observe.Ref{APIVersion: "v1", Kind: "Pod", Namespace: r.namespace(), Name: owner})
	if err != nil {
		return false, err
	}
	return read.Status != observe.Absent, nil
}

func (r JobRecords) namespace() string {
	if r.Namespace != "" {
		return r.Namespace
	}
	return jobRecordsNamespace
}

func (r JobRecords) name() string {
	if r.Name != "" {
		return r.Name
	}
	return jobRecordsName
}

func (r JobRecords) fieldManager() string {
	if r.FieldManager != "" {
		return r.FieldManager
	}
	return jobRecordsFieldManager
}

// jobKey is one job's key in the ConfigMap.
func jobKey(id int) string {
	return jobKeyPrefix + strconv.Itoa(id)
}
