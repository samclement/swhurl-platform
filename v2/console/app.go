package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/orchestration"
	"github.com/samclement/swhurl-platform/v2/pages"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// One instance's page and a promotion's review, and the documents they read: the stack catalogue the New app form
// shows, the variables a committed HelmRelease sets, the settings the Scale form starts from, and the Feature flags
// card in the template's own words.

// stacksPath is where the pinned tree keeps the stack catalogue.
const stacksPath = "v2/contract/stacks.json"

// FlagCards reads an instance's overrides from the console's flag store: the card for its declaration, and whether
// the overrides can be changed.
type FlagCards func(ctx context.Context, instance pages.Instance, declaration []producer.Flag,
	problem string) (orchestration.FlagCard, bool)

// AppRow is the pinned tree, every row, and the instance's row (nil when the cluster has no such instance).
func (s Server) AppRow(ctx context.Context, instance pages.Instance) (map[string][]byte, []pages.AppRow,
	*pages.AppRow, error) {
	tree, rows, err := s.Rows(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range rows {
		if rows[i].Instance == instance {
			return tree, rows, &rows[i], nil
		}
	}
	return tree, rows, nil, nil
}

// AppPage is one deployed instance's page from the tree and rows its request read, the jobs it shows and the flag
// store's card for what the instance declares.
func (s Server) AppPage(ctx context.Context, instance pages.Instance, tree map[string][]byte, rows []pages.AppRow,
	jobs []pages.Job, cards FlagCards) (pages.AppPage, error) {
	site, err := Site(tree, s.Repository)
	if err != nil {
		return pages.AppPage{}, err
	}
	registry, registryErr := TeamRegistry(tree)
	teamsError := ""
	if registryErr != nil {
		registry, teamsError = map[string]pages.TeamInfo{}, registryErr.Error()
	}
	declaration, problem := orchestration.FlagDeclaration(tree[FlagDocument(instance)])
	card, writable := cards(ctx, instance, declaration, problem)
	variables, variablesKnown := DesiredVariables(tree, instance.App, instance.Env)
	connectable := pages.ConnectableTo(rows, Offers(tree, rows, instance.Env), instance.Env, instance.App)
	return pages.AppPageOf(pages.AppInput{Instance: instance, Rows: rows, Site: site,
		Stacks: s.Stacks(ctx, []string{instance.App}), Dashboards: s.Dashboards(ctx), Jobs: jobs,
		Flags: FlagView(instance.Env, card), FlagChanged: "", FlagsWritable: writable, Teams: registry,
		TeamsError: teamsError, Settings: Settings(tree, instance.App, instance.Env), Variables: variables,
		VariablesKnown: variablesKnown, Connectable: connectable, Github: s.GitHub}), nil
}

// FlagDocument is the path an instance's offered flags document takes in the pinned tree.
func FlagDocument(instance pages.Instance) string {
	return kernel.RecordFolder(kernel.RecordsRoot, instance.App, instance.Env) + "/offers/" + kernel.DocumentRoles["flags"]
}

// PromotionReview is staging's image against production's, or why the promotion cannot be reviewed.
func PromotionReview(app string, rows []pages.AppRow) (pages.PromotionReview, string) {
	staging, prod := promotionRows(app, rows)
	if staging == nil {
		return pages.PromotionReview{}, "its records are not in Git, so what it declares is unknown"
	}
	if prod == nil {
		return pages.PromotionReview{}, "v2 has no first promotion: production has no records yet"
	}
	return pages.PromotionOf(app, staging, prod), ""
}

// promotionRows is one app's staging and production rows, each only when its records declare it.
func promotionRows(app string, rows []pages.AppRow) (*pages.AppRow, *pages.AppRow) {
	var staging, prod *pages.AppRow
	for i := range rows {
		row := &rows[i]
		if row.Instance.App != app || row.Inventory == nil || row.Removed != "" {
			continue
		}
		switch row.Instance.Env {
		case "staging":
			staging = row
		case "prod":
			prod = row
		}
	}
	return staging, prod
}

// StackCatalogue reads v2/contract/stacks.json from the pinned tree: the stacks in the order the file states them and
// the app types their templates build, each with the included answers its pills name.
func StackCatalogue(tree map[string][]byte) (pages.StackCatalogue, error) {
	data, known := tree[stacksPath]
	if !known {
		return pages.StackCatalogue{}, fmt.Errorf("cannot read %s: not in the repository", stacksPath)
	}
	document, err := orchestration.DecodeOrdered(data)
	if err != nil {
		return pages.StackCatalogue{}, fmt.Errorf("cannot read %s: %w", stacksPath, err)
	}
	types := appTypes(document.Member("types"))
	builders := map[string][]string{}
	// A stack's line names the types it builds, in the file's order.
	for _, stack := range document.Member("stacks").Fields {
		for _, appType := range types {
			for _, built := range appType.Stacks {
				if built == stack.Name {
					builders[stack.Name] = append(builders[stack.Name], appType.Key)
				}
			}
		}
	}
	catalogue := pages.StackCatalogue{Types: types}
	for _, stack := range document.Member("stacks").Fields {
		info := pages.StackInfo{Name: stack.Name, Label: pages.StackLabelOf(stack.Name),
			Repository:  stack.Value.Member("repository").Text(),
			Description: stack.Value.Member("description").Text()}
		info.Types = strings.Join(builders[stack.Name], " ")
		catalogue.Stacks = append(catalogue.Stacks, info)
	}
	return catalogue, nil
}

// appTypes is the catalogue's types, in the file's order.
func appTypes(types orchestration.OrderedValue) []pages.AppType {
	found := make([]pages.AppType, 0, len(types.Fields))
	for _, field := range types.Fields {
		answers := []pages.NamedValue{}
		for _, answer := range field.Value.Member("answers").Fields {
			answers = append(answers, pages.NamedValue{Name: answer.Name, Value: answer.Value.Text()})
		}
		stacks := []string{}
		for _, stack := range field.Value.Member("stacks").Items {
			stacks = append(stacks, stack.Text())
		}
		found = append(found, pages.AppType{Key: field.Name, Title: field.Value.Member("title").Text(),
			Text: field.Value.Member("text").Text(), Stacks: stacks, Answers: answers,
			Exposure: field.Value.Member("exposure").Text(), Kind: answerKind(answers),
			Included: pages.IncludedOf(answers)})
	}
	return found
}

// answerKind is a type's template answer that says what it runs as: web, worker or job.
func answerKind(answers []pages.NamedValue) string {
	for _, answer := range answers {
		if answer.Name == "kind" {
			return answer.Value
		}
	}
	return ""
}

// DesiredVariables is the variables the instance's committed HelmRelease sets, in the document's order, and whether
// one is committed at all. A value that comes from a reference is shown by where it comes from, never read.
func DesiredVariables(tree map[string][]byte, app, env string) ([]pages.NamedValue, bool) {
	helm, known := tree[pages.ConsoleAppsRoot+"/"+app+"/"+env+"/helmrelease.json"]
	if !known {
		return nil, false
	}
	var release struct {
		Spec struct {
			Values struct {
				Controllers map[string]struct {
					Containers map[string]struct {
						Env json.RawMessage `json:"env"`
					} `json:"containers"`
				} `json:"controllers"`
			} `json:"values"`
		} `json:"spec"`
	}
	if json.Unmarshal(helm, &release) != nil {
		return nil, false
	}
	envBlock := release.Spec.Values.Controllers["main"].Containers["main"].Env
	if len(bytes.TrimSpace(envBlock)) == 0 {
		return []pages.NamedValue{}, true
	}
	found := []pages.NamedValue{}
	ordered, err := orchestration.DecodeOrdered(envBlock)
	if err == nil && ordered.Kind == 'a' {
		for _, entry := range ordered.Items {
			found = append(found, pages.NamedValue{Name: entry.Member("name").Text(),
				Value: variableText(entry.Member("value"), entry.Member("valueFrom"))})
		}
		return found, true
	}
	if err == nil && ordered.Kind == 'o' {
		for _, field := range ordered.Fields {
			found = append(found, pages.NamedValue{Name: field.Name, Value: variableText(field.Value, field.Value)})
		}
		return found, true
	}
	return nil, false
}

// variableText is one variable as the page shows it: its value, or where the pod or a reference gets it from.
func variableText(value, from orchestration.OrderedValue) string {
	if text := value.Text(); text != "" || (value.Kind == 0 && value.Scalar != nil) {
		return text
	}
	if value.Kind == 'o' {
		from = value.Member("valueFrom")
	}
	if path := from.Member("fieldRef").Member("fieldPath").Text(); path != "" {
		return "from the pod (" + path + ")"
	}
	return "from a reference"
}

// Settings is the instance's configuration the Scale form shows as placeholders.
func Settings(tree map[string][]byte, app, env string) map[string]string {
	instance, known := tree[kernel.InstanceRecordPath(kernel.RecordsRoot, app, env)]
	if !known {
		return map[string]string{}
	}
	var spec struct {
		Configuration struct {
			Replicas  int64 `json:"replicas"`
			MemoryMiB int64 `json:"memoryMiB"`
		} `json:"configuration"`
	}
	if json.Unmarshal(instance, &spec) != nil {
		return map[string]string{}
	}
	return map[string]string{"replicas": strconv.FormatInt(spec.Configuration.Replicas, 10),
		"memoryMiB": strconv.FormatInt(spec.Configuration.MemoryMiB, 10)}
}

// FlagView is the Feature flags card as the template prints it: each row's declaration, its override and what the
// app runs with, and the problem that kept any of them out.
func FlagView(environment string, card orchestration.FlagCard) pages.FlagView {
	view := pages.FlagView{Problem: card.Problem}
	for _, row := range card.Rows {
		line := pages.FlagLine{Name: row.Name, Undeclared: row.Undeclared, Overridden: row.Overridden,
			Stale: row.Stale, AgeKnown: row.AgeDays >= 0, AgeWord: pages.FlagAge(row.AgeDays)}
		if !row.Undeclared {
			line.Description, line.Type = row.Flag.Description(), row.Flag.Type()
			line.Declared = pages.JinjaJSON(plainValue(row.Flag.Declared(environment)))
			line.Min = scalarTextValue(row.Flag.Declaration.Get("min"))
			line.Max = scalarTextValue(row.Flag.Declaration.Get("max"))
			if values, ok := row.Flag.Declaration.Get("enum").([]any); ok {
				for _, value := range values {
					line.Enum = append(line.Enum, scalarTextValue(value))
				}
			}
			line.Effective = pages.JinjaJSON(plainValue(row.Effective))
			line.EffectiveRaw = orchestration.ScalarText(row.Effective)
		}
		if row.Overridden && row.Override.Readable {
			line.OverrideText = pages.JinjaJSON(plainValue(row.Override.Value))
		}
		view.Rows = append(view.Rows, line)
	}
	return view
}

// plainValue is a declaration's value as the JSON a page prints it as: a source object becomes a mapping.
func plainValue(value any) any {
	switch found := value.(type) {
	case producer.Object:
		fields := map[string]any{}
		for _, field := range found {
			if name, ok := field.Name.(string); ok {
				fields[name] = plainValue(field.Value)
			}
		}
		return fields
	case []any:
		items := make([]any, 0, len(found))
		for _, item := range found {
			items = append(items, plainValue(item))
		}
		return items
	}
	return value
}

// scalarTextValue is a declared scalar as Python prints it, empty when the declaration states none.
func scalarTextValue(value any) string {
	if value == nil {
		return ""
	}
	return orchestration.ScalarText(value)
}
