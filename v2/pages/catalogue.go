package pages

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	connectivityrules "github.com/samclement/swhurl-platform/v2/connectivity/rules"
)

// The catalogue: what the apps of one environment offer each other. One group per app with what it uses, and in it
// one offer for its API and one per event it publishes, whether or not the app describes it. The facts are the
// cluster's, as every page's are; the descriptions are the documents kept beside each instance in Git, decoded by the
// appsource adapter into connectivity's Offers and worded here.

// member is one key of an object value, or the zero value when it has none.
func member(v connectivityrules.Value, name string) connectivityrules.Value {
	if v.Kind != connectivityrules.ValueObject {
		return connectivityrules.Value{}
	}
	for _, field := range v.Fields {
		if field.Name == name {
			return field.Value
		}
	}
	return connectivityrules.Value{}
}

// PayloadField is one row of an event's payload: a field, with a nested field below its parent under its path
// (caller.id, lines[].item).
type PayloadField struct {
	Name, Type string
	Required   bool
	Meaning    string
}

// OfferUser is an app that uses an offer, or one that could: its name, and the team it belongs to or "unknown".
type OfferUser struct{ Name, Team string }

// Offer is one API or event in the catalogue, with what its app says about it and who uses it.
type Offer struct {
	Kind, Name, App            string // Kind is api or event; Name is the app, or <app>.<event>
	KindLabel                  string // the pill's word: API or event
	Event                      string // an event's own name, empty for an API
	Team, TeamLabel            string
	Description                string
	Described                  bool // whether the app's own document describes it
	UsedBy                     []OfferUser
	UsedByTitle                string // every user, for the summary's tooltip
	FirstTwo                   string // the first two, as the row shows them
	More                       int    // how many users the row does not name
	Address, Link              string // an API: its address inside the cluster, and where its description is served
	LinkExternal               bool
	Operations                 []connectivityrules.Operation
	RoutesLabel                string // "3 routes", "API" for an undescribed one
	SamplePath                 string
	Fields                     []PayloadField
	Schema, Example            string
	Commit, CommitURL, Problem string
	CouldUse                   []OfferUser
	HiddenName                 string // the form field an open-connection request sets: calls or subscribe
	FormID, Confirm, Hint      string
}

// Used is one kind and name an app uses, as a pill on its card.
type Used struct{ Kind, Name, Href, Label string }

// CatalogueGroup is one app of the environment: what it uses, and what it offers (nothing, for an app that only uses).
type CatalogueGroup struct {
	App, Team, TeamLabel string
	Uses                 []Used
	Offers               []Offer
}

// CatalogueTally counts what the page shows.
type CatalogueTally struct {
	APIs, Events, Described int
}

// CataloguePage is the Catalogue page: one environment's offers, filtered, by app.
type CataloguePage struct {
	Envs            []Link // staging and prod, in that order
	AllKinds        Link   // every offer
	APIs, Events    Link   // one kind of offer
	AllTeams        Link   // every team of the environment
	Teams           []Link // the environment's teams, in name order
	Groups          []CatalogueGroup
	Tally           CatalogueTally
	Env, Kind, Team string
	Site            Site
	Github          bool
}

// CatalogueInput is what the Catalogue page reads: the rows, the documents of the environment it shows, and the
// filters.
type CatalogueInput struct {
	Rows   []AppRow
	Offers map[string]connectivityrules.Offers // each app's offered documents, for Env
	Env    string
	Kind   string
	Team   string
	Site   Site
	Github bool
}

// EnvironmentNamed is the environment a query names: staging or prod, and staging for anything else.
func EnvironmentNamed(name string) string {
	if environments[name] {
		return name
	}
	return "staging"
}

// CataloguePageOf computes the Catalogue page from its rows, documents and filters.
func CataloguePageOf(input CatalogueInput) CataloguePage {
	env := EnvironmentNamed(input.Env)
	page := CataloguePage{Env: env, Kind: catalogueKind(input.Kind), Team: input.Team, Site: input.Site, Github: input.Github}
	here := rowsOfEnv(input.Rows, env)
	found := offersOf(here, env, input.Offers, input.Site)
	shown := make([]Offer, 0, len(found))
	for _, offer := range found {
		if (page.Kind == "" || offer.Kind == page.Kind) && (page.Team == "" || offer.Team == page.Team) {
			shown = append(shown, offer)
		}
	}
	page.Tally = tallyOf(shown)
	page.Groups = catalogueGroups(here, env, shown, page.Kind, page.Team)
	page.filters(here)
	return page
}

