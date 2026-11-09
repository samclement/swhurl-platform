package pages

// EgressPage is the Egress page: every instance's port on the proxy and the hosts outside the cluster it is granted,
// by environment.
type EgressPage struct {
	Environments []EgressEnvironment
	Instances    int
	Granted      int
	Hosts        int
	Site         Site
}

// EgressEnvironment is one environment's section of the Egress page.
type EgressEnvironment struct {
	Env  string
	Rows []EgressRow
}

// EgressRow is one instance's row on the Egress page.
type EgressRow struct {
	Name, Href, Team, TeamHref string
	Port                       int32
	Egress                     []string
	Ungranted                  string
}

// EgressPageOf computes the Egress page from the rows: an environment with no instance gets no section.
func EgressPageOf(rows []AppRow, site Site) EgressPage {
	page := EgressPage{Instances: len(rows), Site: site}
	hosts := map[string]bool{}
	for _, row := range rows {
		granted := row.Connections().Egress
		if len(granted) == 0 {
			continue
		}
		page.Granted++
		for _, host := range granted {
			hosts[host] = true
		}
	}
	page.Hosts = len(hosts)
	for _, env := range []string{"staging", "prod"} {
		section := EgressEnvironment{Env: env}
		for _, row := range rows {
			if row.Instance.Env == env {
				section.Rows = append(section.Rows, egressRowOf(row, site))
			}
		}
		if len(section.Rows) > 0 {
			page.Environments = append(page.Environments, section)
		}
	}
	return page
}

// egressRowOf is one instance's row.
func egressRowOf(row AppRow, site Site) EgressRow {
	return EgressRow{Name: row.Instance.Name(), Href: "/apps/" + row.Instance.Name() + "#connect", Team: row.Team(),
		TeamHref: "/apps?team=" + row.Team(), Port: row.Connections().ProxyPort, Egress: row.Connections().Egress,
		Ungranted: site.UngrantedURL(row.Instance)}
}
