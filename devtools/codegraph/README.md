# codegraph

A read-only development aid: derives dependency graphs, entry-point journeys, hotspots and changes between commits from source in Git. It reads Git objects and parses them; it never imports or runs the code it analyses, and never changes the checkout. Python standard library only.

The design, contracts, document fields and roadmap are in [docs/visual-change-tool.md](../../docs/visual-change-tool.md); this page covers running it.

```bash
export PYTHONPATH=devtools/codegraph
python3 -m codegraph snapshot HEAD --scope tools/swhurl --text        # a summary
python3 -m codegraph snapshot HEAD --scope tools/swhurl > head.json   # the snapshot document
python3 -m codegraph snapshot 334a0f11 --scope tools/swhurl --repo .  # any commit, branch or tag
python3 -m codegraph entries head.json --text                         # entry points and the rule that found each
python3 -m codegraph reach head.json py:swhurl.apps.cli:main_promote  # what one node reaches, and its spine
python3 -m codegraph snapshot HEAD --scope tools/swhurl | python3 -m codegraph entries -
python3 -m codegraph entries head.json > entries.json
python3 -m codegraph journeys head.json entries.json --text    # entry points grouped by distinctive shared reach
make check-codegraph                                                  # its tests; part of make check
```

`--scope` is a package directory; modules are named from its parent (`tools/swhurl/apps/cli.py` is `swhurl.apps.cli`). A scope that does not exist at that revision gives an empty snapshot. `entries` and `reach` read a snapshot document, and `journeys` a snapshot and the entries document derived from it, each from a path or `-` (stdin); none of them reads Git. Exit 0 on success, 1 on failure (`codegraph: <code>: <detail>` on stderr, nothing on stdout), 2 on a usage error.
