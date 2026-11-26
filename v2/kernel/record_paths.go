package kernel

import "strings"

// Record names belong to the writer vocabulary, shared by readers and revision relevance.
const (
	AppRecordName      = "app.json"
	InstanceRecordName = "instance.json"
	ReleaseRecordName  = "release.json"
)

// RecordFolder is the repository-relative folder that holds one instance's inputs, including
// declared documents and encrypted inputs that a new compilation might no longer render.
func RecordFolder(root, application, environment string) string {
	return root + "/" + application + "/" + environment
}

// AppRecordPath is the shared app-level record, independent of environment.
func AppRecordPath(root, application string) string {
	return root + "/" + application + "/" + AppRecordName
}

// InstanceRecordPath is one instance's own record: its folder holds an instance exactly when this file exists.
func InstanceRecordPath(root, application, environment string) string {
	return RecordFolder(root, application, environment) + "/" + InstanceRecordName
}

// ReleaseRecordPath is the release one instance runs.
func ReleaseRecordPath(root, application, environment string) string {
	return RecordFolder(root, application, environment) + "/" + ReleaseRecordName
}

// InstanceRecordOf is the app and environment whose instance record file is, or false for any other path: a
// record one folder deeper or shallower, or any other file in a record folder.
func InstanceRecordOf(root, file string) (application, environment string, ok bool) {
	rest, found := strings.CutPrefix(file, root+"/")
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] != InstanceRecordName {
		return "", "", false
	}
	return parts[0], parts[1], true
}