// catalogueKind is the kind a query asks for: api, event, or both.
func catalogueKind(name string) string {
	if name == "api" || name == "event" {
		return name
	}
	return ""
}

// rowsOfEnv is the environment's rows by app name.
func rowsOfEnv(rows []AppRow, env string) []AppRow {
	here := []AppRow{}
	for _, row := range rows {
		if row.Instance.Env == env {
			here = append(here, row)
		}
	}
	slices.SortStableFunc(here, func(a, b AppRow) int { return strings.Compare(a.Instance.App, b.Instance.App) })
	return here
}

// tallyOf counts the offers the page shows.
func tallyOf(shown []Offer) CatalogueTally {
	tally := CatalogueTally{}
	for _, offer := range shown {
		if offer.Kind == "api" {
			tally.APIs++
		} else {
			tally.Events++
		}
		if offer.Described {
			tally.Described++
		}
	}
	return tally
}

// offersOf is every API and event of one environment, APIs first, each with who uses it and who could.
func offersOf(here []AppRow, env string, documents map[string]connectivityrules.Offers, site Site) []Offer {
	teams, others := catalogueTeams(here)
	found := []Offer{}
	for _, row := range here {
		if connection := row.Connections(); connection.Address != "" {
			found = append(found, apiOffer(row, connection, documents[row.Instance.App], env, site, teams, others))
		}
	}
	for _, row := range here {
		found = append(found, eventOffers(row, row.Connections(), documents[row.Instance.App], env, site, teams, others)...)
	}
	return found
}

// catalogueTeams is each app's team by name, and every app as a candidate user.
func catalogueTeams(here []AppRow) (map[string]string, []OfferUser) {
	teams := map[string]string{}
	others := make([]OfferUser, 0, len(here))
	for _, row := range here {
		teams[row.Instance.App] = row.Team()
		others = append(others, OfferUser{Name: row.Instance.App, Team: teamWord(row.Team())})
	}
	return teams, others
}

// apiOffer is one web app's API: its address, its documented routes, and who calls it.
func apiOffer(row AppRow, connection Connections, documents connectivityrules.Offers, env string, site Site,
	teams map[string]string, others []OfferUser) Offer {
	offer := Offer{Kind: "api", Name: row.Instance.App, App: row.Instance.App, KindLabel: "API", Team: row.Team(),
		TeamLabel: teamWord(row.Team()), Address: connection.Address, Link: connection.API, Commit: connection.Offers,
		CommitURL: commitURL(site, row.Instance.App, connection.Offers),
		Problem:   documents.Problem, HiddenName: "calls",
		LinkExternal: strings.HasPrefix(connection.API, "https://")}
	if documents.API != nil {
		offer.Described, offer.Description, offer.Operations = true, documents.API.Description, documents.API.Operations
	}
	offer.RoutesLabel = routeWord(len(offer.Operations))
	offer.SamplePath = samplePath(offer.Operations)
	offer.UsedBy = namedUsers(connection.Callers, teams)
	offer.UsedByTitle, offer.FirstTwo, offer.More = usersSummary(offer.UsedBy)
	offer.FormID = "use-api-" + offer.Name
	offer.Confirm = "Open a pull request so that the app you chose may call " + offer.App + " in " + env + "? It changes that app and " +
		offer.App + ". You review and merge it on GitHub."
	offer.Hint = "The app you choose will call " + offer.App + " in " + env + ". Nothing changes until you merge the pull request."
	if offer.CouldUse = couldUse(others, offer.Name, connection.Callers); len(offer.CouldUse) == 0 {
		offer.Hint = "Every other app in " + env + " uses it already."
	}
	return offer
}

// eventOffers is every event one app publishes, each with who subscribes to it.
func eventOffers(row AppRow, connection Connections, documents connectivityrules.Offers, env string, site Site,
	teams map[string]string, others []OfferUser) []Offer {
	described := map[string]connectivityrules.OfferedEvent{}
	for _, event := range documents.Events {
		described[event.Name] = event
	}
	found := []Offer{}
	for _, name := range connection.Publishes {
		users := []string{}
		for _, subscriber := range connection.Subscribers {
			if subscriber[0] == name {
				users = append(users, subscriber[1])
			}
		}
		found = append(found, eventOffer(row, name, users, described[name], documents.Problem, env, site, teams, others))
	}
	return found
}

