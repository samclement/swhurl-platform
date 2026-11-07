package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/samclement/swhurl-platform/v2/adapters/kube"
	"github.com/samclement/swhurl-platform/v2/adapters/notifysvc"
	"github.com/samclement/swhurl-platform/v2/adapters/ntfy"
	"github.com/samclement/swhurl-platform/v2/orchestration"
)

// checkerNamed requires exactly one explicit credential; it never guesses which cluster to contact.
func (r *request) checkerNamed() []string {
	if (r.kubeconfig != "") == r.inCluster {
		return []string{""}
	}
	return []string{}
}

// notificationsCheck runs one pass: the CronJob uses in-cluster credentials; Make always uses a host dry run.
// Environment credentials are never included in logs.
func (c cli) notificationsCheck(r request) int {
	logger := &jsonLog{out: c.out, now: time.Now, name: "notifications-check"}
	options := checkerOptions(r.dryRun, os.Getenv)
	if !validConsoleBase(options.Base) {
		logger.write("ERROR", "CONSOLE_URL must be an HTTP(S) console base URL")
		return 1
	}
	if !r.dryRun && options.PlatformAddress == "" {
		logger.write("ERROR", "NTFY_PLATFORM_URL is missing; checker cannot deliver notifications")
		return 1
	}
	connection, _, status := c.connection(r)
	if status != 0 {
		return status
	}
	cluster, err := notifyClusterOf(connection)
	if err != nil {
		logger.write("ERROR", "Kubernetes connection failed")
		return 1
	}
	options.Now = float64(time.Now().UnixNano()) / 1e9
	if !options.DryRun && options.NotifyAddress != "" {
		options.Token = checkerToken(os.Getenv)
	}
	ports := orchestration.CheckerPorts{Cluster: cluster.Reader.(kube.Collector), Data: cluster.Data, Platform: ntfy.New(nil), Copies: notifysvc.New(nil)}
	report, err := orchestration.CheckNotifications(context.Background(), ports, options)
	logChecker(logger, report, r.dryRun)
	if err != nil {
		logger.write("ERROR", err.Error())
		return 1
	}
	logger.write("INFO", fmt.Sprintf("Notification check completed: %d instances, %d pending, dry_run=%t", report.State.Instances(), len(report.State.Pending()), r.dryRun))
	return 0
}

// checkerOptions reads only the explicit environment. Unlike Python, the standalone Go command requires
// CONSOLE_URL: no checkout YAML parser or cluster-specific default is needed.
func checkerOptions(dry bool, getenv func(string) string) orchestration.CheckOptions {
	base := strings.TrimRight(getenv("CONSOLE_URL"), "/")

	return orchestration.CheckOptions{Base: base, PlatformAddress: getenv("NTFY_PLATFORM_URL"), NotifyAddress: getenv("SWHURL_NOTIFY_URL"), DryRun: dry}
}

// checkerToken is read afresh per pass, following projected-token rotation. Failure is reported as an unavailable
// copy by the sender, without preventing platform-topic delivery. The token file itself is never logged.
func checkerToken(getenv func(string) string) string {
	raw, err := os.ReadFile(checkerTokenPath(getenv))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
func logChecker(log *jsonLog, report orchestration.CheckReport, dry bool) {
	for _, offered := range report.Offered {
		if offered.Err == nil {
			log.write("INFO", "Notification delivered: "+offered.Entry.Title())
			continue
		}
		suffix := "gave up"
		if offered.Retry {
			suffix = "will retry"
		}
		if len(offered.Entry.About()) == 0 {
			suffix = "remains pending"
		}
		log.write("ERROR", fmt.Sprintf("Notification delivery failed (%s): %v", suffix, offered.Err))
	}
	if report.State == nil {
		return
	}
	for _, held := range report.State.Held() {
		log.write("INFO", held)
	}
	if dry {
		for _, entry := range report.State.Pending() {
			log.write("INFO", fmt.Sprintf("Would notify: %s (priority %d)", entry.Title(), entry.Priority()))
		}
	}
}

func validConsoleBase(base string) bool {
	u, err := url.Parse(base)
	return err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func checkerTokenPath(getenv func(string) string) string {
	if path := getenv("SWHURL_NOTIFY_TOKEN_FILE"); path != "" {
		return path
	}
	return "/var/run/swhurl/tokens/notify/token"
}
