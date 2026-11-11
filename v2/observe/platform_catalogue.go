package observe

// The catalogue of the platform checks: every group's name, the section its entries print under, what passing means and
// what it reads, worded as the operations page states it. The report is built from this list, so a group's name, its
// section and the order they run in are one fact, and the page's table is checked against it row for row.

// PlatformCatalogueEntry is one group of checks: its name in the report, its section heading, what passing means and
// what it reads (the page's third column, which states what a check needs, not a behaviour).
type PlatformCatalogueEntry struct {
	Name, Section, Passes, Reads string
}

// PlatformCatalogue is every group, in the order the checks run: the six the cluster scope runs, then the rest of
// the platform scope's.
var PlatformCatalogue = []PlatformCatalogueEntry{
	{"flux-units", "Flux Kustomizations",
		"Every Flux unit is Ready",
		"Cluster (read), `swhurl-v2 verify`"},
	{"ingress", "Ingress",
		"Traefik redirects HTTP to HTTPS",
		"Cluster (read), `swhurl-v2 verify`"},
	{"alerts", "Alerts",
		"The Flux failure Alert exists and its provider does (a missing Alert warns)",
		"Cluster (read), `swhurl-v2 verify`"},
	{"notify-service", "Notify service",
		"The notify service runs, on the console's image (another image warns)",
		"Cluster (read), `swhurl-v2 verify`"},
	{"dashboards", "App dashboards and alerts",
		"The platform webhook, dashboard and alert sync succeeded within five minutes",
		"Cluster (read), `swhurl-v2 verify`"},
	{"notifications", "Notifications",
		"The notification checker succeeded within five minutes",
		"Cluster (read), `swhurl-v2 verify`"},
	{"image-automation", "Image Automation",
		"Every app's ImagePolicy is Ready and staging runs the image its record states (warns otherwise)",
		"Cluster (read), this checkout, registry (read), `swhurl-v2 verify`"},
	{"isolation", "App isolation",
		"Every instance v2 compiled has its namespace, a registered team, the platform quota and a NetworkPolicy",
		"Cluster (read), this checkout, `swhurl-v2 verify`"},
	{"signing-keys", "Call identity",
		"The signing keys and token issuer apps were given are the cluster's",
		"Cluster (read), this checkout, `swhurl-v2 verify`"},
	{"events", "Events",
		"NATS and its controller run, every stream and consumer v2 rendered is Ready, and JetStream has room for one more stream (warns otherwise, and names streams no instance declares)",
		"Cluster (read), this checkout, `swhurl-v2 verify`"},
	{"egress", "Egress proxy",
		"The egress proxy runs both replicas, and each instance that reaches it does so on a port of its own from the allocation",
		"Cluster (read), this checkout, `swhurl-v2 verify`"},
	{"channels", "Notification channels",
		"The team routes are Git's and every channel has its Secret (a team without a channel warns)",
		"Cluster (read), this checkout, Secrets (read), `swhurl-v2 verify`"},
	{"backups", "Backups",
		"The newest MongoDB backup in S3, archive and metadata, is younger than `BACKUP_MAX_AGE_HOURS` (26)",
		"Cluster (read), S3 (list), `swhurl-v2 verify`"},
	{"sqlite-backups", "App SQLite backups",
		"Each instance whose release declares a SQLite database has a backup in S3 as young",
		"this checkout, S3 (list), `swhurl-v2 verify`"},
	{"console", "Console",
		"The console image is built from this checkout's tooling (warns otherwise) and its job record exists",
		"Cluster (read), this checkout, `swhurl-v2 verify`"},
	{"push-webhook", "Push Webhook",
		"Flux's GitHub receiver is Ready; GitHub's latest push delivery returned 200 (warns otherwise)",
		"Cluster (read), this checkout, GitHub (read), `swhurl-v2 verify`"},
	{"image-webhooks", "Image Webhooks",
		"The image receiver is Ready; each auto-deploy app's repository calls it (warns otherwise)",
		"Cluster (read), this checkout, GitHub (read), `swhurl-v2 verify`"},
	{"console-token", "Console GitHub Token",
		"GitHub accepts the console's tokens and none expires within 14 days (warns)",
		"Cluster (read), this checkout, Secrets (read), GitHub (read), `swhurl-v2 verify`"},
	{"flux-controllers", "Flux Controllers",
		"The Flux controllers run the pinned version with the settings in Git (warns otherwise)",
		"Cluster (read), this checkout (`flux.json`), `swhurl-v2 verify`"},
	{"runtime-secrets", "Runtime Secrets",
		"The collector ingestion key Secret is present and nonempty",
		"Cluster (read), Secrets (read), `swhurl-v2 verify`"},
	{"ingestion-key", "Ingestion Key Sync",
		"The collectors' ingestion key is the ClickStack team's, compared by bytes and never printed",
		"Cluster (read), Secrets (read), commands in pods, `swhurl-v2 verify`"},
	{"registration", "ClickStack Sign-up",
		"A HyperDX team exists, so registration is closed",
		"Cluster (read), commands in pods, `swhurl-v2 verify`"},
	{"retention", "Retention",
		"Telemetry expires after 30 days and ClickHouse system logs after 7, no merge failed in the last hour, and the MongoDB volume is retained",
		"Cluster (read), commands in pods, `swhurl-v2 verify`"},
	{"notification-heartbeat", "Notification heartbeat",
		"The host heartbeat timer is active and its service's last run succeeded within 15 minutes",
		"this host (`--heartbeat`), `swhurl-v2 verify`"},
}

// platformCheck is one group's results under the section the catalogue gives its name; an uncatalogued name has no
// section, which the catalogue's test refuses.
func platformCheck(name string, entries []PlatformEntry) PlatformCheck {
	for _, catalogued := range PlatformCatalogue {
		if catalogued.Name == name {
			return PlatformCheck{Name: name, Section: catalogued.Section, Entries: entries}
		}
	}
	return PlatformCheck{Name: name, Entries: entries}
}