// eventOffer is one published event: its payload's rows, an example, and who receives it.
func eventOffer(row AppRow, name string, users []string, described connectivityrules.OfferedEvent, problem, env string, site Site,
	teams map[string]string, others []OfferUser) Offer {
	app := row.Instance.App
	offer := Offer{Kind: "event", Name: app + "." + name, App: app, KindLabel: "event", Event: name, Team: row.Team(),
		TeamLabel: teamWord(row.Team()), Commit: row.Connections().Offers,
		CommitURL: commitURL(site, app, row.Connections().Offers),
		Problem:   problem, HiddenName: "subscribe", Described: described.Name != ""}
	if offer.Described {
		offer.Description, offer.Fields = described.Description, payloadFields(described.Payload)
		offer.Schema, offer.Example = printed(described.Payload), printed(described.Example)
	}
	offer.UsedBy = namedUsers(users, teams)
	offer.UsedByTitle, offer.FirstTwo, offer.More = usersSummary(offer.UsedBy)
	offer.FormID = "use-event-" + offer.Name
	offer.Confirm = "Open a pull request so that the app you chose receives " + offer.Name + " in " + env + "? It changes that app and " +
		app + ". You review and merge it on GitHub."
	offer.Hint = "The app you choose will receive " + offer.Name + " in " + env + ". Nothing changes until you merge the pull request."
	if offer.CouldUse = couldUse(others, app, users); len(offer.CouldUse) == 0 {
		offer.Hint = "Every other app in " + env + " uses it already."
	}
	return offer
}

// namedUsers is the apps of one list, with their teams.
func namedUsers(names []string, teams map[string]string) []OfferUser {
	found := make([]OfferUser, 0, len(names))
	for _, name := range names {
		found = append(found, OfferUser{Name: name, Team: teamWord(teams[name])})
	}
	return found
}

// usersSummary is the users as a row shows them: every name for its tooltip, the first two beside it, and how many it
// leaves out.
func usersSummary(users []OfferUser) (title, firstTwo string, more int) {
	names := make([]string, 0, len(users))
	for _, user := range users {
		names = append(names, user.Name)
	}
	more = max(0, len(names)-2)
	return strings.Join(names, ", "), strings.Join(names[:min(2, len(names))], ", "), more
}

// couldUse is every other app of the environment that does not use the offer yet.
func couldUse(others []OfferUser, app string, users []string) []OfferUser {
	found := []OfferUser{}
	for _, other := range others {
		if other.Name != app && !slices.Contains(users, other.Name) {
			found = append(found, other)
		}
	}
	return found
}

// routeWord is what an API's row says it documents.
func routeWord(count int) string {
	if count == 0 {
		return "API"
	}
	if count == 1 {
		return "1 route"
	}
	return strconv.Itoa(count) + " routes"
}

// samplePath is a route of the API to show in a code sample: its first GET without a parameter, else /.
func samplePath(operations []connectivityrules.Operation) string {
	for _, operation := range operations {
		if operation.Method == "GET" && !strings.Contains(operation.Path, "{") && operation.Path != "/" {
			return operation.Path
		}
	}
	return "/"
}

// catalogueGroups is the offers of each app of the environment, the apps that offer something first, then those that
// only use: `found` is what the page shows, so nothing may be grouped that a filter took away.
func catalogueGroups(here []AppRow, env string, found []Offer, kind, team string) []CatalogueGroup {
	groups := groupedByApp(here, env, found)
	slices.SortStableFunc(groups, offeringFirst)
	return chosenGroups(groups, kind, team)
}

// groupedByApp is every app's card, in app order, with the offers of it that the page shows.
func groupedByApp(here []AppRow, env string, found []Offer) []CatalogueGroup {
	groups := []CatalogueGroup{}
	for _, row := range here {
		group := CatalogueGroup{App: row.Instance.App, Team: row.Team(), TeamLabel: teamWord(row.Team()),
			Uses: usesOf(row, env)}
		for _, offer := range found {
			if offer.App == group.App {
				group.Offers = append(group.Offers, offer)
			}
		}
		groups = append(groups, group)
	}
	return groups
}

