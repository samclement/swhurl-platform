package kernel

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
