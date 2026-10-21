"""The app model (docs/plan.md section 24): three small records render every app manifest.

    App        apps/<app>/app.yaml             team and alerts: the same in every environment
    Release    apps/<app>/<env>/release.yaml   what the selected image is and needs; written by intake only
    Settings   apps/<app>/<env>/instance.yaml  what the platform grants this environment, and its allocations

``render`` builds an instance's files, and the shared files, from the records of every instance. Each
capability (workload, exposure, calls, events, egress, sqlite, secrets, telemetry, offers) is one
module that adds its part. ``store`` reads and writes the records, ``write`` is how a command changes
them and writes what follows, and ``generated`` is make check-apps' proof that every committed file is
what the records render.
"""
