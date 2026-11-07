package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/hoststate"
	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/heartbeat"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// heartbeatTimeout bounds one pass well inside the timer's five minutes.
const heartbeatTimeout = time.Minute

// heartbeatStateFile is where the state is kept under the home directory unless --state names a file.
const heartbeatStateFile = ".local/state/swhurl-platform/notification-heartbeat.json"

// heartbeatNamed requires the kubeconfig, and a --max-age the heartbeat reads and a --state that names a file when
// either is given.
func (r *request) heartbeatNamed() []string {
	if _, ok := heartbeatMaxAge(r.maxAge); !ok || r.maxAgeFlag.set && r.maxAge == "" || r.stateFlag.set && r.statePath == "" {
		return []string{""}
	}
	return []string{r.kubeconfig}
}

// maxAgePattern is a --max-age as the Python heartbeat read it: a number and its unit, s, m or h.
var maxAgePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(s|m|h)$`)

// heartbeatMaxAge is a --max-age in seconds, greater than zero; none given is 0, each job's own.
func heartbeatMaxAge(value string) (float64, bool) {
	if value == "" {
		return 0, true
	}
	match := maxAgePattern.FindStringSubmatch(value)
	if match == nil {
		return 0, false
	}
	amount, err := strconv.ParseFloat(match[1], 64)
	if err != nil || amount <= 0 || math.IsInf(amount, 0) {
		return 0, false
	}
	return amount * map[string]float64{"s": 1, "m": 60, "h": 3600}[match[2]], true
}

// notificationsHeartbeat runs one pass from the host over the watched CronJobs and logs each job as a JSON line. It
// exits 1 when the state cannot be used or any job failed; no line holds the destination.
func (c cli) notificationsHeartbeat(r request) int {
	logger := &jsonLog{out: c.out, now: time.Now, name: "notifications-heartbeat"}
	path, err := heartbeatStatePath(r.statePath)
	if err != nil {
		logger.write("ERROR", err.Error())
		return 1
	}
	connection, _, status := c.connection(r)
	if status != 0 {
		return status
	}
	ports, err := heartbeatPorts(connection, c.platform)
	if err != nil {
		logger.write("ERROR", "Kubernetes connection failed")
		return 1
	}
	maxAge, _ := heartbeatMaxAge(r.maxAge)
	options := orchestration.HeartbeatOptions{Now: float64(time.Now().UnixNano()) / 1e9, Jobs: heartbeatJobs(maxAge),
		StatePath: path, DryRun: r.dryRun}
	ctx, cancel := context.WithTimeout(context.Background(), heartbeatTimeout)
	defer cancel()
	outcomes, err := orchestration.NotificationsHeartbeat(ctx, ports, options)
	if err != nil {
		logger.write("ERROR", err.Error())
		return 1
	}
	return logHeartbeat(logger, outcomes)
}

// heartbeatStatePath is --state, or the file under the home directory.
func heartbeatStatePath(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("heartbeat state path is unknown: HOME is not set")
	}
	return filepath.Join(home, heartbeatStateFile), nil
}

// heartbeatJobs is the watched jobs, each with maxAge instead of its own when one is given.
func heartbeatJobs(maxAge float64) []heartbeat.Job {
	jobs := heartbeat.Watched()
	for i := range jobs {
		if maxAge > 0 {
			jobs[i].MaxAge = maxAge
		}
	}
	return jobs
}

// heartbeatPorts is the pass's reads from the API server, ntfy (the one given, else over HTTPS) and the state file.
func heartbeatPorts(connection kube.Connection, platform ntfy.Publisher) (orchestration.HeartbeatPorts, error) {
	reader, err := connection.Reader()
	if err != nil {
		return orchestration.HeartbeatPorts{}, err
	}
	data, err := connection.Data()
	if err != nil {
		return orchestration.HeartbeatPorts{}, err
	}
	if platform == nil {
		platform = ntfy.New(nil)
	}
	return orchestration.HeartbeatPorts{Cluster: reader, Data: data, Platform: platform, State: hoststate.New()}, nil
}

// logHeartbeat writes one line per job, in Python's words, and is 1 when any job failed.
func logHeartbeat(log *jsonLog, outcomes []orchestration.HeartbeatOutcome) int {
	status := 0
	for _, outcome := range outcomes {
		switch {
		case outcome.Err != nil:
			log.write("ERROR", outcome.Err.Error())
			status = 1
		case outcome.Sent:
			log.write("INFO", "Notification heartbeat sent: "+outcome.Notice.Title)
		case outcome.Due:
			log.write("INFO", fmt.Sprintf("Would notify: %s (priority %d)", outcome.Notice.Title, outcome.Notice.Priority))
		case outcome.Decision.Reason != "":
			log.write("INFO", "Heartbeat for "+outcome.Job.Label+": stale; reminder not due")
		default:
			log.write("INFO", "Heartbeat for "+outcome.Job.Label+": fresh")
		}
	}
	return status
}