// offeringFirst orders the apps that offer something before those that only use.
func offeringFirst(a, b CatalogueGroup) int {
	switch {
	case len(a.Offers) > 0 && len(b.Offers) == 0:
		return -1
	case len(a.Offers) == 0 && len(b.Offers) > 0:
		return 1
	}
	return 0
}

// chosenGroups is the cards a kind and a team leave: a chosen kind leaves out the apps that offer nothing of it.
func chosenGroups(groups []CatalogueGroup, kind, team string) []CatalogueGroup {
	shown := []CatalogueGroup{}
	for _, group := range groups {
		if (len(group.Offers) > 0 || kind == "") && (team == "" || group.Team == team) {
			shown = append(shown, group)
		}
	}
	return shown
}

// usesOf is what one app calls and receives, as the pills on its card.
func usesOf(row AppRow, env string) []Used {
	connection := row.Connections()
	found := []Used{}
	for _, name := range connection.Calls {
		found = append(found, Used{Kind: "api", Name: name, Href: "/catalogue?env=" + env + "#api-" + name, Label: "API"})
	}
	for _, name := range connection.Subscribes {
		found = append(found, Used{Kind: "event", Name: name, Href: "/catalogue?env=" + env + "#event-" + name, Label: "event"})
	}
	return found
}

// filters is the page's filter bar: its environments, its kinds and the teams of this environment.
func (p *CataloguePage) filters(here []AppRow) {
	for _, name := range []string{"staging", "prod"} {
		p.Envs = append(p.Envs, Link{Name: name, Href: catalogueHref(name, p.Kind, p.Team), Here: p.Env == name})
	}
	p.AllKinds = Link{Name: "all", Href: catalogueHref(p.Env, "", p.Team), Here: p.Kind == ""}
	p.APIs = Link{Name: "APIs", Href: catalogueHref(p.Env, "api", p.Team), Here: p.Kind == "api"}
	p.Events = Link{Name: "events", Href: catalogueHref(p.Env, "event", p.Team), Here: p.Kind == "event"}
	p.AllTeams = Link{Name: "all", Href: catalogueHref(p.Env, p.Kind, ""), Here: p.Team == ""}
	for _, name := range teamNames(here) {
		p.Teams = append(p.Teams, Link{Name: name, Href: catalogueHref(p.Env, p.Kind, name), Here: p.Team == name})
	}
}

// teamNames is the teams of one environment's apps, each once, in name order.
func teamNames(here []AppRow) []string {
	names := []string{}
	for _, row := range here {
		if row.Team() != "" && !slices.Contains(names, row.Team()) {
			names = append(names, row.Team())
		}
	}
	slices.Sort(names)
	return names
}

// catalogueHref is one filter's address: the environment, then a kind and a team only when one is chosen. It is written
// as the console wrote it, unescaped, and the template escapes it as an attribute.
func catalogueHref(env, kind, team string) string {
	href := "/catalogue?env=" + env
	if kind != "" {
		href += "&kind=" + kind
	}
	if team != "" {
		href += "&team=" + team
	}
	return href
}

// teamWord is a team as a page names it, "unknown" when the instance names none.
func teamWord(team string) string {
	if team == "" {
		return "unknown"
	}
	return team
}

// commitURL is an app's repository at the commit its offers documents came from.
func commitURL(site Site, app, commit string) string {
	if commit == "" {
		return ""
	}
	return "https://github.com/" + site.Owner + "/" + app + "/tree/" + commit
}

// payloadFields is a payload schema's fields, one row each, nested objects and lists of objects below their parent.
// Empty for a schema that is not an object with properties: the page then shows the schema alone.
func payloadFields(schema connectivityrules.Value) []PayloadField {
	return schemaFields(schema, "", true)
}

// schemaFields is payloadFields from one prefix, where needed says whether an absent parent may still require a field.
func schemaFields(schema connectivityrules.Value, prefix string, needed bool) []PayloadField {
	properties := member(schema, "properties")
	if schema.Kind != connectivityrules.ValueObject || properties.Kind != connectivityrules.ValueObject {
		return []PayloadField{}
	}
	required := requiredNames(member(schema, "required"))
	found := []PayloadField{}
	for _, property := range properties.Fields {
		each := property.Value
		must := needed && slices.Contains(required, property.Name)
		found = append(found, PayloadField{Name: prefix + property.Name, Type: typeWords(each), Required: must,
			Meaning: member(each, "description").Text})
		found = append(found, schemaFields(each, prefix+property.Name+".", must)...)
		found = append(found, schemaFields(member(each, "items"), prefix+property.Name+"[].", must)...)
	}
	return found
}

