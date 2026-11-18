# Product

## Register

product

## Users

FX traders on a bank dealing desk (forwards and money-market dealers covering G7, GCC and other pairs). The dashboard sits on a side monitor beside the dealing screens all day, mostly glanced at; when a flag appears the trader turns to it and drills into one pair and tenor to check the legs, then verifies on a dealing screen. They are fluent in desk language (bid/offer, points, bp p.a., tenors, value dates) and impatient with explanation.

## Product Purpose

A flagging tool. It compares market forwards with the forward implied by covered interest parity across 32 pairs and 15 tenors, and flags the gaps that survive dealing and funding costs. Nothing is traded. Quotes are delayed and indicative, so a flag is a lead to check, never a price to deal on. Success: a trader can tell in one glance whether anything is flagged and whether the feed is healthy, and in one click see the exact legs, levels and edge behind a flag. Replay and back-test let them judge how the flags would have behaved on recorded history.

## Brand Personality

Terse, exact, trustworthy. Reads like a dealing screen, not a tutorial: desk shorthand for labels, numbers first, explanation on demand (tooltips, a legend), never in the way. Honest about data quality: staleness, delay and missing quotes are always visible.

## Anti-references

- Consumer fintech and crypto dashboards: neon, gradients, glow, hero numbers.
- SaaS analytics templates: card grids, big-number tiles with decorative accents.
- Tutorial-style UIs with paragraphs of plain-English explanation above every table.
- Anything that makes an indicative lead look like a dealable price or a realised profit.

## Design Principles

1. Glance first, drill second. Flag state and feed health must be readable from across the desk; detail appears on selection.
2. Numbers are the interface. Tabular, aligned, signed, with units stated once in headers rather than repeated in prose.
3. Desk shorthand. Use the words a dealer uses (bid/offer, pts, bp p.a., sell/buy fwd, tenor); keep long definitions in tooltips.
4. Never overstate. Indicative and delayed data is labelled as such; potential P&L is always called potential.
5. Color means state. Color is reserved for flags, direction (rich/cheap) and feed health, never decoration.

## Accessibility & Inclusion

Dark theme by default with a light option. WCAG AA contrast for text. Direction and flag state never rely on color alone (sign, label or outline accompanies color); rich/cheap uses a red/blue pair that survives red-green color blindness. Full keyboard operation of the grid and controls, visible focus, reduced-motion respected.
