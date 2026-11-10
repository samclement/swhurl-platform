# App-policy mutation fixture

`chart-prod.json` pairs complete compiler-owned source documents with complete, unmodified app-template
5.3.0 Helm output and the profile's policy constraints. It is synthetic: production `demo-orders` from
`v2/testdata/{app,prod,r41,profile}.json`, with SQLite (1 GiB), signed-in exposure, a `created` publication,
a resolved call to `billing:8080`, caller `reader`, and resolved `billing.paid` subscription. The public
broker key is the profile's fixture key. Backup credential fields are compiler substitution references;
no real seed, credential or Secret value is used.

The source was produced by `kernel.CompileWith` and chart output by `helm.Renderer`; no chart/value
translator is used. `TestFullFeatureFixtureMatchesRealPinnedChart` re-renders the embedded compiled values
and compares every full object, so a chart change requires an explicit fixture update. Mutation tests run
without Helm. The real-chart check skips missing prerequisites unless `REQUIRE_HELM=1` is set; the existing
external chart helper (`make check-apps`) populates the pinned chart cache. See the canonical
[policy contract](../../../docs/iteration-2-plan.md#app-policy-all-compiled-instances).
