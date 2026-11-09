package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/kernel"
	"github.com/samclement/swhurl-platform/v2/pages"
)

// ConsoleOffers is what each instance of one environment offers, read from the documents v2 keeps beside it in the
// pinned tree: the two files the Deploy workflow copies into the instance's offers folder, or why they could not be
// read. An instance with no documents offers nothing, which is not a problem. The read is bounded by what the tree
// filter keeps, so a page can only describe an instance from the commit it also shows.
func ConsoleOffers(tree map[string][]byte, rows []pages.AppRow, env string) map[string]pages.Offers {
	found := map[string]pages.Offers{}
	for _, row := range rows {
		if row.Instance.Env != env {
			continue
		}
		folder := consoleOffersFolder(row.Instance)
		offers, err := appsource.Describe(tree[folder+"/"+kernel.DocumentRoles["api"]],
			tree[folder+"/"+kernel.DocumentRoles["events"]])
		if err != nil {
			offers = pages.Offers{Problem: err.Error()}
		}
		found[row.Instance.App] = offers
	}
	return found
}

// consoleOffersFolder is where a compiled instance keeps the documents it offers.
func consoleOffersFolder(instance pages.Instance) string {
	return consoleApps + "/" + instance.App + "/" + instance.Env + "/offers"
}
