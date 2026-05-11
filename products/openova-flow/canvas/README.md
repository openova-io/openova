# `@openova/flow-canvas`

React SVG canvas for OpenovaFlow. Props-driven, **zero OpenOva imports**.

## Props

```tsx
<FlowCanvas
  flow={flowInstance}
  nodes={flowNodes}
  relationships={relationships}
  folded={foldedSet}
  selectedNodeId={null}
  hostNodeId={null}
  palette={statusPalette}         // adapter-supplied
  families={familyDescriptors}
  regions={regionDescriptors}
  perNodeHints={hintMap}
  onNodeOpen={(id) => /* open log pane */}
  onNodeNavigate={(id) => /* deep link */}
  onFoldToggle={(groupId) => /* toggle */}
  onBackgroundClick={() => /* dismiss selection */}
  renderDetail={(id) => <YourLogPane id={id} />}
/>
```

## Theming via CSS variables

Import the default theme once at app root:

```css
@import '@openova/flow-canvas/theme.css';
```

All canvas styling is driven by `--flow-*` CSS variables scoped under
`.flow-canvas-host`. Override individual tokens in your own stylesheet
to reskin without forking. The theme ships dark + light (light is opted
in via `[data-theme="light"]`).

## Edge visual style table (founder-locked)

| Relationship type | Stroke | Annotation | Counted for depth? |
|---|---|---|---|
| `finish-to-start` | solid | normal arrow | yes |
| `start-to-start` | solid | "SS" tag near origin | yes |
| `finish-to-finish` | solid | "FF" tag near terminus | yes |
| `start-to-finish` | dashed | "SF" tag at midpoint | yes |
| `triggers` | solid | ⚡ at midpoint | yes |
| `contains` | NOT rendered as an edge — used for grouping only | n/a | n/a |
| any with `condition === 'on-failure'` | red dashed, low-opacity until pred is failed | normal arrow | **NO** |

## Embedding example

```tsx
import { FlowCanvas } from '@openova/flow-canvas'
import '@openova/flow-canvas/theme.css'

function MyPage() {
  return (
    <div style={{ height: '80vh' }}>
      <FlowCanvas
        flow={{ id: 'my-flow', status: 'running', startedAt: Date.now() }}
        nodes={[/* FlowNode[] */]}
        relationships={[/* Relationship[] */]}
        folded={new Set()}
      />
    </div>
  )
}
```

## Testing

```sh
npm test               # vitest --pool=threads --maxWorkers=2 --no-isolate
npm run typecheck      # tsc --noEmit -p tsconfig.json
```

NEVER `npm run build` / `playwright install` / `playwright test`.