// requiredNames is a schema's required list.
func requiredNames(required connectivityrules.Value) []string {
	if required.Kind != connectivityrules.ValueArray {
		return nil
	}
	names := make([]string, 0, len(required.Items))
	for _, item := range required.Items {
		names = append(names, item.Text)
	}
	return names
}

// typeWords is a JSON Schema's type as a few words: "string (date-time)", "string or null", "one of a, b".
func typeWords(schema connectivityrules.Value) string {
	if choice := enumWords(schema); choice != "" {
		return choice
	}
	words := typeName(member(schema, "type"))
	if words == "array" && member(schema, "items").Kind == connectivityrules.ValueObject {
		words = "list of " + typeWords(member(schema, "items"))
	}
	if format := member(schema, "format").Text; format != "" {
		return words + " (" + format + ")"
	}
	return words
}

// enumWords is a schema's enumeration as one row says it, empty when it does not enumerate.
func enumWords(schema connectivityrules.Value) string {
	choice := member(schema, "enum")
	if choice.Kind != connectivityrules.ValueArray || len(choice.Items) == 0 {
		return ""
	}
	said := make([]string, 0, len(choice.Items))
	for _, item := range choice.Items {
		said = append(said, pythonText(item))
	}
	return "one of " + strings.Join(said, ", ")
}

// typeName is a schema's type as words: one type, or the alternatives a list of them gives.
func typeName(said connectivityrules.Value) string {
	switch said.Kind {
	case connectivityrules.ValueArray:
		each := make([]string, 0, len(said.Items))
		for _, item := range said.Items {
			each = append(each, pythonText(item))
		}
		if words := strings.Join(each, " or "); words != "" {
			return words
		}
	case connectivityrules.ValueString:
		if said.Text != "" {
			return said.Text
		}
	case connectivityrules.ValueNumber, connectivityrules.ValueBool:
		return pythonText(said)
	}
	return "any"
}

// pythonText is a scalar as Python's str() writes it, for the few words a type is said in an enumeration.
func pythonText(value connectivityrules.Value) string {
	switch value.Kind {
	case connectivityrules.ValueString, connectivityrules.ValueNumber:
		return value.Text
	case connectivityrules.ValueBool:
		if value.Text == "true" {
			return "True"
		}
		return "False"
	case connectivityrules.ValueNull:
		return "None"
	}
	return ""
}

// printed is a document value as the page shows it: json.dumps(indent=2, ensure_ascii=False) of it, and nothing for an
// absent or null one.
func printed(value connectivityrules.Value) string {
	if value.Kind == connectivityrules.ValueAbsent || value.Kind == connectivityrules.ValueNull {
		return ""
	}
	return printValue(value, 0)
}

// printValue is one value at one depth of the printed document.
func printValue(value connectivityrules.Value, depth int) string {
	switch value.Kind {
	case connectivityrules.ValueObject:
		return printObject(value.Fields, depth)
	case connectivityrules.ValueArray:
		return printArray(value.Items, depth)
	case connectivityrules.ValueString:
		return quoteJSON(value.Text)
	case connectivityrules.ValueNull:
		return "null"
	}
	return value.Text
}

// printObject is an object's lines, its keys in document order.
func printObject(fields []connectivityrules.ValueField, depth int) string {
	if len(fields) == 0 {
		return "{}"
	}
	inner := strings.Repeat("  ", depth+1)
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, inner+quoteJSON(field.Name)+": "+printValue(field.Value, depth+1))
	}
	return "{\n" + strings.Join(parts, ",\n") + "\n" + strings.Repeat("  ", depth) + "}"
}

// printArray is an array's lines.
func printArray(items []connectivityrules.Value, depth int) string {
	if len(items) == 0 {
		return "[]"
	}
	inner := strings.Repeat("  ", depth+1)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, inner+printValue(item, depth+1))
	}
	return "[\n" + strings.Join(parts, ",\n") + "\n" + strings.Repeat("  ", depth) + "]"
}

// quoteJSON is one string as JSON writes it, with no HTML escaping: the page's template escapes the markup.
func quoteJSON(text string) string {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(text); err != nil {
		return `""`
	}
	return strings.TrimSuffix(out.String(), "\n")
}
