// swhurl-backup runs the platform's database backup jobs inside the backup image: one command per database.
// It is the Go replacement for `python -m swhurl backup-job-sqlite` and keeps that job's contract — the same
// archive names, the same metadata keys, the same S3 upload semantics — so archives written by either are
// restorable by either.
package main

import (
	"context"
	"fmt"
	"os"
)

const usage = `usage:
  swhurl-backup backup-job-sqlite    copy, check, encrypt and publish one SQLite database
  swhurl-backup restore-job-sqlite   restore one SQLite database from its newest archive (not built yet)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	environ := os.Environ()
	var err error
	switch os.Args[1] {
	case "backup-job-sqlite":
		err = backupSQLite(context.Background(), environ, os.Stdout, nil)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[ERROR]", err)
		os.Exit(1)
	}
}
