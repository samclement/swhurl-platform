"""What the three live tests that read a deployed release need, and nothing else.

The v1 generator, its records model and the checks that used them are gone: v2's records and its compiled
tree are the only model, and `swhurl-v2` the only writer. These four modules are read by
`livetests.team_scenario` (and `app_template` and `network_policy`, for the instance and cookie names) and
leave with the live tests they serve.

    instance    an app in one environment: its names, folder and unit (GLOSSARY.md)
    calling     what a caller declares, read from a live release's variables
    events      what a release publishes and a subscriber asks for
    helmrelease accessors for a live HelmRelease and its rendered variables
"""
