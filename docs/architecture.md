# Architecture (C4)

This document captures C4-style architecture views for the active platform layout.

Chart sources:
- `docs/charts/c4/context.d2`
- `docs/charts/c4/container.d2`
- `docs/charts/c4/component-app-example.d2`

Generate rendered charts:

```bash
make charts-generate
```

Rendered output path:
- `docs/charts/c4/rendered/*.svg`

## Level 1: System Context

![C4 Context](charts/c4/rendered/context.svg)

## Level 2: Container

![C4 Container](charts/c4/rendered/container.svg)

## Level 3: Component (Example App Request Path)

![C4 Component Example App](charts/c4/rendered/component-app-example.svg)
